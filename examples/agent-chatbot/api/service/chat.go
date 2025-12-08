package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"agent-chatbot/api/config"
	apimodel "agent-chatbot/api/model"
	"agent-chatbot/api/service/agent"
	"agent-chatbot/api/service/db"
)

const (
	defaultSystemPrompt = "You are a helpful assistant. Answer questions concisely and clearly."

	ragSystemPrompt = `You are a helpful assistant with access to relevant documents.
Use the following context to answer the user's question accurately and completely.

IMPORTANT FORMATTING GUIDELINES:
1. When presenting hierarchical data (scores, categories, etc.):
   - Use markdown tables for structured data
   - Show parent-child relationships clearly
   - Include both values AND maximum scores (e.g., "84/125점")
   - Calculate and show percentages for scores when possible
   - Highlight exceptional scores (최고/최저) with ★

2. For score breakdowns:
   - Start with the top-level summary
   - Then show each category with its sub-scores
   - Group related items together
   - Use bullet points for individual scores

3. Example format for vehicle performance scores:
   | 카테고리 | 점수 | 만점 | 비율 |
   |----------|------|------|------|
   | PT총점 | 84 | 125 | 67.2%% |
   | 컴포트총점 | 114 | 150 | 76.0%% |

   **PT총점 세부 (84/125점)**:
   - 발진가속: 11/15 | 추월성능: 11/15 | 최고속도: 3/5
   - 충전/주유: 15/15 ★ (최고점)

4. Always provide complete information when the context contains it.
   If the user asks for "모든 하위 점수" or "세부 점수", include ALL available sub-scores.

5. If the context doesn't contain relevant information, clearly state what's missing.

%s

Context from retrieved documents:
%s`

	queryRewritePrompt = `You are a helpful assistant. Your task is to generate 3 different versions
of the user's query to improve document retrieval. Generate queries from different perspectives.

Return ONLY the queries, one per line. No numbering, no explanation.

User query: %s`
)

// clarificationNeededError is returned when additional clarification is needed
type clarificationNeededError struct {
	Request *ClarificationRequest
	Phase   apimodel.ClarificationPhase
}

func (e *clarificationNeededError) Error() string {
	if e.Request != nil {
		return fmt.Sprintf("clarification needed: %s", e.Request.Reason)
	}
	return "clarification needed"
}

// IsClarificationNeeded checks if an error is a clarification needed error
func IsClarificationNeeded(err error) (*ClarificationRequest, bool) {
	if cne, ok := err.(*clarificationNeededError); ok {
		return cne.Request, true
	}
	return nil, false
}

// ChatService handles chat operations with optional debug mode
type ChatService struct {
	chain             compose.Runnable[map[string]any, *schema.Message]
	chatModel         model.ChatModel
	retriever         retriever.Retriever
	planner           *PlannerService
	sessionStore      *SessionStore
	neo4jClient       *db.Neo4jClient
	debugEnabled      bool
	comparisonService *ComparisonGraphService // Comparison query service

	// Agent mode
	graphAgent   *agent.GraphRAGAgent // ReAct agent for iterative search
	useAgentMode bool                 // Enable agent mode
}

// NewChatService creates a new chat service
func NewChatService(ctx context.Context, cfg *config.Config, sessionStore *SessionStore) (*ChatService, error) {
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: cfg.OpenAIAPIKey,
		Model:  cfg.Model,
	})
	if err != nil {
		return nil, err
	}

	chatTemplate := prompt.FromMessages(
		schema.FString,
		schema.SystemMessage("{system_prompt}"),
		schema.MessagesPlaceholder("history", true),
		schema.UserMessage("{input}"),
	)

	chain, err := compose.NewChain[map[string]any, *schema.Message]().
		AppendChatTemplate(chatTemplate).
		AppendChatModel(chatModel).
		Compile(ctx)
	if err != nil {
		return nil, err
	}

	// Create planner service
	planner := NewPlannerService(chatModel)

	// Initialize retriever based on config
	var activeRetriever retriever.Retriever
	var neo4jClient *db.Neo4jClient

	if cfg.GraphRAGEnabled {
		// Try to connect to Neo4j and create GraphRAGRetriever
		neo4jClient, err = db.NewNeo4jClient(ctx, cfg.Neo4jURI, cfg.Neo4jUsername, cfg.Neo4jPassword)
		if err != nil {
			log.Printf("Warning: Failed to connect to Neo4j: %v. Falling back to MockRetriever.", err)
			activeRetriever = NewMockRetriever()
		} else {
			// Ensure Full-Text index exists
			if err := neo4jClient.EnsureFullTextIndex(ctx); err != nil {
				log.Printf("Warning: Failed to ensure Full-Text index: %v", err)
			}

			// Create GraphRAGRetriever with shortest path support
			graphRAG, err := NewGraphRAGRetriever(&GraphRAGConfig{
				Neo4jClient:        neo4jClient,
				ChatModel:          chatModel,
				DefaultTopK:        cfg.DefaultGraphDepth,
				MaxGraphDepth:      cfg.DefaultGraphDepth,
				EnableReranking:    true,
				EnableShortestPath: true, // Enable shortest path navigation
			})
			if err != nil {
				log.Printf("Warning: Failed to create GraphRAGRetriever: %v. Falling back to MockRetriever.", err)
				activeRetriever = NewMockRetriever()
			} else {
				activeRetriever = graphRAG
				log.Printf("GraphRAGRetriever initialized successfully with Neo4j at %s", cfg.Neo4jURI)
			}
		}
	} else {
		// Use mock retriever when Graph RAG is disabled
		activeRetriever = NewMockRetriever()
		log.Println("Using MockRetriever (Graph RAG disabled)")
	}

	// Initialize comparison service if GraphRAG is available
	var comparisonService *ComparisonGraphService
	if graphRAG, ok := activeRetriever.(*GraphRAGRetriever); ok && graphRAG != nil {
		comparisonService = NewComparisonGraphService(&ComparisonGraphConfig{
			GraphRAGRetriever: graphRAG,
			ChatModel:         chatModel,
			Neo4jClient:       neo4jClient,
		})
		log.Println("ComparisonGraphService initialized successfully")
	}

	// Initialize GraphRAG Agent if enabled
	var graphAgent *agent.GraphRAGAgent
	useAgentMode := cfg.UseAgentMode && cfg.GraphRAGEnabled && neo4jClient != nil
	if useAgentMode {
		var agentErr error
		graphAgent, agentErr = agent.NewGraphRAGAgent(ctx, &agent.AgentConfig{
			Neo4jClient:         neo4jClient,
			ChatModel:           chatModel,
			MaxIterations:       cfg.MaxAgentIterations,
			ConfidenceThreshold: cfg.ConfidenceThreshold,
			EnableDebug:         true,
		})
		if agentErr != nil {
			log.Printf("Warning: Failed to create GraphRAGAgent: %v. Agent mode disabled.", agentErr)
			useAgentMode = false
		} else {
			log.Printf("GraphRAGAgent initialized successfully (max iterations: %d, confidence: %.2f)",
				cfg.MaxAgentIterations, cfg.ConfidenceThreshold)
		}
	}

	return &ChatService{
		chain:             chain,
		chatModel:         chatModel,
		retriever:         activeRetriever,
		planner:           planner,
		sessionStore:      sessionStore,
		neo4jClient:       neo4jClient,
		debugEnabled:      true,
		comparisonService: comparisonService,
		graphAgent:        graphAgent,
		useAgentMode:      useAgentMode,
	}, nil
}

