// Package tools provides the comparison analyzer tool for detecting and decomposing
// multi-entity comparison queries.
//
// # Overview
//
// The ComparisonAnalyzerTool uses an LLM to analyze user queries and detect comparison
// patterns. It identifies:
//   - Whether the query is a comparison query
//   - The entities being compared
//   - The aspect being compared
//   - The analysis goal (gap analysis, advantage analysis, etc.)
//
// # Usage
//
//	analyzer := NewComparisonAnalyzerTool(chatModel)
//	result, err := analyzer.Analyze(ctx, "Compare POLO and Tucson performance scores")
//	if result.IsComparison {
//	    // Handle comparison query
//	}
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ComparisonAnalyzerTool detects and decomposes comparison queries using an LLM.
type ComparisonAnalyzerTool struct {
	chatModel model.ChatModel
}

// NewComparisonAnalyzerTool creates a new comparison analyzer tool.
func NewComparisonAnalyzerTool(chatModel model.ChatModel) *ComparisonAnalyzerTool {
	return &ComparisonAnalyzerTool{
		chatModel: chatModel,
	}
}

// ComparisonAnalysisOutput represents the result of comparison query analysis.
type ComparisonAnalysisOutput struct {
	// IsComparison indicates if the query is a comparison query
	IsComparison bool `json:"is_comparison"`

	// Entities contains the detected entities to compare
	Entities []ComparisonEntityInfo `json:"entities"`

	// CompareAspect describes what is being compared
	CompareAspect string `json:"compare_aspect"`

	// AnalysisGoal specifies the type of analysis requested
	AnalysisGoal string `json:"analysis_goal"`

	// SubQueries contains the decomposed sub-queries
	SubQueries []ComparisonSubQuery `json:"sub_queries"`

	// DependencyOrder defines the execution order
	// Each inner slice contains indices that can run in parallel
	DependencyOrder [][]int `json:"dependency_order"`

	// Confidence is the analysis confidence score (0.0-1.0)
	Confidence float64 `json:"confidence"`

	// Reasoning explains the analysis
	Reasoning string `json:"reasoning"`
}

// ComparisonEntityInfo contains information about an entity in a comparison.
type ComparisonEntityInfo struct {
	// Name is the entity name as it appears in the query
	Name string `json:"name"`

	// Keywords are alternative search terms for the entity
	Keywords []string `json:"keywords"`

	// ExpectedLabels are the expected Neo4j node labels
	ExpectedLabels []string `json:"expected_labels"`

	// Role is the entity's role in the comparison
	Role string `json:"role"`
}

// ComparisonSubQuery represents a decomposed sub-query.
type ComparisonSubQuery struct {
	// Index is the sub-query index
	Index int `json:"index"`

	// EntityIndex is the index of the entity this sub-query is for (-1 for comparison step)
	EntityIndex int `json:"entity_index"`

	// QueryText is the sub-query text
	QueryText string `json:"query_text"`

	// Purpose describes what this sub-query does
	Purpose string `json:"purpose"`

	// TargetLabels are the target Neo4j labels to search for
	TargetLabels []string `json:"target_labels"`

	// DependsOn lists indices of sub-queries this one depends on
	DependsOn []int `json:"depends_on"`
}

// Comparison indicators for detection
var comparisonIndicatorsKorean = []string{
	"와", "과", "비교", "차이", "보완", "대비", "VS", "vs",
	"대비해", "비교해", "비교해서", "차이점", "대조",
}

var comparisonIndicatorsEnglish = []string{
	"compare", "vs", "versus", "difference", "against",
	"comparison", "compared to", "compared with",
}

