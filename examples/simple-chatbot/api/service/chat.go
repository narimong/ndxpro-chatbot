package service

import (
	"context"
	"fmt"
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
)

const (
	defaultSystemPrompt = "You are a helpful assistant. Answer questions concisely and clearly."

	ragSystemPrompt = `You are a helpful assistant with access to relevant documents.
Use the following context to answer the user's question. If the context doesn't contain
relevant information, say so and answer based on your general knowledge.

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

	// Create mock retriever for demonstration
	mockRetriever := NewMockRetriever()

	// Create planner service
	planner := NewPlannerService(chatModel)

	return &ChatService{
		chain:        chain,
		chatModel:    chatModel,
		retriever:    mockRetriever,
		planner:      planner,
		sessionStore: sessionStore,
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
