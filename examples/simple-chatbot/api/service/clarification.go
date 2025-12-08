package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"simple-chatbot/api/service/db"
	"simple-chatbot/api/service/tools"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ClarificationEventType represents the type of clarification event
type ClarificationEventType string

const (
	ClarificationEventRequest  ClarificationEventType = "clarification:request"
	ClarificationEventResponse ClarificationEventType = "clarification:response"
	ClarificationEventResolved ClarificationEventType = "clarification:resolved"
)

// ClarificationRequest represents a request for user clarification
type ClarificationRequest struct {
	RequestID       string             `json:"request_id"`
	OriginalQuery   string             `json:"original_query"`
	Reason          string             `json:"reason"`
	Options         []ClarificationOpt `json:"options"`
	AllowFreeText   bool               `json:"allow_free_text"`
	ReachableLabels []string           `json:"reachable_labels,omitempty"`
}

// ClarificationOpt represents an option for clarification
type ClarificationOpt struct {
	ID          string `json:"id"`
	Label       string `json:"label"`       // Display label (e.g., "성능 점수")
	Description string `json:"description"` // Explanation
	TargetLabel string `json:"target_label"` // Neo4j label
}

// ClarificationResponse represents the user's response to clarification
type ClarificationResponse struct {
	RequestID  string `json:"request_id"`
	SelectedID string `json:"selected_id,omitempty"` // Selected option ID
	FreeText   string `json:"free_text,omitempty"`   // Free text input
}

// ClarificationResult represents the resolved clarification
type ClarificationResult struct {
	TargetLabels     []string `json:"target_labels"`
	TargetAttributes []string `json:"target_attributes"`
	RefinedQuery     string   `json:"refined_query"`
}

// ClarificationService handles interactive user clarification for ambiguous queries
type ClarificationService struct {
	neo4jClient   *db.Neo4jClient
	chatModel     einoModel.ChatModel
	queryAnalyzer *tools.QueryAnalyzerTool
	schemaInfo    *tools.SchemaInfo
}

// ClarificationConfig holds configuration for ClarificationService
type ClarificationConfig struct {
	Neo4jClient *db.Neo4jClient
	ChatModel   einoModel.ChatModel
}

// NewClarificationService creates a new clarification service
func NewClarificationService(config *ClarificationConfig) (*ClarificationService, error) {
	if config.Neo4jClient == nil {
		return nil, fmt.Errorf("neo4j client is required")
	}
	if config.ChatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}

	// Get schema info from Neo4j
	schemaInfo, err := getSchemaInfo(context.Background(), config.Neo4jClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get schema info: %w", err)
	}

	return &ClarificationService{
		neo4jClient:   config.Neo4jClient,
		chatModel:     config.ChatModel,
		queryAnalyzer: tools.NewQueryAnalyzerTool(config.ChatModel, schemaInfo),
		schemaInfo:    schemaInfo,
	}, nil
}

// getSchemaInfo retrieves schema info from Neo4j
func getSchemaInfo(ctx context.Context, client *db.Neo4jClient) (*tools.SchemaInfo, error) {
	schemaResult, err := client.GetSchema(ctx, false, false)
	if err != nil {
		return nil, err
	}

	info := &tools.SchemaInfo{
		Labels:        make([]string, 0),
		Relationships: make([]string, 0),
	}

	if labels, ok := schemaResult["labels"].([]any); ok {
		for _, l := range labels {
			if label, ok := l.(string); ok {
				info.Labels = append(info.Labels, label)
			}
		}
	}

	if rels, ok := schemaResult["relationships"].([]any); ok {
		for _, r := range rels {
			if rel, ok := r.(string); ok {
				info.Relationships = append(info.Relationships, rel)
			}
		}
	}

	return info, nil
}

// AnalyzeAndClarify analyzes a query and determines if clarification is needed
// Returns:
// - analysis: the query analysis result
// - clarificationRequest: if clarification is needed, contains the request to send to user
// - err: any error that occurred
func (s *ClarificationService) AnalyzeAndClarify(ctx context.Context, query string, sourceUUID string, requestID string) (*tools.QueryAnalysisOutput, *ClarificationRequest, error) {
	// Step 1: Analyze the query
	analysis, err := s.queryAnalyzer.Analyze(ctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("query analysis failed: %w", err)
	}

	// Step 2: Check if clarification is needed
	if !tools.NeedsClarification(analysis) {
		return analysis, nil, nil
	}

	// Step 3: Build clarification request
	clarificationReq, err := s.buildClarificationRequest(ctx, query, analysis, sourceUUID, requestID)
	if err != nil {
		// If we can't build clarification, return analysis without clarification
		return analysis, nil, nil
	}

	return analysis, clarificationReq, nil
}

