package agent

import (
	"encoding/json"
	"testing"

	apiModel "agent-chatbot/api/model"
)

func TestVisualizationCollector_AddNode(t *testing.T) {
	emittedEvents := make([]interface{}, 0)
	emitter := func(event string, data interface{}) {
		emittedEvents = append(emittedEvents, map[string]interface{}{
			"event": event,
			"data":  data,
		})
	}

	vc := NewVisualizationCollector(emitter)

	// Add a node
	node1 := apiModel.VizNode{
		UUID:   "uuid-1",
		Name:   "TestNode1",
		Labels: []string{"Vehicle"},
		Score:  0.95,
		Source: "graph_search",
	}
	vc.AddNode(node1)
	vc.EmitNode(node1)

	if vc.GetNodeCount() != 1 {
		t.Errorf("Expected 1 node, got %d", vc.GetNodeCount())
	}

	// Add duplicate node - should update
	node1Updated := apiModel.VizNode{
		UUID:       "uuid-1",
		Name:       "TestNode1Updated",
		Labels:     []string{"Vehicle", "SUV"},
		Properties: map[string]any{"year": 2024},
		Source:     "explore_relations",
	}
	vc.AddNode(node1Updated)

	if vc.GetNodeCount() != 1 {
		t.Errorf("Expected still 1 node after update, got %d", vc.GetNodeCount())
	}

	// Add different node
	node2 := apiModel.VizNode{
		UUID:   "uuid-2",
		Name:   "TestNode2",
		Labels: []string{"Score"},
		Source: "explore_relations",
	}
	vc.AddNode(node2)

	if vc.GetNodeCount() != 2 {
		t.Errorf("Expected 2 nodes, got %d", vc.GetNodeCount())
	}

	// Check emitted events
	if len(emittedEvents) != 1 {
		t.Errorf("Expected 1 emitted event (from EmitNode), got %d", len(emittedEvents))
	}
}

func TestVisualizationCollector_AddEdge(t *testing.T) {
	vc := NewVisualizationCollector(nil)

	// Add edge
	edge1 := apiModel.VizEdge{
		FromUUID:     "uuid-1",
		ToUUID:       "uuid-2",
		Relationship: "hasScore",
		Direction:    "outgoing",
	}
	vc.AddEdge(edge1)

	if vc.GetEdgeCount() != 1 {
		t.Errorf("Expected 1 edge, got %d", vc.GetEdgeCount())
	}

	// Add duplicate edge - should be skipped
	vc.AddEdge(edge1)

	if vc.GetEdgeCount() != 1 {
		t.Errorf("Expected still 1 edge after duplicate, got %d", vc.GetEdgeCount())
	}

	// Add different edge
	edge2 := apiModel.VizEdge{
		FromUUID:     "uuid-2",
		ToUUID:       "uuid-3",
		Relationship: "hasSubScore",
		Direction:    "outgoing",
	}
	vc.AddEdge(edge2)

	if vc.GetEdgeCount() != 2 {
		t.Errorf("Expected 2 edges, got %d", vc.GetEdgeCount())
	}
}

func TestVisualizationCollector_AddCypherQuery(t *testing.T) {
	vc := NewVisualizationCollector(nil)

	// Add query
	query := apiModel.VizCypher{
		Cypher:      "MATCH (n) RETURN n LIMIT 10",
		Params:      map[string]any{"limit": 10},
		ResultCount: 5,
		Duration:    100,
		Source:      "GenericFullTextSearch",
	}
	vc.AddCypherQuery(query)

	if vc.GetQueryCount() != 1 {
		t.Errorf("Expected 1 query, got %d", vc.GetQueryCount())
	}

	// Add query with embedding (should be sanitized)
	queryWithEmbedding := apiModel.VizCypher{
		Cypher: "MATCH (n) WHERE n.embedding = $embedding RETURN n",
		Params: map[string]any{
			"embedding": []float64{0.1, 0.2, 0.3, 0.4, 0.5},
			"name":      "test",
		},
		ResultCount: 1,
		Source:      "VectorSearch",
	}
	vc.AddCypherQuery(queryWithEmbedding)

	if vc.GetQueryCount() != 2 {
		t.Errorf("Expected 2 queries, got %d", vc.GetQueryCount())
	}
}

func TestVisualizationCollector_GetCompletePayload(t *testing.T) {
	vc := NewVisualizationCollector(nil)

	// Add nodes
	vc.AddNode(apiModel.VizNode{UUID: "uuid-1", Name: "Node1", Labels: []string{"A"}, Source: "test"})
	vc.AddNode(apiModel.VizNode{UUID: "uuid-2", Name: "Node2", Labels: []string{"B"}, Source: "test"})

	// Add edge
	vc.AddEdge(apiModel.VizEdge{FromUUID: "uuid-1", ToUUID: "uuid-2", Relationship: "rel"})

	// Add query
	vc.AddCypherQuery(apiModel.VizCypher{Cypher: "MATCH (n) RETURN n", ResultCount: 2})

	// Get payload
	payload := vc.GetCompletePayload()

	if len(payload.Nodes) != 2 {
		t.Errorf("Expected 2 nodes in payload, got %d", len(payload.Nodes))
	}

	if len(payload.Edges) != 1 {
		t.Errorf("Expected 1 edge in payload, got %d", len(payload.Edges))
	}

	if len(payload.CypherQueries) != 1 {
		t.Errorf("Expected 1 query in payload, got %d", len(payload.CypherQueries))
	}

	if len(payload.AnswerNodeIDs) != 2 {
		t.Errorf("Expected 2 answer node IDs, got %d", len(payload.AnswerNodeIDs))
	}

	if payload.Timestamp == 0 {
		t.Error("Expected timestamp to be set")
	}
}

