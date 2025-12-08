package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"simple-chatbot/api/service/db"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Neo4jShortestPathTool finds shortest paths from a source node to nodes with a target label
type Neo4jShortestPathTool struct {
	client *db.Neo4jClient
}

// ShortestPathInput defines the input for shortest path queries
type ShortestPathInput struct {
	SourceUUID  string `json:"source_uuid"`
	TargetLabel string `json:"target_label"`
	MaxHops     int    `json:"max_hops,omitempty"` // default: 8
	TopK        int    `json:"top_k,omitempty"`    // default: 3
}

// ShortestPathOutput defines the output of shortest path queries
type ShortestPathOutput struct {
	Paths        []PathInfo `json:"paths"`
	ShortestHops int        `json:"shortest_hops"`
	TotalFound   int        `json:"total_found"`
	APOCUsed     bool       `json:"apoc_used"`
}

// PathInfo represents a single path result
type PathInfo struct {
	PathChain    string         `json:"path_chain"`
	TargetUUID   string         `json:"target_uuid"`
	TargetName   string         `json:"target_name"`
	TargetLabels []string       `json:"target_labels"`
	TargetProps  map[string]any `json:"target_properties"`
	Hops         int            `json:"hops"`
}

// NewNeo4jShortestPathTool creates a new shortest path tool
func NewNeo4jShortestPathTool(client *db.Neo4jClient) *Neo4jShortestPathTool {
	return &Neo4jShortestPathTool{client: client}
}

// Info returns tool information for LLM
func (t *Neo4jShortestPathTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "neo4j_shortest_path",
		Desc: `Find shortest paths from a source node to all nodes of a target label.
This tool automatically finds the shortest path without needing to specify relationship types.
Example: Find path from Vehicle to PerformanceTotalScore.
Returns linearized paths with target node properties.`,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"source_uuid": {
				Type:     schema.String,
				Desc:     "UUID of the starting node",
				Required: true,
			},
			"target_label": {
				Type:     schema.String,
				Desc:     "Label of the target node type (e.g., 'PerformanceTotalScore', 'Engine')",
				Required: true,
			},
			"max_hops": {
				Type:     schema.Integer,
				Desc:     "Maximum path length (default: 8, max: 10)",
				Required: false,
			},
			"top_k": {
				Type:     schema.Integer,
				Desc:     "Number of paths to return (default: 3)",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the shortest path query
func (t *Neo4jShortestPathTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input ShortestPathInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	// Validate input
	if input.SourceUUID == "" {
		return "", fmt.Errorf("source_uuid is required")
	}
	if input.TargetLabel == "" {
		return "", fmt.Errorf("target_label is required")
	}

	// Apply defaults
	if input.MaxHops <= 0 {
		input.MaxHops = 8
	}
	if input.MaxHops > 10 {
		input.MaxHops = 10
	}
	if input.TopK <= 0 {
		input.TopK = 3
	}

	// Execute query
	paths, err := t.client.ShortestPathToLabel(ctx, input.SourceUUID, input.TargetLabel, input.MaxHops, input.TopK)
	if err != nil {
		return "", fmt.Errorf("shortest path query failed: %w", err)
	}

	// Build output
	output := ShortestPathOutput{
		Paths:      make([]PathInfo, 0, len(paths)),
		TotalFound: len(paths),
		APOCUsed:   t.client.IsAPOCAvailable(),
	}

	for _, p := range paths {
		pathInfo := PathInfo{
			PathChain:    p.PathChain,
			TargetUUID:   p.EndNodeUUID,
			TargetName:   p.EndNodeName,
			TargetLabels: p.EndNodeLabels,
			TargetProps:  p.EndProperties,
			Hops:         p.Hops,
		}
		output.Paths = append(output.Paths, pathInfo)
	}

	if len(paths) > 0 {
		output.ShortestHops = paths[0].Hops
	}

	result, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("failed to marshal output: %w", err)
	}

	return string(result), nil
}

// Ensure Neo4jShortestPathTool implements the required interfaces
var (
	_ tool.BaseTool      = (*Neo4jShortestPathTool)(nil)
	_ tool.InvokableTool = (*Neo4jShortestPathTool)(nil)
)
