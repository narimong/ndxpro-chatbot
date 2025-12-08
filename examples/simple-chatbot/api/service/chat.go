package service

import (
	"context"
	"fmt"
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

	"simple-chatbot/api/config"
	apimodel "simple-chatbot/api/model"
	"simple-chatbot/api/service/db"
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

// ChatService handles chat operations with optional debug mode
type ChatService struct {
	chain        compose.Runnable[map[string]any, *schema.Message]
	chatModel    model.ChatModel
	retriever    retriever.Retriever
	planner      *PlannerService
	sessionStore *SessionStore
	neo4jClient  *db.Neo4jClient
	debugEnabled bool
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

	return &ChatService{
		chain:        chain,
		chatModel:    chatModel,
		retriever:    activeRetriever,
		planner:      planner,
		sessionStore: sessionStore,
		neo4jClient:  neo4jClient,
		debugEnabled: true,
	}, nil
}

// ChatStream handles non-debug streaming chat
func (c *ChatService) ChatStream(ctx context.Context, sessionID, input string) (*schema.StreamReader[*schema.Message], error) {
	session, err := c.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	systemPrompt := session.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	return c.chain.Stream(ctx, map[string]any{
		"system_prompt": systemPrompt,
		"history":       session.History,
		"input":         input,
	})
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

	// Step 1: Create execution plan
	emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
		StepID:    "planning",
		StepName:  "Planner",
		Component: apimodel.ComponentPlanner,
		Status:    apimodel.StatusStarted,
		Input:     map[string]string{"query": truncateString(input, 100)},
		Timestamp: time.Now().UnixMilli(),
	})

	plan, err := c.planner.CreatePlan(ctx, input)
	if err != nil {
		// Continue without plan on error
		plan = nil
	}

	emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
		StepID:    "planning",
		StepName:  "Planner",
		Component: apimodel.ComponentPlanner,
		Status:    apimodel.StatusCompleted,
		Output:    map[string]interface{}{"plan_steps": len(plan.Steps)},
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

	queries, err := c.rewriteQueries(ctx, input)
	if err != nil {
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

	docs := c.retrieveWithDebug(ctx, queries, emitter)

	emitter(apimodel.EventDebugStep, apimodel.DebugStepPayload{
		StepID:    "retrieval",
		StepName:  "Retriever",
		Component: apimodel.ComponentRetriever,
		Status:    apimodel.StatusCompleted,
		Output:    map[string]interface{}{"total_documents": len(docs)},
		Timestamp: time.Now().UnixMilli(),
	})

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
	}

	// If still needs clarification (Phase 2), return error for now
	// In a more complete implementation, we would emit a new clarification request
	if decision.NeedsClarification {
		return nil, fmt.Errorf("additional clarification needed: %s", decision.ClarificationRequest.Reason)
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
