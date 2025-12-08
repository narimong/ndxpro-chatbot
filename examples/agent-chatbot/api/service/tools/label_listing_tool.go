package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-chatbot/api/service/db"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// LabelListingTool lists nodes by their Neo4j label without keyword search
type LabelListingTool struct {
	client *db.Neo4jClient
}

// LabelListingInput defines the input parameters for label listing
type LabelListingInput struct {
	Labels  []string `json:"labels"`            // Neo4j labels to list (e.g., "CompetitorVehicle", "Engine")
	Limit   int      `json:"limit,omitempty"`   // Maximum number of nodes to return (default: 50)
	OrderBy string   `json:"order_by,omitempty"` // Property to sort by (default: "name")
}

// LabelListingItem represents a single node in the listing
type LabelListingItem struct {
	UUID        string            `json:"uuid"`
	Name        string            `json:"name"`
	Labels      []string          `json:"labels"`
	Properties  map[string]any    `json:"properties"`
	VariantInfo *db.VariantInfo   `json:"variant_info,omitempty"` // For vehicle nodes
}

// LabelListingResult represents the listing result
type LabelListingResult struct {
	Label      string             `json:"label"`
	DisplayName string            `json:"display_name"`
	Items      []LabelListingItem `json:"items"`
	TotalCount int                `json:"total_count"`
	ReturnedCount int             `json:"returned_count"`
	HasMore    bool               `json:"has_more"`
}

// LabelListingResults contains results for multiple labels
type LabelListingResults struct {
	Results []LabelListingResult `json:"results"`
}

// NewLabelListingTool creates a new label listing tool
func NewLabelListingTool(client *db.Neo4jClient) *LabelListingTool {
	return &LabelListingTool{client: client}
}

// Info returns the tool information for LLM
func (t *LabelListingTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	// Build available labels description
	listableLabels := db.GetListableLabels()
	labelsDesc := strings.Join(listableLabels, ", ")

	return &schema.ToolInfo{
		Name: "label_listing",
		Desc: fmt.Sprintf("Lists all nodes of a specific Neo4j label without keyword search. Use for 'list all X' queries. Available labels: %s", labelsDesc),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"labels": {
				Type:     schema.Array,
				Desc:     "Neo4j labels to list (e.g., ['CompetitorVehicle', 'Engine'])",
				Required: true,
			},
			"limit": {
				Type:     schema.Integer,
				Desc:     "Maximum number of nodes per label (default: 50, max: 200)",
				Required: false,
			},
			"order_by": {
				Type:     schema.String,
				Desc:     "Property to sort by (default: 'name')",
				Required: false,
			},
		}),
	}, nil
}

// InvokableRun executes the label listing
func (t *LabelListingTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input LabelListingInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", err
	}

	// Apply defaults
	if input.Limit <= 0 {
		input.Limit = 50
	}
	if input.Limit > 200 {
		input.Limit = 200
	}
	if input.OrderBy == "" {
		input.OrderBy = "name"
	}

	results, err := t.ListAll(ctx, input.Labels, input.Limit, input.OrderBy)
	if err != nil {
		return "", err
	}

	output, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// ListAll lists nodes for multiple labels
func (t *LabelListingTool) ListAll(ctx context.Context, labels []string, limit int, orderBy string) (*LabelListingResults, error) {
	if limit <= 0 {
		limit = 50
	}
	if orderBy == "" {
		orderBy = "name"
	}

	results := &LabelListingResults{
		Results: make([]LabelListingResult, 0, len(labels)),
	}

	for _, label := range labels {
		result, err := t.listByLabel(ctx, label, limit, orderBy)
		if err != nil {
			// Log error but continue with other labels
			continue
		}
		results.Results = append(results.Results, *result)
	}

	return results, nil
}

// listByLabel lists nodes for a single label
func (t *LabelListingTool) listByLabel(ctx context.Context, label string, limit int, orderBy string) (*LabelListingResult, error) {
	nodes, total, err := t.client.ListNodesByLabel(ctx, label, limit, orderBy)
	if err != nil {
		return nil, err
	}

	displayName := db.LabelDisplayNames[label]
	if displayName == "" {
		displayName = label
	}

	items := make([]LabelListingItem, 0, len(nodes))
	for _, node := range nodes {
		item := LabelListingItem{
			Properties: make(map[string]any),
		}

		if uuid, ok := node["uuid"].(string); ok {
			item.UUID = uuid
		}
		if name, ok := node["name"].(string); ok {
			item.Name = name
		}
		if labels, ok := node["labels"].([]any); ok {
			for _, l := range labels {
				if labelStr, ok := l.(string); ok {
					item.Labels = append(item.Labels, labelStr)
				}
			}
		}
		if props, ok := node["properties"].(map[string]any); ok {
			item.Properties = props
			// Extract variant info for vehicle nodes
			item.VariantInfo = db.ExtractVariantInfo(props)
		}

		items = append(items, item)
	}

	return &LabelListingResult{
		Label:         label,
		DisplayName:   displayName,
		Items:         items,
		TotalCount:    total,
		ReturnedCount: len(items),
		HasMore:       total > len(items),
	}, nil
}

// ListSingle lists nodes for a single label (convenience method)
func (t *LabelListingTool) ListSingle(ctx context.Context, label string, limit int) (*LabelListingResult, error) {
	return t.listByLabel(ctx, label, limit, "name")
}

// GetAvailableLabels returns all listable labels with their counts
func (t *LabelListingTool) GetAvailableLabels(ctx context.Context) ([]db.LabelOption, error) {
	return t.client.GetListableLabelOptions(ctx)
}

// FormatAsTable formats the listing result as a markdown table
func (t *LabelListingTool) FormatAsTable(result *LabelListingResult) string {
	if result == nil || len(result.Items) == 0 {
		return fmt.Sprintf("# %s\n\n등록된 항목이 없습니다.", result.DisplayName)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s 목록 (총 %d개)\n\n", result.DisplayName, result.TotalCount))

	// Determine columns based on label type
	if isVehicleLabel(result.Label) {
		sb.WriteString("| # | 이름 | 엔진 | 트림 | 연식 |\n")
		sb.WriteString("|---|------|------|------|------|\n")

		for i, item := range result.Items {
			engine, trim, modelYear := "", "", ""
			if item.VariantInfo != nil {
				engine = item.VariantInfo.Engine
				trim = item.VariantInfo.Trim
				modelYear = item.VariantInfo.ModelYear
			}
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s |\n",
				i+1, item.Name, engine, trim, modelYear))
		}
	} else {
		// Generic table for non-vehicle nodes
		sb.WriteString("| # | 이름 | 라벨 |\n")
		sb.WriteString("|---|------|------|\n")

		for i, item := range result.Items {
			labels := strings.Join(item.Labels, ", ")
			sb.WriteString(fmt.Sprintf("| %d | %s | %s |\n",
				i+1, item.Name, labels))
		}
	}

	if result.HasMore {
		sb.WriteString(fmt.Sprintf("\n... 그 외 %d개 더 있음\n", result.TotalCount-result.ReturnedCount))
	}

	return sb.String()
}

// isVehicleLabel checks if the label is a vehicle-related label
func isVehicleLabel(label string) bool {
	vehicleLabels := map[string]bool{
		"Vehicle":           true,
		"CompetitorVehicle": true,
		"SimilarVehicle":    true,
	}
	return vehicleLabels[label]
}

// Ensure LabelListingTool implements the required interfaces
var (
	_ tool.BaseTool      = (*LabelListingTool)(nil)
	_ tool.InvokableTool = (*LabelListingTool)(nil)
)