// Analysis goal patterns
var analysisGoalPatterns = map[string][]string{
	"gap_analysis": {
		"보완해야 할", "보완할", "개선해야 할", "개선할", "부족한",
		"약점", "단점", "뒤처지는", "낮은", "열등한",
		"improve", "weakness", "need to", "lacking", "inferior",
	},
	"advantage_analysis": {
		"장점", "강점", "우수한", "뛰어난", "앞서는", "높은",
		"advantage", "strength", "superior", "better", "excellent",
	},
	"difference_analysis": {
		"차이점", "다른점", "다른 점", "차이가",
		"difference", "different", "differ", "distinction",
	},
	"similarity_analysis": {
		"공통점", "같은점", "같은 점", "유사한", "비슷한",
		"similarity", "similar", "common", "same", "alike",
	},
}

// Analyze detects if a query is a comparison and decomposes it.
func (t *ComparisonAnalyzerTool) Analyze(ctx context.Context, query string) (*ComparisonAnalysisOutput, error) {
	// Quick pre-check for comparison indicators
	if !t.hasComparisonIndicator(query) {
		return &ComparisonAnalysisOutput{
			IsComparison: false,
			Confidence:   0.9,
			Reasoning:    "No comparison indicators detected in query",
		}, nil
	}

	// Use LLM for detailed analysis
	prompt := t.buildPrompt(query)

	messages := []*schema.Message{
		schema.SystemMessage(comparisonAnalyzerSystemPrompt),
		schema.UserMessage(prompt),
	}

	response, err := t.chatModel.Generate(ctx, messages)
	if err != nil {
		// Fall back to rule-based analysis on LLM failure
		return t.ruleBasedAnalysis(query), nil
	}

	return t.parseResponse(response.Content, query)
}

// hasComparisonIndicator checks if the query contains any comparison indicators.
func (t *ComparisonAnalyzerTool) hasComparisonIndicator(query string) bool {
	lowerQuery := strings.ToLower(query)

	for _, indicator := range comparisonIndicatorsKorean {
		if strings.Contains(query, indicator) {
			return true
		}
	}

	for _, indicator := range comparisonIndicatorsEnglish {
		if strings.Contains(lowerQuery, indicator) {
			return true
		}
	}

	return false
}

// buildPrompt creates the analysis prompt for the LLM.
func (t *ComparisonAnalyzerTool) buildPrompt(query string) string {
	return fmt.Sprintf(`Analyze this query for multi-entity comparison:

User Query: %s

Instructions:
1. Determine if this is a comparison query (comparing 2+ entities)
2. Extract the entities being compared
3. Identify the aspect being compared
4. Determine the analysis goal

Comparison indicators:
- Korean: "와", "과", "비교", "차이", "보완", "대비", "VS"
- English: "compare", "vs", "versus", "difference", "against"

Analysis goals:
- "보완해야 할 부분", "개선점", "부족한" -> gap_analysis (find weaknesses)
- "장점", "강점", "우수한" -> advantage_analysis (find strengths)
- "차이점", "다른점" -> difference_analysis (find differences)
- "공통점", "같은점" -> similarity_analysis (find similarities)

Entity roles:
- "reference": The baseline entity for comparison (usually mentioned first, or the one being compared TO)
- "subject": The entity being analyzed (usually the one that needs improvement or is being evaluated)

Return ONLY valid JSON in this exact format:
{
  "is_comparison": true,
  "entities": [
    {"name": "EntityName1", "keywords": ["keyword1"], "expected_labels": ["Vehicle"], "role": "reference"},
    {"name": "EntityName2", "keywords": ["keyword2"], "expected_labels": ["Vehicle"], "role": "subject"}
  ],
  "compare_aspect": "성능 평가 점수",
  "analysis_goal": "gap_analysis",
  "sub_queries": [
    {"index": 0, "entity_index": 0, "query_text": "EntityName1 성능 점수", "purpose": "retrieve_scores", "target_labels": ["PerformanceTotalScore"], "depends_on": []},
    {"index": 1, "entity_index": 1, "query_text": "EntityName2 성능 점수", "purpose": "retrieve_scores", "target_labels": ["PerformanceTotalScore"], "depends_on": []}
  ],
  "dependency_order": [[0, 1]],
  "confidence": 0.95,
  "reasoning": "Query compares two entities with gap analysis goal"
}`, query)
}

