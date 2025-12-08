# Eino Framework - Developer Reference


  데이터가 변경되더라도 스스로 정보를 탐색하고 정확하고 풍부한 데이터를 기반으로 답변할 수 있도록 고민해. 모든 개발과정에 포함되어야 하는 필수적인 내용이야. 

## Project Overview

**Eino** (pronounced "I know") is the ultimate LLM application development framework for Go, developed by CloudWeGo. Inspired by LangChain and LlamaIndex, it provides a production-ready toolkit emphasizing:

- **Simplicity**: Clean APIs aligned with Go conventions
- **Scalability**: Handles complex orchestrations and concurrent operations
- **Reliability**: Type-safe, stream-aware architecture
- **Effectiveness**: Best practices bundled as flows and examples

---

## Directory Structure

```
eino/
├── components/           # Core component interfaces
│   ├── model/           # ChatModel interface (Generate, Stream)
│   ├── embedding/       # Embedder interface (EmbedStrings)
│   ├── retriever/       # Retriever interface (Retrieve)
│   ├── indexer/         # Indexer interface (Store)
│   ├── document/        # Loader, Transformer, Parser
│   ├── prompt/          # ChatTemplate
│   └── tool/            # Tool interface for agents
├── compose/             # Orchestration (Chain, Graph, Workflow)
├── schema/              # Data types (Message, Document, ToolCall)
├── flow/                # Pre-built patterns
│   ├── retriever/       # MultiQuery, Router, Parent Retriever
│   ├── indexer/         # Parent Indexer
│   └── agent/           # ReAct Agent
├── callbacks/           # Aspect/Hook system for logging, tracing
├── adk/                 # Agent Development Kit
└── examples/            # Example implementations
    └── simple-chatbot/  # Basic CLI chatbot
```

---

## Core Interfaces

### ChatModel (components/model/interface.go)

```go
type BaseChatModel interface {
    Generate(ctx context.Context, input []*schema.Message, opts ...Option) (*schema.Message, error)
    Stream(ctx context.Context, input []*schema.Message, opts ...Option) (*schema.StreamReader[*schema.Message], error)
}

type ToolCallingChatModel interface {
    BaseChatModel
    WithTools(tools []*schema.ToolInfo) (ToolCallingChatModel, error)
}
```

### Embedder (components/embedding/interface.go)

```go
type Embedder interface {
    EmbedStrings(ctx context.Context, texts []string, opts ...Option) ([][]float64, error)
}
```

### Retriever (components/retriever/interface.go)

```go
type Retriever interface {
    Retrieve(ctx context.Context, query string, opts ...Option) ([]*schema.Document, error)
}
```

**Options**: `WithTopK(k)`, `WithScoreThreshold(t)`, `WithEmbedding(emb)`, `WithIndex(idx)`

### Indexer (components/indexer/interface.go)

```go
type Indexer interface {
    Store(ctx context.Context, docs []*schema.Document, opts ...Option) (ids []string, err error)
}
```

**Options**: `WithEmbedding(emb)`, `WithSubIndexes(indexes)`

### Document Loader & Transformer (components/document/interface.go)

```go
type Loader interface {
    Load(ctx context.Context, src Source, opts ...LoaderOption) ([]*schema.Document, error)
}

type Transformer interface {
    Transform(ctx context.Context, src []*schema.Document, opts ...TransformerOption) ([]*schema.Document, error)
}
```

---

## Key Data Types (schema/)

### Message (schema/message.go)

```go
type Message struct {
    Role       RoleType           // system, user, assistant, tool
    Content    string             // Text content
    ToolCalls  []ToolCall         // Tool calls (assistant only)
    ToolCallID string             // Tool response ID (tool only)
    Extra      map[string]any     // Custom metadata
}

// Helper constructors
schema.SystemMessage("You are helpful")
schema.UserMessage("Hello")
schema.AssistantMessage("Hi!", nil)
schema.ToolMessage("result", "call_id")
```

### Document (schema/document.go)

```go
type Document struct {
    ID       string
    Content  string
    MetaData map[string]any
}

// Helper methods
doc.WithScore(0.95)           // Set relevance score
doc.WithDenseVector(vec)      // Store embedding
doc.WithSubIndexes([]string{})// Set sub-indexes
```

---

## Orchestration APIs

### Chain (compose/chain.go)

Simple linear pipeline with builder pattern:

```go
chain, err := compose.NewChain[map[string]any, *schema.Message]().
    AppendChatTemplate(prompt).
    AppendChatModel(model).
    Compile(ctx)

result, err := chain.Invoke(ctx, map[string]any{"input": "hello"})
```

**Available Methods**:
- `AppendChatModel(model)`
- `AppendChatTemplate(template)`
- `AppendRetriever(retriever)`
- `AppendIndexer(indexer)`
- `AppendLambda(func)`
- `AppendBranch(branch)`

### Graph (compose/graph.go)

DAG/Pregel-based complex flows:

```go
graph := compose.NewGraph[string, *schema.Message]()

graph.AddRetrieverNode("retriever", retriever)
graph.AddLambdaNode("formatter", formatFunc)
graph.AddChatTemplateNode("prompt", template)
graph.AddChatModelNode("model", model)

graph.AddEdge(compose.START, "retriever")
graph.AddEdge("retriever", "formatter")
graph.AddEdge("formatter", "prompt")
graph.AddEdge("prompt", "model")
graph.AddEdge("model", compose.END)

compiled, err := graph.Compile(ctx)
result, err := compiled.Invoke(ctx, "user query")
```

