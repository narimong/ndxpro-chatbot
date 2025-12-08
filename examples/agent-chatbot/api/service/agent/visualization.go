package agent

import (
	"log"
	"strings"
	"sync"
	"time"

	"agent-chatbot/api/model"

	"github.com/google/uuid"
)

// VisualizationCollector - Collects visualization data during agent execution
type VisualizationCollector struct {
	mu            sync.RWMutex
	nodes         map[string]model.VizNode // UUID → Node (dedup)
	edges         []model.VizEdge
	cypherQueries []model.VizCypher
	hierarchyRoot string
	emitter       func(event string, data interface{})
}

// NewVisualizationCollector creates a new visualization collector
func NewVisualizationCollector(emitter func(event string, data interface{})) *VisualizationCollector {
	return &VisualizationCollector{
		nodes:         make(map[string]model.VizNode),
		edges:         make([]model.VizEdge, 0),
		cypherQueries: make([]model.VizCypher, 0),
		emitter:       emitter,
	}
}

// AddNode adds a node (updates if exists)
func (vc *VisualizationCollector) AddNode(node model.VizNode) {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	// Update if exists, add if new
	existing, exists := vc.nodes[node.UUID]
	if exists {
		// Merge properties - keep existing ones and add new ones
		if existing.Properties == nil {
			existing.Properties = make(map[string]any)
		}
		for k, v := range node.Properties {
			existing.Properties[k] = v
		}
		// Update other fields if they have values
		if node.Name != "" {
			existing.Name = node.Name
		}
		if len(node.Labels) > 0 {
			existing.Labels = node.Labels
		}
		if node.Score > 0 {
			existing.Score = node.Score
		}
		if node.Depth > 0 {
			existing.Depth = node.Depth
		}
		if node.ParentUUID != "" {
			existing.ParentUUID = node.ParentUUID
		}
		if node.Source != "" {
			existing.Source = node.Source
		}
		vc.nodes[node.UUID] = existing
	} else {
		vc.nodes[node.UUID] = node
	}
}

// AddEdge adds a relationship
func (vc *VisualizationCollector) AddEdge(edge model.VizEdge) {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	// Check for duplicate edges
	for _, e := range vc.edges {
		if e.FromUUID == edge.FromUUID && e.ToUUID == edge.ToUUID && e.Relationship == edge.Relationship {
			return // Duplicate, skip
		}
	}

	if edge.ID == "" {
		edge.ID = uuid.New().String()
	}
	vc.edges = append(vc.edges, edge)
}

// AddCypherQuery records an executed Cypher query
func (vc *VisualizationCollector) AddCypherQuery(query model.VizCypher) {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	if query.QueryID == "" {
		query.QueryID = uuid.New().String()
	}
	if query.Timestamp == 0 {
		query.Timestamp = time.Now().UnixMilli()
	}

	// Sanitize params before storing
	query.Params = sanitizeParams(query.Params)

	vc.cypherQueries = append(vc.cypherQueries, query)
}

// SetHierarchyRoot sets the root of hierarchy structure
func (vc *VisualizationCollector) SetHierarchyRoot(uuid string) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.hierarchyRoot = uuid
}

// EmitNode emits real-time node event
func (vc *VisualizationCollector) EmitNode(node model.VizNode) {
	log.Printf("[VIZ] EmitNode called: %s (emitter nil: %v)", node.Name, vc.emitter == nil)
	if vc.emitter != nil {
		vc.emitter(model.EventVizNode, node)
	}
}

// EmitEdge emits real-time edge event
func (vc *VisualizationCollector) EmitEdge(edge model.VizEdge) {
	log.Printf("[VIZ] EmitEdge called: %s (emitter nil: %v)", edge.Relationship, vc.emitter == nil)
	if vc.emitter != nil {
		vc.emitter(model.EventVizEdge, edge)
	}
}

// EmitCypher emits real-time cypher query event
func (vc *VisualizationCollector) EmitCypher(query model.VizCypher) {
	log.Printf("[VIZ] EmitCypher called: %s... (emitter nil: %v)", query.Cypher[:min(50, len(query.Cypher))], vc.emitter == nil)
	if vc.emitter != nil {
		vc.emitter(model.EventVizCypher, query)
	}
}