// buildClarificationRequest creates a clarification request based on analysis
func (s *ClarificationService) buildClarificationRequest(ctx context.Context, query string, analysis *tools.QueryAnalysisOutput, sourceUUID string, requestID string) (*ClarificationRequest, error) {
	req := &ClarificationRequest{
		RequestID:     requestID,
		OriginalQuery: query,
		Reason:        tools.ClarificationReason(analysis),
		AllowFreeText: true,
		Options:       make([]ClarificationOpt, 0),
	}

	// If we have a source node, get reachable labels
	if sourceUUID != "" {
		reachableLabels, err := s.neo4jClient.GetReachableLabels(ctx, sourceUUID, 4)
		if err == nil && len(reachableLabels) > 0 {
			req.ReachableLabels = reachableLabels
			// Build options from reachable labels
			opts := s.labelsToOptions(reachableLabels, analysis)
			req.Options = opts
		}
	}

	// If no options from graph, use LLM to suggest options
	if len(req.Options) == 0 {
		opts, err := s.generateOptionsWithLLM(ctx, query, analysis)
		if err == nil && len(opts) > 0 {
			req.Options = opts
		}
	}

	return req, nil
}

// labelsToOptions converts Neo4j labels to clarification options
func (s *ClarificationService) labelsToOptions(labels []string, analysis *tools.QueryAnalysisOutput) []ClarificationOpt {
	// Map common labels to user-friendly descriptions
	labelDescriptions := map[string]struct {
		Label string
		Desc  string
	}{
		"PerformanceTotalScore":    {"성능 종합 점수", "차량의 전체 성능 점수"},
		"PerformanceAcceleration":  {"가속 성능", "0-100km/h 가속 시간 등"},
		"PerformanceBraking":       {"제동 성능", "브레이크 성능 점수"},
		"PerformanceHandling":      {"핸들링", "코너링 및 조향 성능"},
		"PerformanceRideComfort":   {"승차감", "승차 편안함 점수"},
		"Engine":                   {"엔진", "엔진 정보 및 사양"},
		"Transmission":             {"변속기", "변속기 정보"},
		"Tire":                     {"타이어", "타이어 정보"},
		"SafetyScore":              {"안전 점수", "안전 관련 점수"},
		"FuelEfficiency":           {"연비", "연비 정보"},
		"Price":                    {"가격", "차량 가격 정보"},
		"Manufacturer":             {"제조사", "제조사 정보"},
		"Segment":                  {"세그먼트", "차량 분류"},
		"Feature":                  {"기능", "차량 기능"},
		"Interior":                 {"인테리어", "실내 정보"},
		"Exterior":                 {"외관", "외관 정보"},
		"CompetitorVehicle":        {"경쟁 차량", "경쟁 차량 정보"},
		"SimilarVehicle":           {"유사 차량", "유사 차량 정보"},
	}

	options := make([]ClarificationOpt, 0)
	for i, label := range labels {
		if i >= 6 {
			break // Limit to 6 options
		}

		opt := ClarificationOpt{
			ID:          fmt.Sprintf("opt_%d", i+1),
			TargetLabel: label,
		}

		if desc, ok := labelDescriptions[label]; ok {
			opt.Label = desc.Label
			opt.Description = desc.Desc
		} else {
			// Use label as-is with simple formatting
			opt.Label = label
			opt.Description = fmt.Sprintf("%s 관련 정보", label)
		}

		options = append(options, opt)
	}

	return options
}

