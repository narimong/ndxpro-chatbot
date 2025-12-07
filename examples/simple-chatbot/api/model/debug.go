package model

// Debug event types for Socket.IO
const (
	EventDebugStep      = "debug:step"      // Component execution start/end
	EventDebugPlan      = "debug:plan"      // Planning information
	EventDebugQuery     = "debug:query"     // Query decomposition/rewriting
	EventDebugRetrieval = "debug:retrieval" // Retrieval results
)

// DebugStepPayload represents a step execution event
type DebugStepPayload struct {
	StepID    string      `json:"step_id"`
	StepName  string      `json:"step_name"`
	Component string      `json:"component"` // model, retriever, prompt, planner, query_rewriter
	Status    string      `json:"status"`    // started, completed, error
	Input     interface{} `json:"input,omitempty"`
	Output    interface{} `json:"output,omitempty"`
	Duration  int64       `json:"duration_ms,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

// DebugPlanPayload represents a planning event
type DebugPlanPayload struct {
	PlanID      string   `json:"plan_id"`
	Objective   string   `json:"objective"`
	Steps       []string `json:"steps"`
	CurrentStep int      `json:"current_step"`
	Status      string   `json:"status"` // planning, executing, replanning, completed
	Timestamp   int64    `json:"timestamp"`
}

// DebugQueryPayload represents a query decomposition event
type DebugQueryPayload struct {
	OriginalQuery    string   `json:"original_query"`
	RewrittenQueries []string `json:"rewritten_queries,omitempty"`
	Timestamp        int64    `json:"timestamp"`
}

// DebugRetrievalPayload represents a retrieval event
type DebugRetrievalPayload struct {
	Query         string        `json:"query"`
	QueryIndex    int           `json:"query_index"`
	DocumentCount int           `json:"document_count"`
	Documents     []DocumentInfo `json:"documents,omitempty"`
	Duration      int64         `json:"duration_ms"`
	Timestamp     int64         `json:"timestamp"`
}

// DocumentInfo represents a retrieved document
type DocumentInfo struct {
	ID      string  `json:"id"`
	Content string  `json:"content"`
	Score   float64 `json:"score,omitempty"`
}

// Status constants
const (
	StatusStarted   = "started"
	StatusCompleted = "completed"
	StatusError     = "error"
	StatusPlanning  = "planning"
	StatusExecuting = "executing"
)

// Component constants
const (
	ComponentModel        = "model"
	ComponentRetriever    = "retriever"
	ComponentPrompt       = "prompt"
	ComponentPlanner      = "planner"
	ComponentQueryRewriter = "query_rewriter"
	ComponentFusion       = "fusion"
)