// parseResponse parses the LLM response into ComparisonAnalysisOutput.
func (t *ComparisonAnalyzerTool) parseResponse(content string, originalQuery string) (*ComparisonAnalysisOutput, error) {
	// Extract JSON from response (handle markdown code blocks)
	jsonStr := extractJSON(content)

	var output ComparisonAnalysisOutput
	if err := json.Unmarshal([]byte(jsonStr), &output); err != nil {
		// Fall back to rule-based analysis on parse failure
		return t.ruleBasedAnalysis(originalQuery), nil
	}

	// Validate and enrich the output
	t.enrichOutput(&output, originalQuery)

	return &output, nil
}

// extractJSON extracts JSON from a string that may contain markdown code blocks.
func extractJSON(content string) string {
	// Try to find JSON in code blocks first
	codeBlockPattern := regexp.MustCompile("```(?:json)?\\s*\\n?([\\s\\S]*?)\\n?```")
	matches := codeBlockPattern.FindStringSubmatch(content)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}

	// Try to find raw JSON
	jsonPattern := regexp.MustCompile(`\{[\s\S]*\}`)
	match := jsonPattern.FindString(content)
	if match != "" {
		return match
	}

	return content
}

// enrichOutput validates and enriches the analysis output.
func (t *ComparisonAnalyzerTool) enrichOutput(output *ComparisonAnalysisOutput, query string) {
	// Ensure at least 2 entities for comparison
	if len(output.Entities) < 2 {
		output.IsComparison = false
		output.Reasoning = "Less than 2 entities detected"
		return
	}

	// Set default roles if not specified
	for i := range output.Entities {
		if output.Entities[i].Role == "" {
			if i == 0 {
				output.Entities[i].Role = "reference"
			} else {
				output.Entities[i].Role = "subject"
			}
		}
	}

	// Detect analysis goal if not set
	if output.AnalysisGoal == "" {
		output.AnalysisGoal = t.detectAnalysisGoal(query)
	}

	// Generate sub-queries if not provided
	if len(output.SubQueries) == 0 {
		output.SubQueries = t.generateSubQueries(output)
	}

	// Generate dependency order if not provided
	if len(output.DependencyOrder) == 0 {
		output.DependencyOrder = t.generateDependencyOrder(output)
	}
}

// detectAnalysisGoal detects the analysis goal from the query.
func (t *ComparisonAnalyzerTool) detectAnalysisGoal(query string) string {
	lowerQuery := strings.ToLower(query)

	for goal, patterns := range analysisGoalPatterns {
		for _, pattern := range patterns {
			if strings.Contains(query, pattern) || strings.Contains(lowerQuery, pattern) {
				return goal
			}
		}
	}

	// Default to difference analysis
	return "difference_analysis"
}

// generateSubQueries generates sub-queries for each entity.
func (t *ComparisonAnalyzerTool) generateSubQueries(output *ComparisonAnalysisOutput) []ComparisonSubQuery {
	subQueries := make([]ComparisonSubQuery, len(output.Entities))

	for i, entity := range output.Entities {
		queryText := entity.Name
		if output.CompareAspect != "" {
			queryText = fmt.Sprintf("%s %s", entity.Name, output.CompareAspect)
		}

		subQueries[i] = ComparisonSubQuery{
			Index:        i,
			EntityIndex:  i,
			QueryText:    queryText,
			Purpose:      "retrieve_scores",
			TargetLabels: entity.ExpectedLabels,
			DependsOn:    []int{},
		}
	}

	return subQueries
}

// generateDependencyOrder generates the dependency order for sub-queries.
func (t *ComparisonAnalyzerTool) generateDependencyOrder(output *ComparisonAnalysisOutput) [][]int {
	// All entity retrievals can run in parallel
	parallelIndices := make([]int, len(output.Entities))
	for i := range output.Entities {
		parallelIndices[i] = i
	}

	return [][]int{parallelIndices}
}