// generateOptionsWithLLM uses LLM to generate clarification options
func (s *ClarificationService) generateOptionsWithLLM(ctx context.Context, query string, analysis *tools.QueryAnalysisOutput) ([]ClarificationOpt, error) {
	prompt := fmt.Sprintf(`사용자 질문: "%s"

이 질문이 모호합니다. 사용자가 찾고자 하는 정보 유형에 대해 3-5개의 명확화 옵션을 제안해주세요.

사용 가능한 정보 유형 (Neo4j 라벨): %s

JSON 형식으로만 응답하세요:
{
  "options": [
    {"label": "표시 이름", "description": "설명", "target_label": "Neo4j라벨명"}
  ]
}`, query, strings.Join(s.schemaInfo.Labels, ", "))

	messages := []*schema.Message{
		schema.SystemMessage("You are a helpful assistant that generates clarification options for ambiguous queries. Always respond with valid JSON only."),
		schema.UserMessage(prompt),
	}

	response, err := s.chatModel.Generate(ctx, messages)
	if err != nil {
		return nil, err
	}

	// Parse response
	content := strings.TrimSpace(response.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result struct {
		Options []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
			TargetLabel string `json:"target_label"`
		} `json:"options"`
	}

	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, err
	}

	options := make([]ClarificationOpt, 0, len(result.Options))
	for i, opt := range result.Options {
		options = append(options, ClarificationOpt{
			ID:          fmt.Sprintf("opt_%d", i+1),
			Label:       opt.Label,
			Description: opt.Description,
			TargetLabel: opt.TargetLabel,
		})
	}

	return options, nil
}

// ResolveClarification processes the user's clarification response
func (s *ClarificationService) ResolveClarification(ctx context.Context, req *ClarificationRequest, resp *ClarificationResponse) (*ClarificationResult, error) {
	result := &ClarificationResult{
		TargetLabels:     make([]string, 0),
		TargetAttributes: make([]string, 0),
	}

	// Handle selected option
	if resp.SelectedID != "" {
		for _, opt := range req.Options {
			if opt.ID == resp.SelectedID {
				result.TargetLabels = append(result.TargetLabels, opt.TargetLabel)
				result.RefinedQuery = fmt.Sprintf("%s (%s)", req.OriginalQuery, opt.Label)
				return result, nil
			}
		}
	}

	// Handle free text response
	if resp.FreeText != "" {
		// Use LLM to interpret free text and extract target labels
		refined, err := s.interpretFreeText(ctx, req.OriginalQuery, resp.FreeText)
		if err != nil {
			return nil, err
		}
		return refined, nil
	}

	return nil, fmt.Errorf("no valid response provided")
}

// interpretFreeText uses LLM to interpret free text clarification
func (s *ClarificationService) interpretFreeText(ctx context.Context, originalQuery, freeText string) (*ClarificationResult, error) {
	prompt := fmt.Sprintf(`원래 질문: "%s"
사용자 추가 설명: "%s"

사용 가능한 정보 유형 (Neo4j 라벨): %s

사용자가 찾고자 하는 정보를 분석하고 JSON으로 응답하세요:
{
  "target_labels": ["관련 Neo4j 라벨들"],
  "target_attributes": ["필요한 속성들"],
  "refined_query": "명확해진 질문"
}`, originalQuery, freeText, strings.Join(s.schemaInfo.Labels, ", "))

	messages := []*schema.Message{
		schema.SystemMessage("You are a query interpreter. Analyze the user's clarification and determine what information they need. Respond with valid JSON only."),
		schema.UserMessage(prompt),
	}

	response, err := s.chatModel.Generate(ctx, messages)
	if err != nil {
		return nil, err
	}

	// Parse response
	content := strings.TrimSpace(response.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result ClarificationResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		// Fallback: use free text as refined query
		return &ClarificationResult{
			TargetLabels:     []string{},
			TargetAttributes: []string{},
			RefinedQuery:     fmt.Sprintf("%s - %s", originalQuery, freeText),
		}, nil
	}

	return &result, nil
}

// GetSchemaInfo returns the cached schema info
func (s *ClarificationService) GetSchemaInfo() *tools.SchemaInfo {
	return s.schemaInfo
}

// GetQueryAnalyzer returns the query analyzer tool
func (s *ClarificationService) GetQueryAnalyzer() *tools.QueryAnalyzerTool {
	return s.queryAnalyzer
}

