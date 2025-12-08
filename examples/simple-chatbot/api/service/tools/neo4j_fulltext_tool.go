package tools

import (
	"context"
	"encoding/json"

	"simple-chatbot/api/service/db"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Neo4jFullTextSearchTool searches nodes using Neo4j Full-Text Index
type Neo4jFullTextSearchTool struct {
	client *db.Neo4jClient
}

// FullTextSearchInput defines the input parameters for full-text search
type FullTextSearchInput struct {
	Query       string `json:"query"`
	LabelFilter string `json:"label_filter,omitempty"`
	TopK        int    `json:"top_k,omitempty"`
}

// FullTextSearchResult represents a search result
type FullTextSearchResult struct {
	Candidates []NodeCandidate `json:"candidates"`
	TotalFound int             `json:"total_found"`
}

// NodeCandidate represents a candidate node from search
type NodeCandidate struct {
	UUID            string            `json:"uuid"`
	Labels          []string          `json:"labels"`
	Name            string            `json:"name"`
	Properties      map[string]any    `json:"properties"`
	Score           float64           `json:"score"`
	VariantInfo     *db.VariantInfo   `json:"variant_info,omitempty"`     // Variant info (engine, trim, year)
	NeighborSummary []db.NeighborInfo `json:"neighbor_summary,omitempty"` // 1-hop neighbor info for generic context
}

// NewNeo4jFullTextSearchTool creates a new full-text search tool
func NewNeo4jFullTextSearchTool(client *db.Neo4jClient) *Neo4jFullTextSearchTool {
	return &Neo4jFullTextSearchTool{client: client}
}

// Info returns the tool information for LLM
func (t *Neo4jFullTextSearchTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "neo4j_fulltext_search",
		Desc: "Search for nodes using Neo4j Full-Text Index. Returns node candidates with labels and properties. Supports Lucene query syntax (e.g., 'NE2', 'Engine*', 'NE2~1' for fuzzy).",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "Search keyword (e.g., 'NE2', 'Engine*'). Supports Lucene syntax.",
				Required: true,
			},
			"label_filter": {
				Type:     schema.String,
				Desc:     "Filter by Neo4j label (e.g., 'Vehicle'). Empty = all labels.",
				Required: false,
			},
			"top_k": {
				Type:     schema.Integer,
				Desc:     "Number of candidates to return (default: 20).",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the full-text search
func (t *Neo4jFullTextSearchTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input FullTextSearchInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", err
	}

	// Default TopK
	if input.TopK <= 0 {
		input.TopK = 20
	}

	results, err := t.client.FullTextSearch(ctx, input.Query, input.LabelFilter, input.TopK)
	if err != nil {
		return "", err
	}

	// Convert results to structured format
	candidates := make([]NodeCandidate, 0, len(results))
	for _, r := range results {
		candidate := NodeCandidate{
			Properties: make(map[string]any),
		}

		if uuid, ok := r["uuid"].(string); ok {
			candidate.UUID = uuid
		}
		if name, ok := r["name"].(string); ok {
			candidate.Name = name
		}
		if labels, ok := r["labels"].([]any); ok {
			for _, l := range labels {
				if labelStr, ok := l.(string); ok {
					candidate.Labels = append(candidate.Labels, labelStr)
				}
			}
		}
		if score, ok := r["score"].(float64); ok {
			candidate.Score = score
		}
		if props, ok := r["properties"].(map[string]any); ok {
			candidate.Properties = props
			// Extract variant info from properties
			candidate.VariantInfo = db.ExtractVariantInfo(props)
		}

		candidates = append(candidates, candidate)
	}

	searchResult := FullTextSearchResult{
		Candidates: candidates,
		TotalFound: len(candidates),
	}

	result, err := json.MarshalIndent(searchResult, "", "  ")
	if err != nil {
		return "", err
	}

	return string(result), nil
}

// Ensure Neo4jFullTextSearchTool implements the required interfaces
var (
	_ tool.BaseTool      = (*Neo4jFullTextSearchTool)(nil)
	_ tool.InvokableTool = (*Neo4jFullTextSearchTool)(nil)
)
