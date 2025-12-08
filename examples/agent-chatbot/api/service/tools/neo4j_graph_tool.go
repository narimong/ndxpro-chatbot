package tools

import (
	"context"
	"encoding/json"

	"agent-chatbot/api/service/db"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Neo4jGraphTraversalTool traverses relationships in Neo4j graph
type Neo4jGraphTraversalTool struct {
	client *db.Neo4jClient
}

// GraphTraversalInput defines the input parameters for graph traversal
type GraphTraversalInput struct {
	StartUUID         string   `json:"start_uuid"`
	RelationshipTypes []string `json:"relationship_types,omitempty"`
	Direction         string   `json:"direction,omitempty"`
	MaxDepth          int      `json:"max_depth,omitempty"`
}

// GraphTraversalOutput represents the traversal result
type GraphTraversalOutput struct {
	StartNode NodeInfo        `json:"start_node"`
	Paths     []TraversalPath `json:"paths"`
}

// NodeInfo represents basic node information
type NodeInfo struct {
	UUID       string         `json:"uuid"`
	Labels     []string       `json:"labels"`
	Name       string         `json:"name"`
	Properties map[string]any `json:"properties,omitempty"`
}

// TraversalPath represents a path from start node to end node
type TraversalPath struct {
	Relationship string   `json:"relationship"`
	EndNode      NodeInfo `json:"end_node"`
}

// NewNeo4jGraphTraversalTool creates a new graph traversal tool
func NewNeo4jGraphTraversalTool(client *db.Neo4jClient) *Neo4jGraphTraversalTool {
	return &Neo4jGraphTraversalTool{client: client}
}

// Info returns the tool information for LLM
func (t *Neo4jGraphTraversalTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "neo4j_graph_traversal",
		Desc: "Traverse relationships in Neo4j graph from a starting node. Use this to explore related entities like 'Vehicle -> hasEngine -> Engine'.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"start_uuid": {
				Type:     schema.String,
				Desc:     "UUID of the starting node",
				Required: true,
			},
			"relationship_types": {
				Type:     schema.Array,
				Desc:     "Relationship types to follow (e.g., ['hasEngine', 'hasPart']). Empty = all types.",
				Required: false,
			},
			"direction": {
				Type:     schema.String,
				Desc:     "Traversal direction: OUTGOING (default), INCOMING, or BOTH",
				Required: false,
			},
			"max_depth": {
				Type:     schema.Integer,
				Desc:     "Maximum traversal depth (default: 2)",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the graph traversal
func (t *Neo4jGraphTraversalTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input GraphTraversalInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", err
	}

	// Set defaults
	if input.Direction == "" {
		input.Direction = "OUTGOING"
	}
	if input.MaxDepth <= 0 {
		input.MaxDepth = 2
	}

	// First, get the start node
	startNode, err := t.client.GetNodeByUUID(ctx, input.StartUUID)
	if err != nil {
		return "", err
	}

	startNodeInfo := extractNodeInfo(startNode)

	// Execute traversal
	results, err := t.client.TraverseGraph(ctx, input.StartUUID, input.RelationshipTypes, input.Direction, input.MaxDepth)
	if err != nil {
		return "", err
	}

	// Convert results to structured format
	paths := make([]TraversalPath, 0, len(results))
	for _, r := range results {
		path := TraversalPath{}

		if rel, ok := r["relationship"].(string); ok {
			path.Relationship = rel
		}

		path.EndNode = NodeInfo{
			Properties: make(map[string]any),
		}

		if uuid, ok := r["end_uuid"].(string); ok {
			path.EndNode.UUID = uuid
		}
		if name, ok := r["end_name"].(string); ok {
			path.EndNode.Name = name
		}
		if labels, ok := r["end_labels"].([]any); ok {
			for _, l := range labels {
				if labelStr, ok := l.(string); ok {
					path.EndNode.Labels = append(path.EndNode.Labels, labelStr)
				}
			}
		}
		if props, ok := r["end_properties"].(map[string]any); ok {
			path.EndNode.Properties = props
		}

		paths = append(paths, path)
	}

	output := GraphTraversalOutput{
		StartNode: startNodeInfo,
		Paths:     paths,
	}

	result, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", err
	}

	return string(result), nil
}

// extractNodeInfo extracts NodeInfo from a map result
func extractNodeInfo(m map[string]any) NodeInfo {
	info := NodeInfo{
		Properties: make(map[string]any),
	}

	if uuid, ok := m["uuid"].(string); ok {
		info.UUID = uuid
	}
	if labels, ok := m["labels"].([]any); ok {
		for _, l := range labels {
			if labelStr, ok := l.(string); ok {
				info.Labels = append(info.Labels, labelStr)
			}
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		info.Properties = props
		if name, ok := props["name"].(string); ok {
			info.Name = name
		}
	}

	return info
}

// Ensure Neo4jGraphTraversalTool implements the required interfaces
var (
	_ tool.BaseTool      = (*Neo4jGraphTraversalTool)(nil)
	_ tool.InvokableTool = (*Neo4jGraphTraversalTool)(nil)
)