// BuildInitialNodeRequest creates a clarification request for selecting the source entity (Phase 1)
func (s *ClarificationService) BuildInitialNodeRequest(
	ctx context.Context,
	query string,
	candidates []tools.NodeCandidate,
	requestID string,
) *ClarificationRequest {
	req := &ClarificationRequest{
		RequestID:     requestID,
		OriginalQuery: query,
		AllowFreeText: true,
		Options:       make([]ClarificationOpt, 0),
	}

	if len(candidates) == 0 {
		// No candidates found - ask user to specify
		req.Reason = "어떤 차량에 대해 알고 싶으신가요?"
		// Get popular vehicles from the database
		popularVehicles := s.getPopularVehicleOptions(ctx)
		if len(popularVehicles) > 0 {
			req.Options = popularVehicles
		}
	} else {
		// Multiple candidates - ask user to pick
		req.Reason = "다음 중 어떤 것에 대해 알고 싶으신가요?"
		req.Options = s.candidatesToOptions(candidates)
	}

	return req
}

// BuildTargetLabelRequest creates a clarification request for selecting target information (Phase 2)
func (s *ClarificationService) BuildTargetLabelRequest(
	ctx context.Context,
	query string,
	sourceNode tools.NodeCandidate,
	reachableLabels []string,
	requestID string,
) *ClarificationRequest {
	req := &ClarificationRequest{
		RequestID:       requestID,
		OriginalQuery:   query,
		AllowFreeText:   true,
		ReachableLabels: reachableLabels,
		Options:         make([]ClarificationOpt, 0),
	}

	// Format: "POLO에 대해 어떤 정보를 원하시나요?"
	req.Reason = fmt.Sprintf("%s에 대해 어떤 정보를 원하시나요?", sourceNode.Name)

	if len(reachableLabels) > 0 {
		req.Options = s.labelsToOptions(reachableLabels, nil)
	} else {
		// Fallback to LLM-generated options
		opts, err := s.generateOptionsWithLLM(ctx, query, nil)
		if err == nil && len(opts) > 0 {
			req.Options = opts
		}
	}

	return req
}

// candidatesToOptions converts node candidates to clarification options
// Now includes label type and variant info for clarity
// e.g., "[경쟁 차량] Tucson (1.6T HEV, Premium, 2024)"
func (s *ClarificationService) candidatesToOptions(candidates []tools.NodeCandidate) []ClarificationOpt {
	options := make([]ClarificationOpt, 0, min(6, len(candidates)))
	for i, c := range candidates {
		if i >= 6 {
			break
		}

		// Get the first label's display name for better UX
		labelDisplayName := ""
		if len(c.Labels) > 0 {
			labelDisplayName = db.GetLabelDisplayName(c.Labels[0])
		}

		// Format with variant info for disambiguation
		displayLabel := s.formatCandidateDisplay(c, labelDisplayName)

		// Build description with score and labels
		description := fmt.Sprintf("점수: %.2f", c.Score)
		if len(c.Labels) > 0 {
			description += fmt.Sprintf(" (%s)", strings.Join(c.Labels, ", "))
		}

		options = append(options, ClarificationOpt{
			ID:          fmt.Sprintf("node_%d", i+1),
			Label:       displayLabel,
			Description: description,
			TargetLabel: c.UUID, // Store UUID for later retrieval
		})
	}
	return options
}

// formatCandidateDisplay creates display string with variant info and 1-hop neighbor context
// Format: "[라벨타입] 이름 (variant info)" or "[라벨타입] 이름 (1-hop context)"
// e.g., "[경쟁 차량] Tucson (1.6T gsl MHEV, Prime, 2022)"
// e.g., "[Engine] 1.6T (브랜드: Hyundai; 연결: 3개 차량)"
// e.g., "[Part] OPENING EQUIPMENT-REAR DOOR (type: Assembly)"
func (s *ClarificationService) formatCandidateDisplay(c tools.NodeCandidate, labelDisplayName string) string {
	// Base: Name with label prefix
	displayLabel := c.Name
	if labelDisplayName != "" {
		displayLabel = fmt.Sprintf("[%s] %s", labelDisplayName, c.Name)
	} else if len(c.Labels) > 0 {
		// Use raw label if no display name available
		displayLabel = fmt.Sprintf("[%s] %s", c.Labels[0], c.Name)
	}

	// Collect context parts
	var contextParts []string

	// 1. Try variant info first (for vehicles)
	if c.VariantInfo != nil && !c.VariantInfo.IsEmpty() {
		variantDisplay := c.VariantInfo.FormatDisplay()
		if variantDisplay != "" {
			contextParts = append(contextParts, variantDisplay)
		}
	} else if c.Properties != nil {
		// Fallback: Try to extract from Properties directly
		variantInfo := db.ExtractVariantInfo(c.Properties)
		if !variantInfo.IsEmpty() {
			variantDisplay := variantInfo.FormatDisplay()
			if variantDisplay != "" {
				contextParts = append(contextParts, variantDisplay)
			}
		}
	}

	// 2. If still no context, try extracting from generic properties
	if len(contextParts) == 0 && c.Properties != nil {
		genericContext := extractGenericContext(c.Properties)
		if genericContext != "" {
			contextParts = append(contextParts, genericContext)
		}
	}

	// 3. Add 1-hop neighbor summary if still no context
	if len(contextParts) == 0 && len(c.NeighborSummary) > 0 {
		neighborContext := formatNeighborSummary(c.NeighborSummary)
		if neighborContext != "" {
			contextParts = append(contextParts, neighborContext)
		}
	}

	// Combine context
	if len(contextParts) > 0 {
		displayLabel = fmt.Sprintf("%s (%s)", displayLabel, strings.Join(contextParts, "; "))
	}

	return displayLabel
}

