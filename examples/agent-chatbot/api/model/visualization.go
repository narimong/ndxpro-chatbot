package model

// Visualization event types
const (
	EventVizNode     = "viz:node"     // Node discovered (real-time)
	EventVizEdge     = "viz:edge"     // Relationship discovered (real-time)
	EventVizCypher   = "viz:cypher"   // Cypher query executed (real-time)
	EventVizComplete = "viz:complete" // Complete visualization data (final)
)

// VizNode - Node information for visualization
type VizNode struct {
	UUID       string         `json:"uuid"`
	Name       string         `json:"name"`
	Labels     []string       `json:"labels"`
	Properties map[string]any `json:"properties,omitempty"`
	Score      float64        `json:"score,omitempty"`
	Depth      int            `json:"depth,omitempty"`       // Hierarchy depth
	ParentUUID string         `json:"parent_uuid,omitempty"` // Hierarchy parent
	Source     string         `json:"source"`                // graph_search, explore_relations, etc.
}

// VizEdge - Relationship information for visualization
type VizEdge struct {
	ID           string `json:"id"`
	FromUUID     string `json:"from_uuid"`
	ToUUID       string `json:"to_uuid"`
	Relationship string `json:"relationship"`
	Direction    string `json:"direction"` // outgoing, incoming
}

// VizCypher - Executed Cypher query information
type VizCypher struct {
	QueryID     string         `json:"query_id"`
	Cypher      string         `json:"cypher"`
	Params      map[string]any `json:"params,omitempty"`
	ResultCount int            `json:"result_count"`
	Duration    int64          `json:"duration_ms"`
	Source      string         `json:"source"` // Which method executed this query
	Timestamp   int64          `json:"timestamp"`
}

// VizCompletePayload - Final complete visualization data
type VizCompletePayload struct {
	Nodes         []VizNode     `json:"nodes"`
	Edges         []VizEdge     `json:"edges"`
	CypherQueries []VizCypher   `json:"cypher_queries"`
	Hierarchy     *VizHierarchy `json:"hierarchy,omitempty"`
	AnswerNodeIDs []string      `json:"answer_node_ids"` // Nodes that directly contributed to the answer
	Timestamp     int64         `json:"timestamp"`
}

// VizHierarchy - Hierarchy structure (score tree, etc.)
type VizHierarchy struct {
	RootUUID string             `json:"root_uuid"`
	Tree     []VizHierarchyNode `json:"tree"`
}

// VizHierarchyNode - Single node in hierarchy tree
type VizHierarchyNode struct {
	UUID     string             `json:"uuid"`
	Name     string             `json:"name"`
	Labels   []string           `json:"labels"`
	Depth    int                `json:"depth"`
	Children []VizHierarchyNode `json:"children,omitempty"`
}