// ChatStream handles non-debug streaming chat (uses RAG without debug events)
func (c *ChatService) ChatStream(ctx context.Context, sessionID, input string) (*schema.StreamReader[*schema.Message], error) {
	// Use the same RAG logic as debug mode, but without emitting debug events
	noopEmitter := func(event string, data interface{}) {}
	return c.ChatStreamWithDebug(ctx, sessionID, input, noopEmitter)
}

// ChatStreamWithDebug handles streaming chat with debug events
func (c *ChatService) ChatStreamWithDebug(
	ctx context.Context,
	sessionID, input string,
	emitter DebugEmitter,
) (*schema.StreamReader[*schema.Message], error) {
	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Set emitter for planner
	c.planner.SetEmitter(emitter)

	// Step 0a: Check for comparison query FIRST (before regular clarification)
	if c.comparisonService != nil {
		c.comparisonService.SetDebugEmitter(emitter)

		// Get pending comparison clarification if any
		comparisonPending := c.sessionStore.GetPendingComparisonClarification(sessionID)

		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "comparison_check",
			StepName:  "ComparisonQueryDetection",
			Component: "comparison",
			Status:    apimodel.StatusStarted,
			Input:     map[string]string{"query": truncateString(input, 100)},
			Timestamp: time.Now().UnixMilli(),
		})

		result, err := c.comparisonService.ProcessComparisonQuery(ctx, input, comparisonPending)
		if err == nil && result != nil && result.IsComparison {
			emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
				StepID:    "comparison_check",
				StepName:  "ComparisonQueryDetection",
				Component: "comparison",
				Status:    apimodel.StatusCompleted,
				Output: map[string]interface{}{
					"is_comparison":        result.IsComparison,
					"needs_clarification":  result.NeedsClarification,
					"entity_count":         len(result.ResolvedEntities),
				},
				Timestamp: time.Now().UnixMilli(),
			})

			if result.NeedsClarification {
				// Store pending comparison clarification
				c.sessionStore.SetPendingComparisonClarification(sessionID, result.PendingState)

				// Emit clarification request (converted to standard format)
				clarificationReq := c.convertComparisonClarification(result.ClarificationRequest)
				log.Printf("[DEBUG] Emitting comparison clarification:request with Entity=%s, Options=%d",
					result.ClarificationRequest.Entity,
					len(result.ClarificationRequest.Options))
				emitter(apimodel.EventClarificationReq, clarificationReq)

				return nil, fmt.Errorf("comparison_clarification_required")
			}

			// Clear any pending comparison clarification
			c.sessionStore.ClearPendingComparisonClarification(sessionID)

			// Comparison query processed successfully - stream response with comparison context
			return c.streamComparisonResponseWithViz(ctx, session, input, result, emitter)
		}

		// Not a comparison query - continue with regular flow
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "comparison_check",
			StepName:  "ComparisonQueryDetection",
			Component: "comparison",
			Status:    apimodel.StatusCompleted,
			Output: map[string]interface{}{
				"is_comparison": false,
				"reason":        "not_a_comparison_query",
			},
			Timestamp: time.Now().UnixMilli(),
		})
	}

	// Check for pending clarification first
	pending, _ := c.sessionStore.GetPendingClarification(sessionID)

	// Step 0: Check ClarificationOrchestrator for initial node clarification
	orchestrator := c.GetClarificationOrchestrator(emitter)
	var docs []*schema.Document
	var plan *Plan
	skipRetrieval := false

	if orchestrator != nil && c.GetGraphRAGRetriever() != nil {
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "clarification_check",
			StepName:  "ClarificationOrchestrator",
			Component: "clarification",
			Status:    apimodel.StatusStarted,
			Input:     map[string]string{"query": truncateString(input, 100)},
			Timestamp: time.Now().UnixMilli(),
		})

		decision, err := orchestrator.ProcessQuery(ctx, input, pending)
		if err == nil {
			emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
				StepID:    "clarification_check",
				StepName:  "ClarificationOrchestrator",
				Component: "clarification",
				Status:    apimodel.StatusCompleted,
				Output: map[string]interface{}{
					"needs_clarification": decision.NeedsClarification,
					"phase":               string(decision.Phase),
				},
				Timestamp: time.Now().UnixMilli(),
			})

			if decision.NeedsClarification {
				// Store pending clarification state
				c.sessionStore.SetPendingClarification(sessionID, &apimodel.PendingClarification{
					Phase:          decision.Phase,
					OriginalQuery:  input,
					SourceNodeUUID: decision.SourceNodeUUID,
					SourceNodeName: decision.SourceNodeName,
					CreatedAt:      time.Now(),
				})

				// Emit clarification request to client
				log.Printf("[DEBUG] Emitting clarification:request with RequestID=%s, Options=%d",
					decision.ClarificationRequest.RequestID,
					len(decision.ClarificationRequest.Options))
				emitter(apimodel.EventClarificationReq, decision.ClarificationRequest)

				// Return special error to indicate clarification is needed
				return nil, fmt.Errorf("clarification_required")
			}

			// Use documents from orchestrator decision if available
			if len(decision.Documents) > 0 {
				docs = decision.Documents
				skipRetrieval = true
			}
		} else {
			emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
				StepID:    "clarification_check",
				StepName:  "ClarificationOrchestrator",
				Component: "clarification",
				Status:    apimodel.StatusCompleted,
				Output:    map[string]interface{}{"error": err.Error()},
				Timestamp: time.Now().UnixMilli(),
			})
		}
	}

	// Clear any pending clarification since we're proceeding
	c.sessionStore.ClearPendingClarification(sessionID)

	// If we don't have documents from orchestrator, do regular retrieval
	if !skipRetrieval {
		// Step 1: Create execution plan
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "planning",
			StepName:  "Planner",
			Component: apimodel.ComponentPlanner,
			Status:    apimodel.StatusStarted,
			Input:     map[string]string{"query": truncateString(input, 100)},
			Timestamp: time.Now().UnixMilli(),
		})

		var planErr error
		plan, planErr = c.planner.CreatePlan(ctx, input)
		if planErr != nil {
			// Continue without plan on error
			plan = nil
		}

		planSteps := 0
		if plan != nil {
			planSteps = len(plan.Steps)
		}
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "planning",
			StepName:  "Planner",
			Component: apimodel.ComponentPlanner,
			Status:    apimodel.StatusCompleted,
			Output:    map[string]interface{}{"plan_steps": planSteps},
			Timestamp: time.Now().UnixMilli(),
		})

		// Step 2: Query rewriting (MultiQuery)
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "query_rewrite",
			StepName:  "QueryRewriter",
			Component: apimodel.ComponentQueryRewriter,
			Status:    apimodel.StatusStarted,
			Input:     map[string]string{"original_query": truncateString(input, 100)},
			Timestamp: time.Now().UnixMilli(),
		})

		queries, rewriteErr := c.rewriteQueries(ctx, input)
		if rewriteErr != nil {
			queries = []string{input} // Fallback to original query
		}

		emitter(apimodel.EventDebugQuery, apimodel.DebugQueryPayload{
			OriginalQuery:    input,
			RewrittenQueries: queries,
			Timestamp:        time.Now().UnixMilli(),
		})

		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "query_rewrite",
			StepName:  "QueryRewriter",
			Component: apimodel.ComponentQueryRewriter,
			Status:    apimodel.StatusCompleted,
			Output:    map[string]interface{}{"queries_count": len(queries)},
			Timestamp: time.Now().UnixMilli(),
		})

		// Step 3: Parallel retrieval
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "retrieval",
			StepName:  "Retriever",
			Component: apimodel.ComponentRetriever,
			Status:    apimodel.StatusStarted,
			Input:     map[string]interface{}{"queries": queries},
			Timestamp: time.Now().UnixMilli(),
		})

		docs = c.retrieveWithDebug(ctx, queries, emitter)

		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "retrieval",
			StepName:  "Retriever",
			Component: apimodel.ComponentRetriever,
			Status:    apimodel.StatusCompleted,
			Output:    map[string]interface{}{"total_documents": len(docs)},
			Timestamp: time.Now().UnixMilli(),
		})
	}

	// Step 4: Build context and generate response
	systemPrompt := c.buildRAGSystemPrompt(session.SystemPrompt, plan, docs)

	// Create debug callback handler
	debugHandler := NewDebugCallbackHandler(emitter)

	// Update plan step if exists
	if plan != nil && len(plan.Steps) > 0 {
		c.planner.UpdateStepStatus(plan, 0, "in_progress")
	}

	// Stream response with callbacks
	streamReader, err := c.chain.Stream(ctx, map[string]any{
		"system_prompt": systemPrompt,
		"history":       session.History,
		"input":         input,
	}, compose.WithCallbacks(debugHandler))

	if err != nil {
		return nil, err
	}

	// Wrap stream to complete plan on finish
	if plan != nil {
		return c.wrapStreamWithPlanCompletion(streamReader, plan), nil
	}

	return streamReader, nil
}