// formatNeighborSummary creates a brief description from neighbor info
// e.g., "엔진: 1.6T; 연결: PerformanceTotalScore"
func formatNeighborSummary(neighbors []db.NeighborInfo) string {
	if len(neighbors) == 0 {
		return ""
	}

	// Group by relationship type
	relMap := make(map[string][]string)
	for _, n := range neighbors {
		if n.Name != "" {
			relMap[n.Relationship] = append(relMap[n.Relationship], n.Name)
		}
	}

	// Format: "관계: 이름들"
	parts := make([]string, 0)
	for rel, names := range relMap {
		displayRel := db.GetRelationshipDisplayName(rel)
		if len(names) == 1 {
			parts = append(parts, fmt.Sprintf("%s: %s", displayRel, names[0]))
		} else if len(names) <= 3 {
			parts = append(parts, fmt.Sprintf("%s: %s", displayRel, strings.Join(names, ", ")))
		} else {
			parts = append(parts, fmt.Sprintf("%s: %s 외 %d개", displayRel, names[0], len(names)-1))
		}
	}

	// Limit to 2 relationship types for readability
	if len(parts) > 2 {
		parts = parts[:2]
	}

	return strings.Join(parts, "; ")
}

// getPopularVehicleOptions gets popular vehicles from the database
func (s *ClarificationService) getPopularVehicleOptions(ctx context.Context) []ClarificationOpt {
	// Query for vehicles in the database
	vehicles, err := s.neo4jClient.GetPopularVehicles(ctx, 6)
	if err != nil || len(vehicles) == 0 {
		return nil
	}

	options := make([]ClarificationOpt, 0, len(vehicles))
	for i, v := range vehicles {
		uuid, _ := v["uuid"].(string)
		name, _ := v["name"].(string)
		labels, _ := v["labels"].([]string)

		if uuid == "" || name == "" {
			continue
		}

		labelStr := "Vehicle"
		if len(labels) > 0 {
			labelStr = strings.Join(labels, ", ")
		}

		options = append(options, ClarificationOpt{
			ID:          fmt.Sprintf("vehicle_%d", i+1),
			Label:       name,
			Description: fmt.Sprintf("[%s]", labelStr),
			TargetLabel: uuid,
		})
	}
	return options
}

// helper function for min
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// extractGenericContext extracts context from generic node properties
// Looks for common descriptive properties like type, category, description, etc.
// Returns a formatted string like "type: Assembly" or "category: Electrical"
func extractGenericContext(props map[string]any) string {
	if props == nil {
		return ""
	}

	// Priority order for context extraction
	contextKeys := []string{
		"type",
		"category",
		"part_type",
		"component_type",
		"description",
		"desc",
		"class",
		"group",
		"kind",
		"status",
	}

	var contextParts []string
	for _, key := range contextKeys {
		if val, ok := props[key]; ok {
			var strVal string
			switch v := val.(type) {
			case string:
				strVal = v
			case fmt.Stringer:
				strVal = v.String()
			default:
				strVal = fmt.Sprintf("%v", v)
			}

			if strVal != "" && len(strVal) <= 50 { // Limit length for display
				contextParts = append(contextParts, fmt.Sprintf("%s: %s", key, strVal))
				if len(contextParts) >= 2 { // Limit to 2 properties
					break
				}
			}
		}
	}

	return strings.Join(contextParts, ", ")
}
