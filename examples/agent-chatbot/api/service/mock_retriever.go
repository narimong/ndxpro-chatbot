package service

import (
	"context"
	"math/rand"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// MockRetriever is a demonstration retriever that returns sample documents
type MockRetriever struct {
	documents []*schema.Document
	delay     time.Duration
}

// NewMockRetriever creates a new mock retriever with sample Eino documentation
func NewMockRetriever() *MockRetriever {
	return &MockRetriever{
		documents: getSampleDocuments(),
		delay:     50 * time.Millisecond, // Simulate network latency
	}
}

// Retrieve returns documents that match the query
func (m *MockRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	// Simulate network delay
	time.Sleep(m.delay + time.Duration(rand.Intn(50))*time.Millisecond)

	// Simple keyword matching for demonstration
	queryLower := strings.ToLower(query)
	var results []*schema.Document

	for _, doc := range m.documents {
		contentLower := strings.ToLower(doc.Content)
		// Check if any word from query appears in document
		words := strings.Fields(queryLower)
		for _, word := range words {
			if len(word) > 2 && strings.Contains(contentLower, word) {
				// Clone document and add score
				result := &schema.Document{
					ID:       doc.ID,
					Content:  doc.Content,
					MetaData: doc.MetaData,
				}
				// Add random score for demonstration
				result = result.WithScore(0.7 + rand.Float64()*0.3)
				results = append(results, result)
				break
			}
		}
	}

	// Apply TopK option
	o := retriever.GetCommonOptions(&retriever.Options{
		TopK: ptrInt(5), // Default to 5
	}, opts...)

	if o.TopK != nil && len(results) > *o.TopK {
		results = results[:*o.TopK]
	}

	return results, nil
}

// GetType returns the retriever type
func (m *MockRetriever) GetType() string {
	return "MockRetriever"
}

func ptrInt(i int) *int {
	return &i
}

// getSampleDocuments returns sample Eino documentation for testing
func getSampleDocuments() []*schema.Document {
	return []*schema.Document{
		{
			ID: "doc_1",
			Content: `Eino (pronounced "I know") is the ultimate LLM application development framework for Go,
developed by CloudWeGo. It provides a production-ready toolkit emphasizing simplicity, scalability,
reliability, and effectiveness. Eino is inspired by LangChain and LlamaIndex but designed specifically
for Go developers.`,
			MetaData: map[string]any{"title": "Eino Overview", "category": "introduction"},
		},
		{
			ID: "doc_2",
			Content: `RAG (Retrieval Augmented Generation) is a technique that combines retrieval of relevant
documents with LLM generation. In Eino, you can build RAG applications using the Retriever interface
for document retrieval and ChatModel for generation. The compose package helps chain these components together.`,
			MetaData: map[string]any{"title": "RAG in Eino", "category": "concepts"},
		},
		{
			ID: "doc_3",
			Content: `The ChatModel interface in Eino defines two main methods: Generate() for synchronous
generation and Stream() for streaming responses. ToolCallingChatModel extends this with WithTools()
for function calling capabilities. OpenAI, Claude, and other providers are supported through eino-ext.`,
			MetaData: map[string]any{"title": "ChatModel Interface", "category": "components"},
		},
		{
			ID: "doc_4",
			Content: `The compose package provides Chain and Graph for orchestrating components. Chain is for
simple linear pipelines: chain.AppendChatTemplate().AppendChatModel().Compile(). Graph supports
complex DAG flows with branching, parallel execution, and cycles for iterative patterns.`,
			MetaData: map[string]any{"title": "Compose Package", "category": "orchestration"},
		},
		{
			ID: "doc_5",
			Content: `To create a chatbot with Eino, you need: 1) A ChatModel (e.g., OpenAI), 2) A ChatTemplate
for formatting messages, 3) A Chain to connect them. Use chain.Stream() for real-time responses.
History management is done by passing previous messages through the template placeholder.`,
			MetaData: map[string]any{"title": "Building a Chatbot", "category": "tutorials"},
		},
		{
			ID: "doc_6",
			Content: `The Retriever interface has a single method: Retrieve(ctx, query, opts) that returns
documents. Options include TopK for limiting results, ScoreThreshold for filtering, and custom
embedding options. MultiQuery retriever rewrites queries for better coverage.`,
			MetaData: map[string]any{"title": "Retriever Interface", "category": "components"},
		},
		{
			ID: "doc_7",
			Content: `Callbacks in Eino provide hooks for logging, tracing, and metrics. Use
callbacks.NewHandlerBuilder() to create handlers with OnStart, OnEnd, and OnError functions.
Attach callbacks using compose.WithCallbacks() when invoking chains or graphs.`,
			MetaData: map[string]any{"title": "Callbacks System", "category": "advanced"},
		},
		{
			ID: "doc_8",
			Content: `The Agent in Eino uses the ReAct pattern: the model generates ToolCalls, ToolsNode
executes them, and results are fed back to the model. Configure agents with ToolCallingModel and
ToolsConfig. MaxStep limits iterations to prevent infinite loops.`,
			MetaData: map[string]any{"title": "ReAct Agent", "category": "agents"},
		},
		{
			ID: "doc_9",
			Content: `For vector storage, Eino supports Milvus, Pinecone, Qdrant, and other databases through
eino-ext. The Indexer interface stores documents with Store(), while Retriever fetches them.
Embeddings are generated using the Embedder interface (e.g., OpenAI text-embedding-3-small).`,
			MetaData: map[string]any{"title": "Vector Stores", "category": "storage"},
		},
		{
			ID: "doc_10",
			Content: `Plan-Execute-Replan is an ADK pattern for complex tasks. The planner breaks down
objectives into steps, the executor runs each step, and the replanner adjusts based on results.
This pattern is useful for multi-step reasoning and task decomposition.`,
			MetaData: map[string]any{"title": "Plan-Execute Pattern", "category": "patterns"},
		},
		{
			ID: "doc_11",
			Content: `MultiQuery Retriever improves search by rewriting a single query into multiple
perspectives. Configure with RewriteLLM for LLM-based rewriting or RewriteHandler for custom logic.
Results are merged using FusionFunc (default: deduplicate by document ID).`,
			MetaData: map[string]any{"title": "MultiQuery Retriever", "category": "patterns"},
		},
		{
			ID: "doc_12",
			Content: `To implement tools in Eino: 1) Implement BaseTool with Info() returning ToolInfo,
2) Implement InvokableTool or StreamableTool with Run methods, 3) Add tools to ToolsNodeConfig.
Use utils.InferTool() for automatic schema inference from Go structs.`,
			MetaData: map[string]any{"title": "Implementing Tools", "category": "tools"},
		},
	}
}