// rewriteQueries generates multiple query variations using LLM
func (c *ChatService) rewriteQueries(ctx context.Context, query string) ([]string, error) {
	prompt := fmt.Sprintf(queryRewritePrompt, query)
	messages := []*schema.Message{
		schema.UserMessage(prompt),
	}

	response, err := c.chatModel.Generate(ctx, messages)
	if err != nil {
		return nil, err
	}

	// Parse response - split by newlines
	lines := strings.Split(strings.TrimSpace(response.Content), "\n")
	queries := make([]string, 0, len(lines)+1)
	queries = append(queries, query) // Always include original query

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip empty lines and numbered prefixes
		if line == "" {
			continue
		}
		// Remove common prefixes like "1.", "- ", etc.
		line = strings.TrimLeft(line, "0123456789.-) ")
		if line != "" && line != query {
			queries = append(queries, line)
		}
	}

	// Limit to 4 queries total (original + 3 rewrites)
	if len(queries) > 4 {
		queries = queries[:4]
	}

	return queries, nil
}

// retrieveWithDebug performs parallel retrieval with debug events
func (c *ChatService) retrieveWithDebug(ctx context.Context, queries []string, emitter DebugEmitter) []*schema.Document {
	var wg sync.WaitGroup
	var mu sync.Mutex
	allDocs := make([]*schema.Document, 0)
	seenIDs := make(map[string]bool)

	for i, query := range queries {
		wg.Add(1)
		go func(idx int, q string) {
			defer wg.Done()

			startTime := time.Now()
			docs, err := c.retriever.Retrieve(ctx, q, retriever.WithTopK(3))
			duration := time.Since(startTime).Milliseconds()

			if err != nil {
				emitter(apimodel.EventDebugRetrieval, apimodel.DebugRetrievalPayload{
					Query:         q,
					QueryIndex:    idx,
					DocumentCount: 0,
					Duration:      duration,
					Timestamp:     time.Now().UnixMilli(),
				})
				return
			}

			// Convert documents for debug output
			docInfos := make([]apimodel.DocumentInfo, 0, len(docs))
			for _, doc := range docs {
				score := 0.0
				if s, ok := doc.MetaData["score"].(float64); ok {
					score = s
				}
				docInfos = append(docInfos, apimodel.DocumentInfo{
					ID:      doc.ID,
					Content: truncateString(doc.Content, 150),
					Score:   score,
				})
			}

			emitter(apimodel.EventDebugRetrieval, apimodel.DebugRetrievalPayload{
				Query:         q,
				QueryIndex:    idx,
				DocumentCount: len(docs),
				Documents:     docInfos,
				Duration:      duration,
				Timestamp:     time.Now().UnixMilli(),
			})

			// Emit viz events for retrieval results
			c.emitVizForRetrieval(q, docs, duration, emitter)

			// Merge results with deduplication
			mu.Lock()
			for _, doc := range docs {
				if !seenIDs[doc.ID] {
					seenIDs[doc.ID] = true
					allDocs = append(allDocs, doc)
				}
			}
			mu.Unlock()
		}(i, query)
	}

	wg.Wait()
	return allDocs
}

