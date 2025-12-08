package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// QueryAnalyzerTool analyzes user queries to extract entities and target labels
type QueryAnalyzerTool struct {
	chatModel  model.ChatModel
	schemaInfo *SchemaInfo
}

// SchemaInfo holds Neo4j schema information for query analysis
type SchemaInfo struct {
	Labels        []string `json:"labels"`
	Relationships []string `json:"relationships"`
}

// QueryAnalysisInput defines input for query analysis
type QueryAnalysisInput struct {
	UserQuery string `json:"user_query"`
}

// QueryAnalysisOutput defines the analysis result
type QueryAnalysisOutput struct {
	StartEntity    StartEntityInfo `json:"start_entity"`
	Target         TargetInfo      `json:"target"`
	QueryType      string          `json:"query_type"` // factual, comparison, list, aggregation
	IsHierarchical bool            `json:"is_hierarchical"` // Whether user wants hierarchical/nested data
	HierarchyDepth int             `json:"hierarchy_depth"` // Suggested depth for hierarchical queries (1-5)
	Confidence     float64         `json:"confidence"`
	Reasoning      string          `json:"reasoning"`
}

// StartEntityInfo contains information about the starting entity
type StartEntityInfo struct {
	Keywords       []string `json:"keywords"`
	ExpectedLabels []string `json:"expected_labels"`
}

// TargetInfo contains information about the target (what user is looking for)
type TargetInfo struct {
	ExpectedLabels []string `json:"expected_labels"`
	ExpectedRels   []string `json:"expected_relationships"`
	Attributes     []string `json:"attributes"`
}

// NewQueryAnalyzerTool creates a new query analyzer tool
func NewQueryAnalyzerTool(chatModel model.ChatModel, schemaInfo *SchemaInfo) *QueryAnalyzerTool {
	return &QueryAnalyzerTool{
		chatModel:  chatModel,
		schemaInfo: schemaInfo,
	}
}

// Info returns tool information
func (t *QueryAnalyzerTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "query_analyzer",
		Desc: "Analyze user query to extract start entity, target labels, and relationships",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"user_query": {
				Type:     schema.String,
				Desc:     "User's natural language query",
				Required: true,
			},
		}),
	}, nil
}

// InvokableRun executes query analysis
func (t *QueryAnalyzerTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var input QueryAnalysisInput
	if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
		return "", fmt.Errorf("failed to parse input: %w", err)
	}

	if input.UserQuery == "" {
		return "", fmt.Errorf("user_query is required")
	}

	prompt := t.buildAnalysisPrompt(input.UserQuery)

	messages := []*schema.Message{
		schema.SystemMessage("You are a query analyzer for a vehicle knowledge graph. Extract entities and relationships from user queries. Always respond with valid JSON only."),
		schema.UserMessage(prompt),
	}

	response, err := t.chatModel.Generate(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("query analysis failed: %w", err)
	}

	output, err := t.parseAnalysisResponse(response.Content)
	if err != nil {
		// Return default analysis on parse failure
		output = t.defaultAnalysis(input.UserQuery)
	}

	result, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("failed to marshal output: %w", err)
	}

	return string(result), nil
}

// Analyze performs query analysis and returns the result directly
func (t *QueryAnalyzerTool) Analyze(ctx context.Context, query string) (*QueryAnalysisOutput, error) {
	input, _ := json.Marshal(QueryAnalysisInput{UserQuery: query})
	resultJSON, err := t.InvokableRun(ctx, string(input))
	if err != nil {
		return nil, err
	}

	var output QueryAnalysisOutput
	if err := json.Unmarshal([]byte(resultJSON), &output); err != nil {
		return nil, err
	}

	return &output, nil
}