// GetCompletePayload generates the final visualization payload
func (vc *VisualizationCollector) GetCompletePayload() *model.VizCompletePayload {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	// Convert nodes map to slice
	nodes := make([]model.VizNode, 0, len(vc.nodes))
	for _, node := range vc.nodes {
		nodes = append(nodes, node)
	}

	// Extract answer node IDs (nodes that were used in final context)
	answerNodeIDs := make([]string, 0)
	for uuid := range vc.nodes {
		answerNodeIDs = append(answerNodeIDs, uuid)
	}

	payload := &model.VizCompletePayload{
		Nodes:         nodes,
		Edges:         vc.edges,
		CypherQueries: vc.cypherQueries,
		AnswerNodeIDs: answerNodeIDs,
		Timestamp:     time.Now().UnixMilli(),
	}

	// Build hierarchy if root is set
	if vc.hierarchyRoot != "" {
		payload.Hierarchy = vc.BuildHierarchy()
	}

	return payload
}

// BuildHierarchy builds hierarchy structure from collected nodes
func (vc *VisualizationCollector) BuildHierarchy() *model.VizHierarchy {
	if vc.hierarchyRoot == "" {
		return nil
	}

	hierarchy := &model.VizHierarchy{
		RootUUID: vc.hierarchyRoot,
		Tree:     make([]model.VizHierarchyNode, 0),
	}

	// Build parent → children map
	childrenMap := make(map[string][]model.VizNode)
	for _, node := range vc.nodes {
		if node.ParentUUID != "" {
			childrenMap[node.ParentUUID] = append(childrenMap[node.ParentUUID], node)
		} else if node.Depth == 1 {
			// Direct children of root
			childrenMap[vc.hierarchyRoot] = append(childrenMap[vc.hierarchyRoot], node)
		}
	}

	// Build tree recursively from root's children
	for _, child := range childrenMap[vc.hierarchyRoot] {
		treeNode := vc.buildHierarchyNode(child, childrenMap)
		hierarchy.Tree = append(hierarchy.Tree, treeNode)
	}

	return hierarchy
}

func (vc *VisualizationCollector) buildHierarchyNode(node model.VizNode, childrenMap map[string][]model.VizNode) model.VizHierarchyNode {
	treeNode := model.VizHierarchyNode{
		UUID:     node.UUID,
		Name:     node.Name,
		Labels:   node.Labels,
		Depth:    node.Depth,
		Children: make([]model.VizHierarchyNode, 0),
	}

	// Recursively add children
	for _, child := range childrenMap[node.UUID] {
		childTreeNode := vc.buildHierarchyNode(child, childrenMap)
		treeNode.Children = append(treeNode.Children, childTreeNode)
	}

	return treeNode
}

// GetNodeCount returns the number of collected nodes
func (vc *VisualizationCollector) GetNodeCount() int {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	return len(vc.nodes)
}

// GetEdgeCount returns the number of collected edges
func (vc *VisualizationCollector) GetEdgeCount() int {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	return len(vc.edges)
}

// GetQueryCount returns the number of collected queries
func (vc *VisualizationCollector) GetQueryCount() int {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	return len(vc.cypherQueries)
}

// sanitizeParams removes large/sensitive parameters
func sanitizeParams(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}

	sanitized := make(map[string]any)
	for k, v := range params {
		// Skip embedding vectors and other large data
		if k == "embedding" || k == "vector" || k == "embeddings" {
			sanitized[k] = "[VECTOR_OMITTED]"
			continue
		}

		// Truncate long strings
		if str, ok := v.(string); ok && len(str) > 500 {
			sanitized[k] = str[:500] + "...[TRUNCATED]"
			continue
		}

		// Skip very large arrays
		if arr, ok := v.([]any); ok && len(arr) > 100 {
			sanitized[k] = "[LARGE_ARRAY_OMITTED]"
			continue
		}

		// Skip internal fields
		if strings.HasPrefix(k, "_") {
			continue
		}

		sanitized[k] = v
	}

	return sanitized
}