// emitVizForRetrieval emits viz events for general retrieval results
func (c *ChatService) emitVizForRetrieval(query string, docs []*schema.Document, duration int64, emitter DebugEmitter) {
	if emitter == nil {
		return
	}

	var vizNodes []apimodel.VizNode

	// Emit viz:cypher for the retrieval query
	cypherViz := apimodel.VizCypher{
		QueryID:     uuid.New().String(),
		Cypher:      fmt.Sprintf("// Full-text search: %s", query),
		Params:      map[string]any{"query": query},
		ResultCount: len(docs),
		Duration:    duration,
		Source:      "chat.retrieveWithDebug",
		Timestamp:   time.Now().UnixMilli(),
	}
	log.Printf("[VIZ] Emitting viz:cypher (retrieveWithDebug): query=%s, results=%d", query, len(docs))
	emitter(apimodel.EventVizCypher, cypherViz)

	// Emit viz:node for each document
	for i, doc := range docs {
		nodeUUID := doc.ID
		nodeName := ""
		nodeLabels := []string{}

		if doc.MetaData != nil {
			if n, ok := doc.MetaData["name"].(string); ok {
				nodeName = n
			}
			if l, ok := doc.MetaData["labels"].([]string); ok {
				nodeLabels = l
			}
			if u, ok := doc.MetaData["uuid"].(string); ok {
				nodeUUID = u
			}
		}

		if nodeName == "" {
			nodeName = fmt.Sprintf("Result_%d", i+1)
		}

		vizNode := apimodel.VizNode{
			UUID:   nodeUUID,
			Name:   nodeName,
			Labels: nodeLabels,
			Depth:  0,
			Source: "general_retrieval",
		}
		log.Printf("[VIZ] Emitting viz:node (retrieveWithDebug %d): %s", i+1, nodeName)
		emitter(apimodel.EventVizNode, vizNode)
		vizNodes = append(vizNodes, vizNode)
	}

	// Emit viz:complete
	vizComplete := apimodel.VizCompletePayload{
		Nodes:         vizNodes,
		Edges:         []apimodel.VizEdge{},
		CypherQueries: []apimodel.VizCypher{cypherViz},
		AnswerNodeIDs: func() []string {
			ids := make([]string, 0, len(docs))
			for _, doc := range docs {
				ids = append(ids, doc.ID)
			}
			return ids
		}(),
		Timestamp: time.Now().UnixMilli(),
	}
	log.Printf("[VIZ] Emitting viz:complete (retrieveWithDebug): %d nodes", len(vizNodes))
	emitter(apimodel.EventVizComplete, vizComplete)
}

// buildRAGSystemPrompt builds the system prompt with plan and retrieved context
func (c *ChatService) buildRAGSystemPrompt(basePrompt string, plan *Plan, docs []*schema.Document) string {
	if basePrompt == "" {
		basePrompt = defaultSystemPrompt
	}

	// Build plan context
	planContext := ""
	if plan != nil {
		planContext = c.planner.GetPlanContext(plan)
	}

	// Build document context
	var docContext strings.Builder
	if len(docs) > 0 {
		for i, doc := range docs {
			if i >= 5 { // Limit to 5 documents
				break
			}
			title := ""
			if t, ok := doc.MetaData["title"].(string); ok {
				title = t
			}
			docContext.WriteString(fmt.Sprintf("\n[Document %d: %s]\n%s\n", i+1, title, doc.Content))
		}
	}

	if planContext != "" || docContext.Len() > 0 {
		return fmt.Sprintf(ragSystemPrompt, planContext, docContext.String())
	}

	return basePrompt
}

// wrapStreamWithPlanCompletion wraps the stream to mark plan as completed when done
func (c *ChatService) wrapStreamWithPlanCompletion(
	reader *schema.StreamReader[*schema.Message],
	plan *Plan,
) *schema.StreamReader[*schema.Message] {
	newReader, writer := schema.Pipe[*schema.Message](1)

	go func() {
		defer writer.Close()
		defer c.planner.CompletePlan(plan)

		for {
			msg, err := reader.Recv()
			if err != nil {
				return
			}
			writer.Send(msg, nil)
		}
	}()

	return newReader
}

// Chat handles non-streaming chat (kept for compatibility)
func (c *ChatService) Chat(ctx context.Context, sessionID, input string) (*schema.Message, error) {
	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	systemPrompt := session.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	return c.chain.Invoke(ctx, map[string]any{
		"system_prompt": systemPrompt,
		"history":       session.History,
		"input":         input,
	})
}

// CheckClarificationNeeded analyzes a query and returns clarification request if needed
func (c *ChatService) CheckClarificationNeeded(ctx context.Context, query, requestID string) (*ClarificationRequest, error) {
	graphRAG, ok := c.retriever.(*GraphRAGRetriever)
	if !ok || graphRAG.GetClarificationService() == nil {
		return nil, nil // No clarification service available
	}

	clarificationSvc := graphRAG.GetClarificationService()

	// First, try to find a source node
	sourceUUID := ""
	docs, err := c.retriever.Retrieve(ctx, query, retriever.WithTopK(1))
	if err == nil && len(docs) > 0 {
		if uuid, ok := docs[0].MetaData["source_uuid"].(string); ok {
			sourceUUID = uuid
		} else {
			sourceUUID = docs[0].ID
		}
	}

	_, clarificationReq, err := clarificationSvc.AnalyzeAndClarify(ctx, query, sourceUUID, requestID)
	if err != nil {
		return nil, err
	}

	return clarificationReq, nil
}

// ResolveClarification resolves a clarification response and retrieves documents
func (c *ChatService) ResolveClarification(ctx context.Context, req *ClarificationRequest, resp *ClarificationResponse) ([]*schema.Document, error) {
	graphRAG, ok := c.retriever.(*GraphRAGRetriever)
	if !ok || graphRAG.GetClarificationService() == nil {
		return nil, fmt.Errorf("clarification service not available")
	}

	clarificationSvc := graphRAG.GetClarificationService()

	// Resolve clarification to get target labels
	result, err := clarificationSvc.ResolveClarification(ctx, req, resp)
	if err != nil {
		return nil, err
	}

	// Retrieve with resolved target labels
	if len(result.TargetLabels) > 0 {
		return graphRAG.RetrieveWithTargetLabels(ctx, result.RefinedQuery, result.TargetLabels, 5)
	}

	// Fallback to regular retrieval with refined query
	return c.retriever.Retrieve(ctx, result.RefinedQuery, retriever.WithTopK(5))
}

// GetGraphRAGRetriever returns the GraphRAG retriever if available
func (c *ChatService) GetGraphRAGRetriever() *GraphRAGRetriever {
	if graphRAG, ok := c.retriever.(*GraphRAGRetriever); ok {
		return graphRAG
	}
	return nil
}