func (t *QueryAnalyzerTool) buildAnalysisPrompt(query string) string {
	labels := strings.Join(t.schemaInfo.Labels, ", ")
	rels := strings.Join(t.schemaInfo.Relationships, ", ")

	return fmt.Sprintf(`Analyze this query for a vehicle knowledge graph.

Available Node Labels: %s
Available Relationships: %s

User Query: %s

Extract:
1. start_entity: Main entity to search for
   - keywords: Search terms (e.g., vehicle name like "POLO", "NE2", "Tucson")
   - expected_labels: Most likely node types (e.g., ["Vehicle", "CompetitorVehicle"])

2. target: Information the user wants
   - expected_labels: Target node types (e.g., ["PerformanceTotalScore", "Engine"])
   - expected_relationships: Relationships to follow (e.g., ["hasPerformanceScore", "hasEngine"])
   - attributes: Specific properties wanted (e.g., ["score", "power"])

3. query_type: factual, comparison, list, or aggregation

4. is_hierarchical: true if user is asking for nested/hierarchical data
   - Hierarchical keywords: "하위", "세부", "모두", "전체", "상세", "연결된", "관련", "포함"
   - English: "breakdown", "details", "all", "complete", "sub-scores", "children", "nested"
   - Example: "하위 점수 모두 알려줘" → is_hierarchical: true

5. hierarchy_depth: If hierarchical, suggest depth (1-5, default 3)
   - 1: Direct children only
   - 2-3: Include grandchildren (recommended for score queries)
   - 4-5: Full tree traversal

6. confidence: 0.0 to 1.0 (how confident you are about the analysis)
   - 0.0-0.5: Ambiguous query, needs clarification
   - 0.5-0.8: Reasonably clear
   - 0.8-1.0: Very clear intent

Return ONLY valid JSON (no markdown, no explanation):
{
  "start_entity": {"keywords": [""], "expected_labels": [""]},
  "target": {"expected_labels": [""], "expected_relationships": [""], "attributes": [""]},
  "query_type": "",
  "is_hierarchical": false,
  "hierarchy_depth": 0,
  "confidence": 0.0,
  "reasoning": ""
}`, labels, rels, query)
}

func (t *QueryAnalyzerTool) parseAnalysisResponse(content string) (*QueryAnalysisOutput, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var output QueryAnalysisOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return nil, err
	}

	return &output, nil
}

func (t *QueryAnalyzerTool) defaultAnalysis(query string) *QueryAnalysisOutput {
	// Simple keyword extraction as fallback
	words := strings.Fields(query)
	keywords := make([]string, 0)
	for _, w := range words {
		if len(w) > 2 && !isStopWord(w) {
			keywords = append(keywords, w)
		}
	}

	return &QueryAnalysisOutput{
		StartEntity: StartEntityInfo{
			Keywords:       keywords,
			ExpectedLabels: []string{},
		},
		Target: TargetInfo{
			ExpectedLabels: []string{},
			ExpectedRels:   []string{},
			Attributes:     []string{},
		},
		QueryType:  "factual",
		Confidence: 0.3,
		Reasoning:  "Fallback: simple keyword extraction",
	}
}

// NeedsClarification checks if the analysis result requires user clarification
func NeedsClarification(analysis *QueryAnalysisOutput) bool {
	if analysis == nil {
		return true
	}
	// No target labels extracted
	if len(analysis.Target.ExpectedLabels) == 0 {
		return true
	}
	// Low confidence
	if analysis.Confidence < 0.5 {
		return true
	}
	return false
}

// ClarificationReason returns the reason why clarification is needed
func ClarificationReason(analysis *QueryAnalysisOutput) string {
	if analysis == nil {
		return "Query analysis failed"
	}
	if len(analysis.Target.ExpectedLabels) == 0 {
		return "Could not determine what information you're looking for"
	}
	if analysis.Confidence < 0.5 {
		return "Query is ambiguous, multiple interpretations possible"
	}
	return ""
}

func isStopWord(word string) bool {
	stopWords := map[string]bool{
		// Korean
		"의": true, "에": true, "를": true, "을": true,
		"대해": true, "알려줘": true, "뭐야": true, "어때": true,
		"정보": true, "에서": true, "으로": true, "이": true,
		"가": true, "은": true, "는": true, "좀": true,
		// English
		"the": true, "a": true, "an": true, "is": true,
		"are": true, "what": true, "about": true, "tell": true,
		"me": true, "please": true, "can": true, "you": true,
	}
	return stopWords[word]
}

// Ensure QueryAnalyzerTool implements the required interfaces
var (
	_ tool.BaseTool      = (*QueryAnalyzerTool)(nil)
	_ tool.InvokableTool = (*QueryAnalyzerTool)(nil)
)