**Features**:
- Branching with conditions
- Parallel execution
- State management
- Cycles for iterative patterns

### Workflow (compose/workflow.go)

Field-level struct mapping (alpha):

```go
wf := compose.NewWorkflow[InputType, OutputType]()
wf.AddChatModelNode("model", model).AddInput(compose.START)
wf.AddLambdaNode("lambda", fn).AddInput("model", compose.MapFields("Content", "Input"))
```

---

## RAG Patterns (flow/retriever/)

### MultiQuery Retriever

Rewrites queries for better coverage:

```go
import "github.com/cloudwego/eino/flow/retriever/multiquery"

mqr, err := multiquery.NewRetriever(ctx, &multiquery.Config{
    RewriteLLM:    chatModel,           // LLM for query rewriting
    OrigRetriever: vectorRetriever,     // Base retriever
    MaxQueriesNum: 5,                   // Limit generated queries
    FusionFunc:    customFusion,        // Optional: custom dedup logic
})
```

### Router Retriever

Routes to different retrievers based on query:

```go
import "github.com/cloudwego/eino/flow/retriever/router"

rr, err := router.NewRetriever(ctx, &router.Config{
    Retrievers: map[string]retriever.Retriever{
        "code":    codeRetriever,
        "docs":    docsRetriever,
    },
    Router: func(ctx context.Context, query string) ([]string, error) {
        if strings.Contains(query, "code") {
            return []string{"code"}, nil
        }
        return []string{"docs"}, nil
    },
})
```

### Parent Document Retriever

Returns full documents after chunk search:

```go
import "github.com/cloudwego/eino/flow/retriever/parent"

pr, err := parent.NewRetriever(ctx, &parent.Config{
    Retriever:   chunkRetriever,
    ParentIDKey: "parent_id",
    OrigDocGetter: func(ctx context.Context, ids []string) ([]*schema.Document, error) {
        return docStore.GetByIDs(ids)
    },
})
```

---

## External Implementations (eino-ext)

### Models
```go
import "github.com/cloudwego/eino-ext/components/model/openai"

model, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
    APIKey: os.Getenv("OPENAI_API_KEY"),
    Model:  "gpt-4o-mini",
})
```

### Embeddings
```go
import "github.com/cloudwego/eino-ext/components/embedding/openai"

embedder, err := openai.NewEmbedder(ctx, &openai.EmbedderConfig{
    APIKey: os.Getenv("OPENAI_API_KEY"),
    Model:  "text-embedding-3-small",
})
```

### Vector Stores

**Milvus**:
```go
import (
    milvusIndexer "github.com/cloudwego/eino-ext/components/indexer/milvus"
    milvusRetriever "github.com/cloudwego/eino-ext/components/retriever/milvus"
)
```

**VikingDB**:
```go
import (
    vikingIndexer "github.com/cloudwego/eino-ext/components/indexer/volc_vikingdb"
    vikingRetriever "github.com/cloudwego/eino-ext/components/retriever/volc_vikingdb"
)
```

---

## Callback System (callbacks/)

Inject logging, tracing, metrics:

```go
handler := callbacks.NewHandlerBuilder().
    OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
        log.Printf("Starting: %s", info.Name)
        return ctx
    }).
    OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
        log.Printf("Completed: %s", info.Name)
        return ctx
    }).
    Build()

chain.Invoke(ctx, input, compose.WithCallbacks(handler))
```

---

## Build & Run

```bash
# Install dependencies
go mod tidy

# Build
go build -o app .

# Run with API key
OPENAI_API_KEY=your-key ./app
```

---

## Example: Simple RAG Chatbot

```go
package main

import (
    "context"
    "github.com/cloudwego/eino/compose"
    "github.com/cloudwego/eino/components/prompt"
    "github.com/cloudwego/eino/schema"
)

func main() {
    ctx := context.Background()

    // 1. Initialize components (model, embedder, retriever)

    // 2. Create RAG prompt template
    ragPrompt := prompt.FromMessages(
        schema.FString,
        schema.SystemMessage("Answer based on context:\n{context}"),
        schema.UserMessage("{query}"),
    )

    // 3. Build RAG chain
    chain, _ := compose.NewChain[map[string]any, *schema.Message]().
        AppendRetriever(retriever).
        AppendLambda(compose.InvokableLambda(formatContext)).
        AppendChatTemplate(ragPrompt).
        AppendChatModel(model).
        Compile(ctx)

    // 4. Query
    result, _ := chain.Invoke(ctx, map[string]any{
        "query": "What is Eino?",
    })

    fmt.Println(result.Content)
}
```

---

## Key Files Reference

| Purpose | File |
|---------|------|
| ChatModel interface | `components/model/interface.go` |
| Embedder interface | `components/embedding/interface.go` |
| Retriever interface | `components/retriever/interface.go` |
| Indexer interface | `components/indexer/interface.go` |
| Document types | `schema/document.go` |
| Message types | `schema/message.go` |
| Chain orchestration | `compose/chain.go` |
| Graph orchestration | `compose/graph.go` |
| MultiQuery pattern | `flow/retriever/multiquery/multi_query.go` |
| Router pattern | `flow/retriever/router/router.go` |
| Parent retriever | `flow/retriever/parent/parent.go` |
| ReAct agent | `flow/agent/react/react.go` |

---

## Documentation Links

- User Manual: https://www.cloudwego.io/zh/docs/eino/
- Quick Start: https://www.cloudwego.io/zh/docs/eino/quick_start/
- Examples: https://github.com/cloudwego/eino-examples
- Extensions: https://github.com/cloudwego/eino-ext