// ProcessClarificationResponse processes the user's clarification response and returns a stream
func (c *ChatService) ProcessClarificationResponse(
	ctx context.Context,
	sessionID string,
	resp *ClarificationResponse,
	pendingReq *ClarificationRequest,
) (*schema.StreamReader[*schema.Message], error) {
	return c.processClarificationResponseInternal(ctx, sessionID, resp, pendingReq, nil)
}

// ProcessClarificationResponseWithDebug processes clarification response with debug events
func (c *ChatService) ProcessClarificationResponseWithDebug(
	ctx context.Context,
	sessionID string,
	resp *ClarificationResponse,
	pendingReq *ClarificationRequest,
	emitter DebugEmitter,
) (*schema.StreamReader[*schema.Message], error) {
	return c.processClarificationResponseInternal(ctx, sessionID, resp, pendingReq, emitter)
}

// processClarificationResponseInternal is the internal implementation
func (c *ChatService) processClarificationResponseInternal(
	ctx context.Context,
	sessionID string,
	resp *ClarificationResponse,
	pendingReq *ClarificationRequest,
	emitter DebugEmitter,
) (*schema.StreamReader[*schema.Message], error) {
	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Get pending clarification from session
	pending, err := c.sessionStore.GetPendingClarification(sessionID)
	if err != nil || pending == nil {
		return nil, fmt.Errorf("no pending clarification for session")
	}

	// Get the orchestrator
	graphRAG := c.GetGraphRAGRetriever()
	if graphRAG == nil {
		return nil, fmt.Errorf("GraphRAG retriever not available")
	}

	clarificationSvc := graphRAG.GetClarificationService()
	if clarificationSvc == nil {
		return nil, fmt.Errorf("clarification service not available")
	}

	// Create orchestrator
	orchestrator := NewClarificationOrchestrator(&ClarificationOrchestratorConfig{
		ClarificationService: clarificationSvc,
		GraphRAGRetriever:    graphRAG,
		Neo4jClient:          c.neo4jClient,
		DebugEmitter:         emitter,
	})

	// Process clarification response
	if emitter != nil {
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "clarification_resolve",
			StepName:  "ClarificationResolver",
			Component: "clarification",
			Status:    apimodel.StatusStarted,
			Input: map[string]interface{}{
				"selected_id": resp.SelectedID,
				"free_text":   resp.FreeText,
			},
			Timestamp: time.Now().UnixMilli(),
		})
	}

	decision, err := orchestrator.ProcessClarificationResponse(ctx, pending, resp.SelectedID, resp.FreeText, pendingReq)
	if err != nil {
		return nil, fmt.Errorf("failed to process clarification response: %w", err)
	}

	if emitter != nil {
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "clarification_resolve",
			StepName:  "ClarificationResolver",
			Component: "clarification",
			Status:    apimodel.StatusCompleted,
			Output: map[string]interface{}{
				"needs_clarification": decision.NeedsClarification,
				"documents_count":     len(decision.Documents),
			},
			Timestamp: time.Now().UnixMilli(),
		})

		// Emit viz events for retrieved documents
		for _, doc := range decision.Documents {
			vizNode := apimodel.VizNode{
				UUID:   doc.ID,
				Name:   doc.ID,
				Labels: []string{"Document"},
				Score:  0.0,
				Source: "clarification_response",
			}
			// Extract metadata if available
			if uuid, ok := doc.MetaData["source_uuid"].(string); ok {
				vizNode.UUID = uuid
			}
			if name, ok := doc.MetaData["name"].(string); ok {
				vizNode.Name = name
			}
			if labels, ok := doc.MetaData["labels"].([]string); ok {
				vizNode.Labels = labels
			}
			if score, ok := doc.MetaData["score"].(float64); ok {
				vizNode.Score = score
			}
			if depth, ok := doc.MetaData["depth"].(int); ok {
				vizNode.Depth = depth
			}
			if parentUUID, ok := doc.MetaData["parent_uuid"].(string); ok {
				vizNode.ParentUUID = parentUUID
			}
			log.Printf("[VIZ] Emitting viz:node from clarification: %s", vizNode.Name)
			emitter(apimodel.EventVizNode, vizNode)
		}

		// Emit viz:complete
		vizPayload := &apimodel.VizCompletePayload{
			Nodes:         make([]apimodel.VizNode, 0),
			Edges:         make([]apimodel.VizEdge, 0),
			CypherQueries: make([]apimodel.VizCypher, 0),
			AnswerNodeIDs: make([]string, 0, len(decision.Documents)),
			Timestamp:     time.Now().UnixMilli(),
		}
		for _, doc := range decision.Documents {
			uuid := doc.ID
			if u, ok := doc.MetaData["source_uuid"].(string); ok {
				uuid = u
			}
			vizPayload.AnswerNodeIDs = append(vizPayload.AnswerNodeIDs, uuid)
		}
		log.Printf("[VIZ] Emitting viz:complete from clarification with %d answer nodes", len(vizPayload.AnswerNodeIDs))
		emitter(apimodel.EventVizComplete, vizPayload)
	}

	// If still needs clarification (Phase 2), update pending and return clarification request
	if decision.NeedsClarification && decision.ClarificationRequest != nil {
		// Update pending clarification with new phase info
		newPending := &apimodel.PendingClarification{
			RequestID:      decision.ClarificationRequest.RequestID,
			OriginalQuery:  pending.OriginalQuery,
			Phase:          decision.Phase,
			SourceNodeUUID: decision.SourceNodeUUID,
			SourceNodeName: decision.SourceNodeName,
			CreatedAt:      time.Now(),
		}
		if err := c.sessionStore.SetPendingClarification(sessionID, newPending); err != nil {
			return nil, fmt.Errorf("failed to update pending clarification: %w", err)
		}

		// Emit clarification event for frontend
		if emitter != nil {
			emitter(apimodel.EventClarificationReq, decision.ClarificationRequest)
		}

		// Return a special error that the handler can recognize as needing clarification
		return nil, &clarificationNeededError{
			Request: decision.ClarificationRequest,
			Phase:   decision.Phase,
		}
	}

	// Build context from retrieved documents
	systemPrompt := c.buildRAGSystemPrompt(session.SystemPrompt, nil, decision.Documents)

	// Stream response
	return c.chain.Stream(ctx, map[string]any{
		"system_prompt": systemPrompt,
		"history":       session.History,
		"input":         pending.OriginalQuery,
	})
}

