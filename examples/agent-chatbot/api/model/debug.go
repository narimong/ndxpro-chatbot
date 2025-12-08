package model

// Debug event types for Socket.IO
const (
	EventDebugStep          = "debug:step"          // Component execution start/end
	EventDebugPlan          = "debug:plan"          // Planning information
	EventDebugQuery         = "debug:query"         // Query decomposition/rewriting
	EventDebugRetrieval     = "debug:retrieval"     // Retrieval results
	EventDebugShortestPath  = "debug:shortest_path" // Shortest path navigation
	EventClarificationReq   = "clarification:request"  // Clarification needed
	EventClarificationResp  = "clarification:response" // User response to clarification
	EventClarificationDone  = "clarification:resolved" // Clarification resolved

	// Agent-specific events
	EventAgentStart     = "agent:start"     // Agent execution started
	EventAgentIteration = "agent:iteration" // Agent iteration progress
	EventAgentToolCall  = "agent:tool_call" // Agent tool invocation
	EventAgentEvaluate  = "agent:evaluate"  // Agent result evaluation
	EventAgentComplete  = "agent:complete"  // Agent execution completed
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
	ComponentModel         = "model"
	ComponentRetriever     = "retriever"
	ComponentPrompt        = "prompt"
	ComponentPlanner       = "planner"
	ComponentQueryRewriter = "query_rewriter"
	ComponentFusion        = "fusion"
	ComponentShortestPath  = "shortest_path"
	ComponentClarification = "clarification"
)

// ClarificationRequestPayload represents a clarification request to the user
type ClarificationRequestPayload struct {
	RequestID       string                `json:"request_id"`
	OriginalQuery   string                `json:"original_query"`
	Reason          string                `json:"reason"`
	Options         []ClarificationOption `json:"options"`
	AllowFreeText   bool                  `json:"allow_free_text"`
	ReachableLabels []string              `json:"reachable_labels,omitempty"`
	Timestamp       int64                 `json:"timestamp"`
}

// ClarificationOption represents an option for clarification
type ClarificationOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	TargetLabel string `json:"target_label"`
}

// ClarificationResponsePayload represents the user's response to clarification
type ClarificationResponsePayload struct {
	RequestID  string `json:"request_id"`
	SelectedID string `json:"selected_id,omitempty"`
	FreeText   string `json:"free_text,omitempty"`
	Timestamp  int64  `json:"timestamp"`
}

// ShortestPathPayload represents a shortest path navigation event
type ShortestPathPayload struct {
	SourceNode  string     `json:"source_node"`
	TargetLabel string     `json:"target_label"`
	PathsFound  int        `json:"paths_found"`
	Paths       []PathInfo `json:"paths,omitempty"`
	Timestamp   int64      `json:"timestamp"`
}

// PathInfo represents a single path result
type PathInfo struct {
	PathChain  string `json:"path_chain"`
	Hops       int    `json:"hops"`
	TargetName string `json:"target_name"`
}

// Agent-specific payloads

// AgentStartPayload represents agent execution start
type AgentStartPayload struct {
	MaxIterations       int     `json:"max_iterations"`
	ConfidenceThreshold float64 `json:"confidence_threshold"`
	ToolsCount          int     `json:"tools_count"`
	Timestamp           int64   `json:"timestamp"`
}

// AgentIterationPayload represents agent iteration progress
type AgentIterationPayload struct {
	Iteration        int      `json:"iteration"`
	MaxIterations    int      `json:"max_iterations"`
	CollectedDocs    int      `json:"collected_docs"`
	DiscoveredNodes  int      `json:"discovered_nodes"`
	AttemptedQueries []string `json:"attempted_queries,omitempty"`
	Timestamp        int64    `json:"timestamp"`
}

// AgentToolCallPayload represents an agent tool invocation
type AgentToolCallPayload struct {
	ToolName  string `json:"tool_name"`
	Arguments string `json:"arguments"`
	Iteration int    `json:"iteration"`
	Timestamp int64  `json:"timestamp"`
}

// AgentToolResultPayload represents a tool execution result
type AgentToolResultPayload struct {
	ToolName   string `json:"tool_name"`
	Success    bool   `json:"success"`
	ResultSize int    `json:"result_size"`
	Duration   int64  `json:"duration_ms"`
	Timestamp  int64  `json:"timestamp"`
}

// AgentEvaluatePayload represents agent evaluation result
type AgentEvaluatePayload struct {
	IsSufficient    bool     `json:"is_sufficient"`
	Confidence      float64  `json:"confidence"`
	MissingInfo     []string `json:"missing_info,omitempty"`
	SuggestedAction string   `json:"suggested_action,omitempty"`
	Timestamp       int64    `json:"timestamp"`
}

// AgentCompletePayload represents agent execution completion
type AgentCompletePayload struct {
	Status          string `json:"status"` // completed, error, timeout
	TotalIterations int    `json:"total_iterations"`
	TotalToolCalls  int    `json:"total_tool_calls"`
	Duration        int64  `json:"duration_ms"`
	StopReason      string `json:"stop_reason,omitempty"`
	Timestamp       int64  `json:"timestamp"`
}

// Component constant for agent
const (
	ComponentAgent = "agent"
)
