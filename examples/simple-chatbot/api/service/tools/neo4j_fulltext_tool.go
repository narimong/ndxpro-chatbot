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

// SearchWithFallback performs a search with multiple fallback strategies
// Strategy 1: Label-specific search (if label hint provided)
// Strategy 2: Generic search across all indexed nodes
// Strategy 3: Wildcard/Fuzzy search for partial matches
func (t *Neo4jFullTextSearchTool) SearchWithFallback(ctx context.Context, query string, labelHint string, topK int) ([]NodeCandidate, error) {
	if topK <= 0 {
		topK = 20
	}

	// Strategy 1: Try label-specific search if hint provided
	if labelHint != "" {
		results, err := t.client.FullTextSearchWithLabelGroup(ctx, query, labelHint, topK)
		if err == nil && len(results) > 0 {
			return t.mapResultsToCandidates(results), nil
		}
	}

	// Strategy 2: Try generic search with fallback (includes wildcard and fuzzy)
	genericResults, err := t.client.GenericFullTextSearchWithFallback(ctx, query, topK)
	if err == nil && len(genericResults) > 0 {
		return t.mapGenericResultsToCandidates(genericResults), nil
	}

	return []NodeCandidate{}, nil
}

// SearchGeneric performs a label-agnostic search with relationship context
func (t *Neo4jFullTextSearchTool) SearchGeneric(ctx context.Context, query string, topK int, includeContext bool) ([]NodeCandidate, error) {
	if topK <= 0 {
		topK = 20
	}

	results, err := t.client.GenericFullTextSearch(ctx, query, topK, includeContext)
	if err != nil {
		return nil, err
	}

	return t.mapGenericResultsToCandidates(results), nil
}

// mapResultsToCandidates converts map results to NodeCandidate slice
func (t *Neo4jFullTextSearchTool) mapResultsToCandidates(results []map[string]any) []NodeCandidate {
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
			candidate.VariantInfo = db.ExtractVariantInfo(props)
		}

		candidates = append(candidates, candidate)
	}
	return candidates
}

// mapGenericResultsToCandidates converts GenericSearchResult to NodeCandidate slice
func (t *Neo4jFullTextSearchTool) mapGenericResultsToCandidates(results []db.GenericSearchResult) []NodeCandidate {
	candidates := make([]NodeCandidate, 0, len(results))
	for _, r := range results {
		candidate := NodeCandidate{
			UUID:       r.UUID,
			Name:       r.Name,
			Labels:     r.Labels,
			Properties: r.Properties,
			Score:      r.Score,
		}

		// Extract variant info
		candidate.VariantInfo = db.ExtractVariantInfo(r.Properties)

		// Convert neighbor context to NeighborInfo
		if len(r.Neighbors) > 0 {
			candidate.NeighborSummary = make([]db.NeighborInfo, 0, len(r.Neighbors))
			for _, n := range r.Neighbors {
				neighbor := db.NeighborInfo{
					Relationship: n.Relationship,
					Outgoing:     n.Direction == "outgoing",
					Name:         n.NeighborName,
					UUID:         n.NeighborUUID,
					Labels:       n.NeighborLabels,
				}
				candidate.NeighborSummary = append(candidate.NeighborSummary, neighbor)
			}
		}

		candidates = append(candidates, candidate)
	}
	return candidates
}

// Ensure Neo4jFullTextSearchTool implements the required interfaces
var (
	_ tool.BaseTool      = (*Neo4jFullTextSearchTool)(nil)
	_ tool.InvokableTool = (*Neo4jFullTextSearchTool)(nil)
)