// GetClarificationOrchestrator creates and returns a clarification orchestrator
func (c *ChatService) GetClarificationOrchestrator(emitter DebugEmitter) *ClarificationOrchestrator {
	graphRAG := c.GetGraphRAGRetriever()
	if graphRAG == nil {
		return nil
	}

	clarificationSvc := graphRAG.GetClarificationService()
	if clarificationSvc == nil {
		return nil
	}

	return NewClarificationOrchestrator(&ClarificationOrchestratorConfig{
		ClarificationService: clarificationSvc,
		GraphRAGRetriever:    graphRAG,
		Neo4jClient:          c.neo4jClient,
		DebugEmitter:         emitter,
	})
}

// convertComparisonClarification converts a ComparisonClarificationRequest to a standard ClarificationRequest
// for use with the existing frontend clarification handling.
func (c *ChatService) convertComparisonClarification(req *ComparisonClarificationRequest) *ClarificationRequest {
	if req == nil {
		return nil
	}

	// Convert comparison options to standard ClarificationOpt
	options := make([]ClarificationOpt, 0, len(req.Options))
	for _, opt := range req.Options {
		options = append(options, ClarificationOpt{
			ID:          opt.UUID,
			Label:       opt.Name,
			Description: opt.Details,
			TargetLabel: "Vehicle", // Comparison entities are typically vehicles
		})
	}

	return &ClarificationRequest{
		RequestID:     fmt.Sprintf("comparison_%s_%s", req.Phase, req.Entity),
		OriginalQuery: "", // Will be set from pending state
		Reason:        fmt.Sprintf("%s: %s", req.Reason, req.Message),
		Options:       options,
		AllowFreeText: false, // Comparison clarification requires selection
	}
}

// streamComparisonResponse streams a response for a completed comparison query.
// It uses the comparison analysis data to build context and generate a natural language response.
func (c *ChatService) streamComparisonResponse(
	ctx context.Context,
	session *apimodel.Session,
	input string,
	result *ComparisonClarificationResult,
) (*schema.StreamReader[*schema.Message], error) {
	noopEmitter := func(event string, data interface{}) {}
	return c.streamComparisonResponseWithViz(ctx, session, input, result, noopEmitter)
}

// streamComparisonResponseWithViz streams a response for a completed comparison query with viz events.
func (c *ChatService) streamComparisonResponseWithViz(
	ctx context.Context,
	session *apimodel.Session,
	input string,
	result *ComparisonClarificationResult,
	emitter DebugEmitter,
) (*schema.StreamReader[*schema.Message], error) {
	// Execute comparison analysis using the resolved entities
	if c.comparisonService == nil {
		return nil, fmt.Errorf("comparison service not available")
	}

	// Execute the full comparison with resolved entities
	comparisonState, err := c.comparisonService.ExecuteWithResolvedEntities(
		ctx,
		result.ResolvedEntities,
		result.ComparisonQuery,
	)
	if err != nil {
		return nil, fmt.Errorf("comparison execution failed: %w", err)
	}

	// Emit viz events for comparison entities
	if emitter != nil {
		c.emitComparisonVizEvents(comparisonState, result.ResolvedEntities, emitter)
	}

	// Build comparison context from the analysis data
	comparisonContext := c.comparisonService.BuildComparisonContext(comparisonState)

	// Build system prompt with comparison context
	systemPrompt := c.buildComparisonSystemPrompt(session.SystemPrompt, comparisonContext)

	// Stream response
	return c.chain.Stream(ctx, map[string]any{
		"system_prompt": systemPrompt,
		"history":       session.History,
		"input":         input,
	})
}

// emitComparisonVizEvents emits viz events for comparison query results
func (c *ChatService) emitComparisonVizEvents(state *ComparisonState, entities []ResolvedEntity, emitter DebugEmitter) {
	if emitter == nil {
		return
	}

	// Emit viz:node for each resolved entity
	for _, entity := range entities {
		vizNode := apimodel.VizNode{
			UUID:   entity.ResolvedUUID,
			Name:   entity.ResolvedName,
			Labels: []string{"Vehicle", entity.Role}, // Role: subject, reference
			Score:  0,
			Source: "comparison_query",
		}
		log.Printf("[VIZ] Emitting viz:node (comparison entity): %s (%s)", entity.ResolvedName, entity.Role)
		emitter(apimodel.EventVizNode, vizNode)
	}

	// Emit viz:edge for comparison relationship between entities
	if len(entities) >= 2 {
		vizEdge := apimodel.VizEdge{
			ID:           fmt.Sprintf("comparison_%s_%s", entities[0].ResolvedUUID[:8], entities[1].ResolvedUUID[:8]),
			FromUUID:     entities[0].ResolvedUUID,
			ToUUID:       entities[1].ResolvedUUID,
			Relationship: "COMPARED_WITH",
			Direction:    "outgoing",
		}
		log.Printf("[VIZ] Emitting viz:edge (comparison): %s <-> %s", entities[0].ResolvedName, entities[1].ResolvedName)
		emitter(apimodel.EventVizEdge, vizEdge)
	}

	// Emit viz:complete
	answerNodeIDs := make([]string, 0, len(entities))
	for _, entity := range entities {
		answerNodeIDs = append(answerNodeIDs, entity.ResolvedUUID)
	}

	vizPayload := &apimodel.VizCompletePayload{
		Nodes:         make([]apimodel.VizNode, 0),
		Edges:         make([]apimodel.VizEdge, 0),
		CypherQueries: make([]apimodel.VizCypher, 0),
		AnswerNodeIDs: answerNodeIDs,
		Timestamp:     time.Now().UnixMilli(),
	}
	log.Printf("[VIZ] Emitting viz:complete from comparison with %d answer nodes", len(answerNodeIDs))
	emitter(apimodel.EventVizComplete, vizPayload)
}

// buildComparisonSystemPrompt builds a system prompt specifically for comparison queries.
func (c *ChatService) buildComparisonSystemPrompt(basePrompt, comparisonContext string) string {
	if basePrompt == "" {
		basePrompt = defaultSystemPrompt
	}

	comparisonSystemPrompt := `You are a helpful assistant specialized in comparative analysis.
You have been provided with detailed comparison data between multiple entities.

IMPORTANT FORMATTING GUIDELINES for comparison responses:
1. Start with a brief summary of what is being compared
2. Use markdown tables to show side-by-side comparisons
3. Highlight significant differences with clear indicators:
   - ✓ for advantages (subject outperforms reference)
   - ✗ for disadvantages (subject underperforms reference)
   - ★ for exceptional scores
4. For gap analysis:
   - List items ordered by priority (high → medium → low)
   - Include specific improvement recommendations
   - Show numerical gaps clearly (e.g., "4점 차이")
5. End with a concise summary of key findings

Example format for gap analysis:
| 항목 | 기준 | 대상 | 차이 | 우선순위 |
|------|------|------|------|----------|
| 가속 | 42   | 38   | -4   | 높음     |
| 제동 | 43   | 45   | +2   | -        |

**보완 필요 항목:**
1. 🔴 가속 성능 (우선순위: 높음)
   - 현재: 38점 / 기준: 42점
   - 개선 목표: 4점 향상 필요

%s

Comparison Analysis Data:
%s`

	return fmt.Sprintf(comparisonSystemPrompt, basePrompt, comparisonContext)
}