// ruleBasedAnalysis performs simple rule-based analysis without LLM.
func (t *ComparisonAnalyzerTool) ruleBasedAnalysis(query string) *ComparisonAnalysisOutput {
	output := &ComparisonAnalysisOutput{
		IsComparison: false,
		Confidence:   0.5,
		Reasoning:    "Rule-based analysis (LLM fallback)",
	}

	// Try to extract entities using simple patterns
	entities := t.extractEntitiesRuleBased(query)
	if len(entities) < 2 {
		return output
	}

	output.IsComparison = true
	output.Entities = entities
	output.AnalysisGoal = t.detectAnalysisGoal(query)
	output.CompareAspect = t.extractCompareAspect(query)
	output.SubQueries = t.generateSubQueries(output)
	output.DependencyOrder = t.generateDependencyOrder(output)
	output.Confidence = 0.7

	return output
}

// extractEntitiesRuleBased extracts entities using pattern matching.
func (t *ComparisonAnalyzerTool) extractEntitiesRuleBased(query string) []ComparisonEntityInfo {
	var entities []ComparisonEntityInfo

	// Pattern for "A와 B", "A과 B"
	koreanPattern := regexp.MustCompile(`([가-힣A-Za-z0-9]+)\s*(와|과)\s*([가-힣A-Za-z0-9]+)`)
	matches := koreanPattern.FindStringSubmatch(query)
	if len(matches) >= 4 {
		entities = append(entities, ComparisonEntityInfo{
			Name:           matches[1],
			Keywords:       []string{matches[1]},
			ExpectedLabels: []string{"Vehicle"},
			Role:           "reference",
		})
		entities = append(entities, ComparisonEntityInfo{
			Name:           matches[3],
			Keywords:       []string{matches[3]},
			ExpectedLabels: []string{"Vehicle"},
			Role:           "subject",
		})
	}

	// Pattern for "A vs B", "A VS B"
	if len(entities) == 0 {
		vsPattern := regexp.MustCompile(`([A-Za-z0-9가-힣]+)\s+(?:vs|VS|versus)\s+([A-Za-z0-9가-힣]+)`)
		matches := vsPattern.FindStringSubmatch(query)
		if len(matches) >= 3 {
			entities = append(entities, ComparisonEntityInfo{
				Name:           matches[1],
				Keywords:       []string{matches[1]},
				ExpectedLabels: []string{"Vehicle"},
				Role:           "reference",
			})
			entities = append(entities, ComparisonEntityInfo{
				Name:           matches[2],
				Keywords:       []string{matches[2]},
				ExpectedLabels: []string{"Vehicle"},
				Role:           "subject",
			})
		}
	}

	return entities
}

// extractCompareAspect extracts the aspect being compared from the query.
func (t *ComparisonAnalyzerTool) extractCompareAspect(query string) string {
	// Common aspects
	aspects := []string{
		"성능 평가 점수", "성능 점수", "성능", "점수",
		"가격", "스펙", "연비", "안전", "편의",
		"performance", "score", "price", "specs", "fuel efficiency",
	}

	for _, aspect := range aspects {
		if strings.Contains(strings.ToLower(query), strings.ToLower(aspect)) {
			return aspect
		}
	}

	return ""
}

// System prompt for the comparison analyzer
const comparisonAnalyzerSystemPrompt = `You are a query analyzer specialized in detecting multi-entity comparison queries.

Your task is to:
1. Determine if a query is comparing two or more entities
2. Extract the entities being compared
3. Identify what aspect is being compared
4. Determine the analysis goal (gap analysis, advantage analysis, difference analysis, similarity analysis)
5. Decompose the query into sub-queries that can be executed

Rules:
- A comparison query must have at least 2 distinct entities
- The "reference" entity is the baseline (usually mentioned first or the one being compared TO)
- The "subject" entity is the one being analyzed/improved
- For gap analysis queries like "A가 B보다 부족한 점", B is reference and A is subject
- Expected labels for vehicles should be ["Vehicle"]
- Target labels for performance scores should be ["PerformanceTotalScore"]

Return ONLY valid JSON with no additional text or explanation.`