func TestVisualizationCollector_BuildHierarchy(t *testing.T) {
	vc := NewVisualizationCollector(nil)

	// Set hierarchy root
	vc.SetHierarchyRoot("root-uuid")

	// Add root node
	vc.AddNode(apiModel.VizNode{UUID: "root-uuid", Name: "Root", Labels: []string{"Score"}, Depth: 0})

	// Add depth 1 children
	vc.AddNode(apiModel.VizNode{UUID: "child-1", Name: "Child1", Labels: []string{"SubScore"}, Depth: 1, ParentUUID: "root-uuid"})
	vc.AddNode(apiModel.VizNode{UUID: "child-2", Name: "Child2", Labels: []string{"SubScore"}, Depth: 1, ParentUUID: "root-uuid"})

	// Add depth 2 children
	vc.AddNode(apiModel.VizNode{UUID: "grandchild-1", Name: "Grandchild1", Labels: []string{"Detail"}, Depth: 2, ParentUUID: "child-1"})

	// Build hierarchy
	hierarchy := vc.BuildHierarchy()

	if hierarchy == nil {
		t.Fatal("Expected hierarchy to be built")
	}

	if hierarchy.RootUUID != "root-uuid" {
		t.Errorf("Expected root UUID 'root-uuid', got '%s'", hierarchy.RootUUID)
	}

	if len(hierarchy.Tree) != 2 {
		t.Errorf("Expected 2 top-level children, got %d", len(hierarchy.Tree))
	}

	// Find child-1 and check its children
	var child1 *apiModel.VizHierarchyNode
	for i := range hierarchy.Tree {
		if hierarchy.Tree[i].UUID == "child-1" {
			child1 = &hierarchy.Tree[i]
			break
		}
	}

	if child1 == nil {
		t.Fatal("Expected to find child-1 in hierarchy")
	}

	if len(child1.Children) != 1 {
		t.Errorf("Expected child-1 to have 1 child, got %d", len(child1.Children))
	}
}

func TestVisualizationCollector_JSONSerialization(t *testing.T) {
	vc := NewVisualizationCollector(nil)

	vc.AddNode(apiModel.VizNode{
		UUID:       "uuid-1",
		Name:       "TestNode",
		Labels:     []string{"Vehicle"},
		Properties: map[string]any{"score": 85.5, "year": 2024},
		Score:      0.95,
		Source:     "graph_search",
	})

	vc.AddEdge(apiModel.VizEdge{
		FromUUID:     "uuid-1",
		ToUUID:       "uuid-2",
		Relationship: "hasScore",
		Direction:    "outgoing",
	})

	vc.AddCypherQuery(apiModel.VizCypher{
		Cypher:      "MATCH (n) RETURN n",
		ResultCount: 1,
		Duration:    50,
		Source:      "test",
	})

	payload := vc.GetCompletePayload()

	// Serialize to JSON
	jsonBytes, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("Failed to serialize payload: %v", err)
	}

	// Check that it's valid JSON
	var parsed apiModel.VizCompletePayload
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("Failed to parse serialized JSON: %v", err)
	}

	if len(parsed.Nodes) != 1 {
		t.Errorf("Expected 1 node after round-trip, got %d", len(parsed.Nodes))
	}

	t.Logf("Serialized payload:\n%s", string(jsonBytes))
}

func TestSanitizeParams(t *testing.T) {
	params := map[string]any{
		"embedding":    []float64{0.1, 0.2, 0.3},
		"vector":       []float64{0.4, 0.5, 0.6},
		"normalParam":  "test",
		"longString":   string(make([]byte, 600)),
		"_internalKey": "hidden",
		"number":       42,
	}

	sanitized := sanitizeParams(params)

	if sanitized["embedding"] != "[VECTOR_OMITTED]" {
		t.Error("Expected embedding to be sanitized")
	}

	if sanitized["vector"] != "[VECTOR_OMITTED]" {
		t.Error("Expected vector to be sanitized")
	}

	if sanitized["normalParam"] != "test" {
		t.Error("Expected normalParam to be preserved")
	}

	longStr, ok := sanitized["longString"].(string)
	if !ok || len(longStr) > 520 {
		t.Error("Expected long string to be truncated")
	}

	if _, exists := sanitized["_internalKey"]; exists {
		t.Error("Expected internal key to be removed")
	}

	if sanitized["number"] != 42 {
		t.Error("Expected number to be preserved")
	}
}