// ProcessComparisonClarificationResponse handles user's response to a comparison clarification request.
// It continues the entity resolution process and returns either another clarification request or the final response.
func (c *ChatService) ProcessComparisonClarificationResponse(
	ctx context.Context,
	sessionID string,
	selectedIndex int,
	selectedUUID string,
	selectedName string,
	emitter DebugEmitter,
) (*schema.StreamReader[*schema.Message], error) {
	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Get pending comparison clarification
	pending := c.sessionStore.GetPendingComparisonClarification(sessionID)
	if pending == nil {
		return nil, fmt.Errorf("no pending comparison clarification for session")
	}

	if c.comparisonService == nil {
		return nil, fmt.Errorf("comparison service not available")
	}

	// Get comparison clarification orchestrator
	orchestrator := c.comparisonService.GetClarificationOrchestrator()
	if orchestrator == nil {
		return nil, fmt.Errorf("comparison clarification orchestrator not available")
	}

	if emitter != nil {
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "comparison_clarification_response",
			StepName:  "ComparisonClarificationResponse",
			Component: "comparison",
			Status:    apimodel.StatusStarted,
			Input: map[string]interface{}{
				"selected_index": selectedIndex,
				"selected_uuid":  selectedUUID,
				"selected_name":  selectedName,
			},
			Timestamp: time.Now().UnixMilli(),
		})
	}

	// Process the clarification response
	result, err := orchestrator.ProcessClarificationResponse(ctx, pending, selectedIndex, selectedUUID, selectedName)
	if err != nil {
		return nil, fmt.Errorf("failed to process comparison clarification: %w", err)
	}

	if emitter != nil {
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "comparison_clarification_response",
			StepName:  "ComparisonClarificationResponse",
			Component: "comparison",
			Status:    apimodel.StatusCompleted,
			Output: map[string]interface{}{
				"needs_clarification": result.NeedsClarification,
				"resolved_count":      len(result.ResolvedEntities),
			},
			Timestamp: time.Now().UnixMilli(),
		})
	}

	// If more clarification is needed for other entities
	if result.NeedsClarification {
		// Update pending state
		c.sessionStore.SetPendingComparisonClarification(sessionID, result.PendingState)

		// Emit clarification request
		clarificationReq := c.convertComparisonClarification(result.ClarificationRequest)
		if emitter != nil {
			emitter(apimodel.EventClarificationReq, clarificationReq)
		}

		return nil, fmt.Errorf("comparison_clarification_required")
	}

	// All entities resolved - clear pending and execute comparison
	c.sessionStore.ClearPendingComparisonClarification(sessionID)

	// Stream the comparison response with viz events
	return c.streamComparisonResponseWithViz(ctx, session, pending.OriginalQuery, result, emitter)
}

// ChatWithAgent handles streaming chat using the ReAct agent for iterative search
func (c *ChatService) ChatWithAgent(
	ctx context.Context,
	sessionID, input string,
	emitter DebugEmitter,
) (*schema.StreamReader[*schema.Message], error) {
	if !c.useAgentMode || c.graphAgent == nil {
		// Fallback to regular chat if agent mode is not available
		return c.ChatStreamWithDebug(ctx, sessionID, input, emitter)
	}

	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// ============================================================
	// [NEW] Step 0: Set emitter for all sub-components
	// ============================================================
	if graphRAG := c.GetGraphRAGRetriever(); graphRAG != nil {
		graphRAG.SetDebugEmitter(emitter)
	}

	// ============================================================
	// [NEW] Step 0: Comparison Query Detection (same as Chain mode)
	// ============================================================
	if c.comparisonService != nil {
		c.comparisonService.SetDebugEmitter(emitter)

		comparisonPending := c.sessionStore.GetPendingComparisonClarification(sessionID)

		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "agent_comparison_check",
			StepName:  "ComparisonQueryDetection",
			Component: "comparison",
			Status:    apimodel.StatusStarted,
			Input:     map[string]string{"query": truncateString(input, 100)},
			Timestamp: time.Now().UnixMilli(),
		})

		result, err := c.comparisonService.ProcessComparisonQuery(ctx, input, comparisonPending)
		if err == nil && result != nil && result.IsComparison {
			emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
				StepID:    "agent_comparison_check",
				StepName:  "ComparisonQueryDetection",
				Component: "comparison",
				Status:    apimodel.StatusCompleted,
				Output: map[string]interface{}{
					"is_comparison":       result.IsComparison,
					"needs_clarification": result.NeedsClarification,
					"entity_count":        len(result.ResolvedEntities),
				},
				Timestamp: time.Now().UnixMilli(),
			})

			if result.NeedsClarification {
				c.sessionStore.SetPendingComparisonClarification(sessionID, result.PendingState)
				clarificationReq := c.convertComparisonClarification(result.ClarificationRequest)
				log.Printf("[DEBUG] Agent: Emitting comparison clarification for entity=%s", result.ClarificationRequest.Entity)
				emitter(apimodel.EventClarificationReq, clarificationReq)
				return nil, fmt.Errorf("comparison_clarification_required")
			}

			// Comparison query resolved - use Chain mode for response
			c.sessionStore.ClearPendingComparisonClarification(sessionID)
			return c.streamComparisonResponse(ctx, session, input, result)
		}

		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "agent_comparison_check",
			StepName:  "ComparisonQueryDetection",
			Component: "comparison",
			Status:    apimodel.StatusCompleted,
			Output:    map[string]interface{}{"is_comparison": false},
			Timestamp: time.Now().UnixMilli(),
		})
	}

	// ============================================================
	// [NEW] Step 1: ClarificationOrchestrator Integration
	// ============================================================
	pending, _ := c.sessionStore.GetPendingClarification(sessionID)
	orchestrator := c.GetClarificationOrchestrator(emitter)

	var preRetrievedDocs []*schema.Document
	var agentContext *agent.AgentContext

	if orchestrator != nil && c.GetGraphRAGRetriever() != nil {
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "agent_clarification_check",
			StepName:  "ClarificationOrchestrator",
			Component: "clarification",
			Status:    apimodel.StatusStarted,
			Input:     map[string]string{"query": truncateString(input, 100)},
			Timestamp: time.Now().UnixMilli(),
		})

		decision, err := orchestrator.ProcessQuery(ctx, input, pending)
		if err == nil {
			emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
				StepID:    "agent_clarification_check",
				StepName:  "ClarificationOrchestrator",
				Component: "clarification",
				Status:    apimodel.StatusCompleted,
				Output: map[string]interface{}{
					"needs_clarification": decision.NeedsClarification,
					"phase":               string(decision.Phase),
					"source_node":         decision.SourceNodeName,
					"documents_count":     len(decision.Documents),
				},
				Timestamp: time.Now().UnixMilli(),
			})

			if decision.NeedsClarification {
				// Store pending clarification state
				c.sessionStore.SetPendingClarification(sessionID, &apimodel.PendingClarification{
					Phase:          decision.Phase,
					OriginalQuery:  input,
					SourceNodeUUID: decision.SourceNodeUUID,
					SourceNodeName: decision.SourceNodeName,
					CreatedAt:      time.Now(),
				})

				log.Printf("[DEBUG] Agent: Emitting clarification request for phase=%s", decision.Phase)
				emitter(apimodel.EventClarificationReq, decision.ClarificationRequest)
				return nil, fmt.Errorf("clarification_required")
			}

			// Extract verified context for Agent
			agentContext = &agent.AgentContext{
				SourceNodeUUID: decision.SourceNodeUUID,
				SourceNodeName: decision.SourceNodeName,
				IsHierarchical: decision.Analysis != nil && decision.Analysis.IsHierarchical,
			}
			if decision.Analysis != nil && len(decision.Analysis.Target.ExpectedLabels) > 0 {
				agentContext.TargetLabels = decision.Analysis.Target.ExpectedLabels
			}

			// Use pre-retrieved documents if available
			if len(decision.Documents) > 0 {
				preRetrievedDocs = decision.Documents
				log.Printf("[DEBUG] Agent: Using %d pre-retrieved documents from orchestrator", len(preRetrievedDocs))
			}
		} else {
			emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
				StepID:    "agent_clarification_check",
				StepName:  "ClarificationOrchestrator",
				Component: "clarification",
				Status:    apimodel.StatusCompleted,
				Output:    map[string]interface{}{"error": err.Error()},
				Timestamp: time.Now().UnixMilli(),
			})
		}
	}

	// Clear any pending clarification since we're proceeding
	c.sessionStore.ClearPendingClarification(sessionID)

	// ============================================================
	// [NEW] Step 2: Use Pre-Retrieved Documents (Skip Agent if available)
	// ============================================================
	if len(preRetrievedDocs) > 0 {
		log.Printf("[DEBUG] Agent: Skipping agent, using %d pre-retrieved docs from orchestrator", len(preRetrievedDocs))

		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "agent_skip",
			StepName:  "UsingPreRetrievedDocs",
			Component: "agent",
			Status:    apimodel.StatusCompleted,
			Output: map[string]interface{}{
				"reason":          "orchestrator_provided_docs",
				"documents_count": len(preRetrievedDocs),
			},
			Timestamp: time.Now().UnixMilli(),
		})

		// Build context and stream response (same as Chain mode)
		systemPrompt := c.buildRAGSystemPrompt(session.SystemPrompt, nil, preRetrievedDocs)
		return c.chain.Stream(ctx, map[string]any{
			"system_prompt": systemPrompt,
			"history":       session.History,
			"input":         input,
		})
	}

	// ============================================================
	// Step 3: Run Agent with Verified Context
	// ============================================================
	emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
		StepID:    "agent_start",
		StepName:  "GraphRAGAgent",
		Component: "agent",
		Status:    apimodel.StatusStarted,
		Input:     map[string]string{"query": truncateString(input, 100)},
		Timestamp: time.Now().UnixMilli(),
	})

	// Build initial messages with filtered history
	messages := make([]*schema.Message, 0, len(session.History)+1)
	for _, msg := range session.History {
		if msg.Role == schema.User || (msg.Role == schema.Assistant && len(msg.ToolCalls) == 0) {
			messages = append(messages, msg)
		}
	}
	messages = append(messages, schema.UserMessage(input))

	// Create agent debug emitter wrapper
	agentEmitter := func(event string, data interface{}) {
		emitter(event, data)
	}

	// Run agent with debug emitter and optional context
	var streamReader *schema.StreamReader[*schema.Message]
	if agentContext != nil && agentContext.SourceNodeUUID != "" {
		log.Printf("[DEBUG] Agent: Running with verified context: sourceNode=%s, targetLabels=%v, isHierarchical=%v",
			agentContext.SourceNodeName, agentContext.TargetLabels, agentContext.IsHierarchical)
		streamReader, err = c.graphAgent.StreamWithDebugAndContext(ctx, messages, agentEmitter, agentContext)
	} else {
		streamReader, err = c.graphAgent.StreamWithDebug(ctx, messages, agentEmitter)
	}

	if err != nil {
		emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
			StepID:    "agent_start",
			StepName:  "GraphRAGAgent",
			Component: "agent",
			Status:    apimodel.StatusError,
			Output:    map[string]string{"error": err.Error()},
			Timestamp: time.Now().UnixMilli(),
		})
		return nil, fmt.Errorf("agent execution failed: %w", err)
	}

	// Wrap stream to emit completion event
	return c.wrapAgentStream(streamReader, emitter), nil
}

// wrapAgentStream wraps the agent stream to emit completion events
func (c *ChatService) wrapAgentStream(
	reader *schema.StreamReader[*schema.Message],
	emitter DebugEmitter,
) *schema.StreamReader[*schema.Message] {
	newReader, writer := schema.Pipe[*schema.Message](1)

	go func() {
		defer writer.Close()
		defer func() {
			emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
				StepID:    "agent_complete",
				StepName:  "GraphRAGAgent",
				Component: "agent",
				Status:    apimodel.StatusCompleted,
				Timestamp: time.Now().UnixMilli(),
			})
		}()

		for {
			msg, err := reader.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					log.Printf("[ERROR] Chat agent stream recv failed: %v", err)
				}
				return
			}
			writer.Send(msg, nil)
		}
	}()

	return newReader
}

// IsAgentModeEnabled returns whether agent mode is enabled
func (c *ChatService) IsAgentModeEnabled() bool {
	return c.useAgentMode && c.graphAgent != nil
}

// GetAgentStats returns current agent statistics (for debugging)
func (c *ChatService) GetAgentStats() map[string]interface{} {
	if c.graphAgent == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}
	return map[string]interface{}{
		"enabled":              true,
		"max_iterations":       c.graphAgent.GetMaxIterations(),
		"confidence_threshold": c.graphAgent.GetConfidenceThreshold(),
	}
}
