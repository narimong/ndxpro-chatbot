package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-chatbot/api/service/db"

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
	QueryType      string          `json:"query_type"`      // factual, comparison, list, aggregation, exploration, conditional
	SearchStrategy string          `json:"search_strategy"` // exact, fuzzy, label_filtered, generic, label_query, conditional_traversal
	IsHierarchical bool            `json:"is_hierarchical"` // Whether user wants hierarchical/nested data
	HierarchyDepth int             `json:"hierarchy_depth"` // Suggested depth for hierarchical queries (1-5)
	Confidence     float64         `json:"confidence"`
	Reasoning      string          `json:"reasoning"`

	// Label listing query fields (for "list all X" queries)
	IsLabelListing bool            `json:"is_label_listing"` // Whether this is a "list all nodes of label X" query
	ListingLabels  []string        `json:"listing_labels"`   // Labels to list (e.g., ["CompetitorVehicle"])
	ListingLimit   int             `json:"listing_limit"`    // Max items to return (default: 50)
	ResolvedLabels []ResolvedLabel `json:"resolved_labels"`  // Semantic term to label mappings

	// Conditional query fields (for "Honda가 생산하는 차량" type queries)
	IsConditionalQuery bool                `json:"is_conditional_query"` // Whether this is a conditional/relational query
	ConditionalAnchor  *ConditionalAnchor  `json:"conditional_anchor,omitempty"`
	ConditionalFilters []ConditionalFilter `json:"conditional_filters,omitempty"`
}

// ConditionalAnchor describes the anchor entity for conditional queries
// Example: "Honda가 생산하는" -> anchor=Honda, condition=produces
type ConditionalAnchor struct {
	Keywords       []string `json:"keywords"`        // ["Honda"]
	ExpectedLabels []string `json:"expected_labels"` // ["Manufacturer"]
	ConditionType  string   `json:"condition_type"`  // "produces", "belongs_to", "has_property"
}

// ConditionalFilter describes a filter condition (property or relationship-based)
type ConditionalFilter struct {
	FilterType  string `json:"filter_type"`  // "relationship", "property"
	Attribute   string `json:"attribute"`    // "segment", "연식", "엔진"
	Operator    string `json:"operator"`     // "equals", "contains", "greater_than"
	Value       any    `json:"value"`        // Filter value
	TargetLabel string `json:"target_label"` // Target label if relationship filter
}

// ResolvedLabel represents a semantic term mapped to a Neo4j label
type ResolvedLabel struct {
	OriginalTerm  string  `json:"original_term"`  // e.g., "경쟁차"
	ResolvedLabel string  `json:"resolved_label"` // e.g., "CompetitorVehicle"
	Confidence    float64 `json:"confidence"`     // 0.0-1.0
	Source        string  `json:"source"`         // "static_mapping", "llm_inference"
}

// StartEntityInfo contains information about the starting entity
type StartEntityInfo struct {
	Keywords       []string `json:"keywords"`
	ExpectedLabels []string `json:"expected_labels"`
	IsExactName    bool     `json:"is_exact_name"` // true if user mentioned a specific entity name
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

	// Apply rule-based conditional query detection as post-processing
	// This ensures conditional queries are detected even when LLM doesn't recognize them
	// (since LLM prompt doesn't include is_conditional_query field)
	applyConditionalQueryDetection(query, &output)

	return &output, nil
}

func (t *QueryAnalyzerTool) buildAnalysisPrompt(query string) string {
	labels := strings.Join(t.schemaInfo.Labels, ", ")
	rels := strings.Join(t.schemaInfo.Relationships, ", ")

	return fmt.Sprintf(`Analyze this query for a vehicle knowledge graph.

Available Node Labels: %s
Available Relationships: %s

## Semantic Term to Label Mapping (IMPORTANT):
- 경쟁차/경쟁차량/경쟁 차량/competitor → CompetitorVehicle
- 유사차/유사 차량/similar → SimilarVehicle
- 차량/vehicle → Vehicle
- 엔진/engine → Engine
- 변속기/transmission → Transmission
- 성능점수/성능평가/performance → PerformanceTotalScore
- 안전점수/safety → SafetyScore
- 연비/fuel efficiency → FuelEfficiency

User Query: %s

Extract:
1. start_entity: Main entity to search for
   - keywords: Search terms (exact entity name if mentioned, e.g., "OPENING EQUIPMENT-REAR DOOR", "POLO", "NE2")
   - expected_labels: Most likely node types. If entity is generic or unknown, use empty array []
   - is_exact_name: true if user mentioned a specific entity name (vs general category)

2. target: Information the user wants
   - expected_labels: Target node types (e.g., ["PerformanceTotalScore", "Engine"]). Empty if just exploring
   - expected_relationships: Relationships to follow (e.g., ["hasPerformanceScore", "hasEngine"])
   - attributes: Specific properties wanted (e.g., ["score", "power"])

3. query_type: factual, comparison, list, aggregation, or exploration
   - "list" = user wants to list ALL nodes of a type (e.g., "모든 경쟁차 알려줘", "엔진 목록")
   - "exploration" = user wants to see what's connected to an entity

4. search_strategy: How to search for the entity
   - "exact": Search for exact entity name match (use when specific name is mentioned)
   - "fuzzy": Allow partial/similar matches (use for potentially misspelled names)
   - "label_filtered": Search within specific label types (use when entity type is known)
   - "generic": Search across all indexed nodes (use when entity type is unknown)
   - "label_query": List all nodes of a label type (use for "list all X" queries)

5. is_hierarchical: true if user is asking for nested/hierarchical data
   - Hierarchical keywords: "하위", "세부", "모두", "전체", "상세", "연결된", "관련", "포함"
   - English: "breakdown", "details", "all", "complete", "sub-scores", "children", "nested"

6. hierarchy_depth: If hierarchical, suggest depth (1-5, default 3)

7. confidence: 0.0 to 1.0 (how confident you are about the analysis)
   - 0.0-0.5: Ambiguous query, needs clarification
   - 0.5-0.8: Reasonably clear
   - 0.8-1.0: Very clear intent

8. is_label_listing: true if user wants to list ALL nodes of a specific label type
   - Listing keywords: "모든", "전체", "전부", "목록", "리스트", "LIST", "all", "every"
   - Example: "모든 경쟁차 알려줘" → is_label_listing: true

9. listing_labels: If is_label_listing is true, which labels to list
   - Map Korean terms to Neo4j labels using the mapping above
   - Example: "경쟁차" → ["CompetitorVehicle"]

10. listing_limit: Max items to return (default: 50, max: 200)

11. resolved_labels: Array of semantic term mappings found
   - original_term: The Korean/English term found in query
   - resolved_label: The mapped Neo4j label
   - confidence: 0.95 for exact matches
   - source: "llm_inference"

Return ONLY valid JSON (no markdown, no explanation):
{
  "start_entity": {"keywords": [""], "expected_labels": [], "is_exact_name": false},
  "target": {"expected_labels": [], "expected_relationships": [], "attributes": []},
  "query_type": "factual",
  "search_strategy": "generic",
  "is_hierarchical": false,
  "hierarchy_depth": 0,
  "confidence": 0.0,
  "reasoning": "",
  "is_label_listing": false,
  "listing_labels": [],
  "listing_limit": 50,
  "resolved_labels": []
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
	// Step 1: Check for conditional query (e.g., "Honda가 생산하는 차량")
	isConditional, anchor, filters := detectConditionalQuery(query)

	// Step 2: Check for label listing (used as target label hint for compound queries)
	isListing, resolvedLabels := detectLabelListingQuery(query)

	// Compound query: conditional + label target (e.g., "Honda가 생산한 경쟁차 List")
	// Conditional query takes priority, but uses resolved labels as target hint
	if isConditional && anchor != nil {
		targetLabels := []string{"CompetitorVehicle", "Vehicle"} // Default target

		// Use resolved labels from listing detection as target hint
		if len(resolvedLabels) > 0 {
			targetLabels = make([]string, 0, len(resolvedLabels))
			for _, rl := range resolvedLabels {
				targetLabels = append(targetLabels, rl.ResolvedLabel)
			}
		}

		return &QueryAnalysisOutput{
			StartEntity: StartEntityInfo{
				Keywords:       anchor.Keywords,
				ExpectedLabels: anchor.ExpectedLabels,
				IsExactName:    true,
			},
			Target: TargetInfo{
				ExpectedLabels: targetLabels, // Dynamic target labels from resolved labels
				ExpectedRels:   []string{},
				Attributes:     []string{},
			},
			QueryType:          "conditional",
			SearchStrategy:     "conditional_traversal",
			IsConditionalQuery: true,
			ConditionalAnchor:  anchor,
			ConditionalFilters: filters,
			// IsLabelListing stays false - conditional query takes priority
			ResolvedLabels: resolvedLabels, // Include resolved labels for reference
			Confidence:     0.85,
			Reasoning: fmt.Sprintf("Rule-based: conditional query anchor=%v, condition=%s, target_labels=%v",
				anchor.Keywords, anchor.ConditionType, targetLabels),
		}
	}

	// Step 3: Pure label listing query (no conditional pattern)

	if isListing && len(resolvedLabels) > 0 {
		// This is a label listing query
		listingLabels := make([]string, 0, len(resolvedLabels))
		for _, rl := range resolvedLabels {
			listingLabels = append(listingLabels, rl.ResolvedLabel)
		}

		return &QueryAnalysisOutput{
			StartEntity: StartEntityInfo{
				Keywords:       []string{},
				ExpectedLabels: listingLabels,
				IsExactName:    false,
			},
			Target: TargetInfo{
				ExpectedLabels: listingLabels,
				ExpectedRels:   []string{},
				Attributes:     []string{},
			},
			QueryType:      "list",
			SearchStrategy: "label_query",
			IsLabelListing: true,
			ListingLabels:  listingLabels,
			ListingLimit:   50,
			ResolvedLabels: resolvedLabels,
			Confidence:     0.8, // Higher confidence for rule-based detection
			Reasoning:      "Rule-based detection: label listing query with semantic term mapping",
		}
	}

	// Step 3: Check if it's a listing query but without recognized labels
	if isListing {
		// Listing intent detected but no labels resolved
		return &QueryAnalysisOutput{
			StartEntity: StartEntityInfo{
				Keywords:       extractKeywordsFromQuery(query),
				ExpectedLabels: []string{},
				IsExactName:    false,
			},
			Target: TargetInfo{
				ExpectedLabels: []string{},
				ExpectedRels:   []string{},
				Attributes:     []string{},
			},
			QueryType:      "list",
			SearchStrategy: "label_query",
			IsLabelListing: true,
			ListingLabels:  []string{}, // Empty - needs clarification
			ListingLimit:   50,
			ResolvedLabels: []ResolvedLabel{},
			Confidence:     0.5, // Lower confidence - needs clarification
			Reasoning:      "Listing intent detected but target label unknown - needs clarification",
		}
	}

	// Step 4: Standard keyword extraction fallback
	keywords := extractKeywordsFromQuery(query)

	// Check if query contains what looks like a specific entity name
	// (e.g., all caps, hyphenated, quoted)
	isExactName := containsExactEntityName(query)
	searchStrategy := "generic"
	if isExactName {
		searchStrategy = "exact"
	}

	return &QueryAnalysisOutput{
		StartEntity: StartEntityInfo{
			Keywords:       keywords,
			ExpectedLabels: []string{},
			IsExactName:    isExactName,
		},
		Target: TargetInfo{
			ExpectedLabels: []string{},
			ExpectedRels:   []string{},
			Attributes:     []string{},
		},
		QueryType:      "exploration", // Default to exploration for generic queries
		SearchStrategy: searchStrategy,
		Confidence:     0.3,
		Reasoning:      "Fallback: simple keyword extraction with generic search",
	}
}

// Listing keywords in Korean and English
var listingKeywords = []string{
	"모든", "전체", "전부", "목록", "리스트", "LIST", "list",
	"all", "every", "show all", "list all", "있어", "알려줘",
}

// =============================================================================
// Conditional Query Detection (탐색 우선 조건부 쿼리 감지)
// =============================================================================

// conditionalPatterns maps semantic condition types to Korean/English patterns
var conditionalPatterns = map[string][]string{
	"produces":    {"생산하는", "만드는", "제조하는", "제조한", "제조했", "만든", "생산한", "생산했", "만들", "만들었", "produces", "produced", "made by", "manufactured by"},
	"belongs_to":  {"세그먼트", "차급", "segment", "category"},
	"has_engine":  {"엔진이", "엔진을", "엔진의", "engine"},
	"model_year":  {"년식", "연식", "year"},
	"has_trim":    {"트림", "trim"},
}

// conditionToExpectedLabels maps condition types to expected anchor labels
var conditionToExpectedLabels = map[string][]string{
	"produces":   {"Manufacturer"},
	"belongs_to": {"Segment"},
	"has_engine": {"Engine"},
	"model_year": {}, // Property filter, not relationship-based
	"has_trim":   {}, // Property filter
}

// detectConditionalQuery checks if query has conditional pattern
// Returns (isConditional, anchor, filters)
func detectConditionalQuery(query string) (bool, *ConditionalAnchor, []ConditionalFilter) {
	queryLower := strings.ToLower(query)

	// Check for conditional patterns
	for condType, patterns := range conditionalPatterns {
		for _, pattern := range patterns {
			patternLower := strings.ToLower(pattern)
			if strings.Contains(query, pattern) || strings.Contains(queryLower, patternLower) {
				// Found conditional pattern
				anchor := extractConditionalAnchor(query, pattern, condType)
				filters := extractConditionalFilters(query)

				if anchor != nil {
					return true, anchor, filters
				}
			}
		}
	}

	return false, nil, nil
}

// extractConditionalAnchor extracts the anchor entity from a conditional query
func extractConditionalAnchor(query string, pattern string, condType string) *ConditionalAnchor {
	// Find position of the pattern in query
	idx := strings.Index(query, pattern)
	if idx <= 0 {
		// Try case-insensitive
		idx = strings.Index(strings.ToLower(query), strings.ToLower(pattern))
		if idx <= 0 {
			return nil
		}
	}

	// Extract text before the pattern
	beforePattern := strings.TrimSpace(query[:idx])
	words := strings.Fields(beforePattern)
	if len(words) == 0 {
		return nil
	}

	// Last word before pattern is likely the anchor
	anchorKeyword := words[len(words)-1]

	// Remove Korean particles
	anchorKeyword = removeKoreanParticles(anchorKeyword)

	if anchorKeyword == "" {
		return nil
	}

	return &ConditionalAnchor{
		Keywords:       []string{anchorKeyword},
		ExpectedLabels: conditionToExpectedLabels[condType],
		ConditionType:  condType,
	}
}

// removeKoreanParticles removes common Korean grammatical particles from a word
func removeKoreanParticles(word string) string {
	particles := []string{"가", "이", "의", "에서", "에", "를", "을", "는", "은", "로", "으로"}
	result := word
	for _, p := range particles {
		if strings.HasSuffix(result, p) {
			result = strings.TrimSuffix(result, p)
		}
	}
	return result
}

// extractConditionalFilters extracts property filters from query
func extractConditionalFilters(query string) []ConditionalFilter {
	filters := make([]ConditionalFilter, 0)

	// Check for year filter (e.g., "2024년식")
	if yearFilter := extractYearFilter(query); yearFilter != nil {
		filters = append(filters, *yearFilter)
	}

	// Check for segment filter
	if segmentFilter := extractSegmentFilter(query); segmentFilter != nil {
		filters = append(filters, *segmentFilter)
	}

	return filters
}

// extractYearFilter extracts year filter from query
func extractYearFilter(query string) *ConditionalFilter {
	// Pattern: "2024년식", "2023년", "24년식" etc.
	yearPatterns := []string{"년식", "년"}
	for _, pattern := range yearPatterns {
		idx := strings.Index(query, pattern)
		if idx > 0 {
			// Look for number before the pattern
			beforePattern := query[:idx]
			words := strings.Fields(beforePattern)
			if len(words) > 0 {
				lastWord := words[len(words)-1]
				// Try to extract year
				year := extractYear(lastWord)
				if year > 0 {
					return &ConditionalFilter{
						FilterType: "property",
						Attribute:  "연식",
						Operator:   "equals",
						Value:      year,
					}
				}
			}
		}
	}
	return nil
}

// extractYear extracts a year from a string
func extractYear(s string) int {
	// Remove non-digit characters
	digits := ""
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}

	if len(digits) == 0 {
		return 0
	}

	// Parse as integer
	var year int
	fmt.Sscanf(digits, "%d", &year)

	// Handle 2-digit years
	if year >= 20 && year <= 99 {
		year += 2000
	} else if year >= 0 && year <= 19 {
		year += 2000
	}

	// Validate year range
	if year >= 2000 && year <= 2030 {
		return year
	}

	return 0
}

// extractSegmentFilter extracts segment filter from query
func extractSegmentFilter(query string) *ConditionalFilter {
	segments := map[string]string{
		"compact": "compact",
		"middle":  "middle",
		"small":   "small",
		"large":   "large",
		"suv":     "suv",
		"컴팩트":    "compact",
		"중형":     "middle",
		"소형":     "small",
		"대형":     "large",
	}

	queryLower := strings.ToLower(query)
	for keyword, segment := range segments {
		if strings.Contains(queryLower, keyword) {
			return &ConditionalFilter{
				FilterType:  "relationship",
				Attribute:   "segment",
				Operator:    "equals",
				Value:       segment,
				TargetLabel: "Segment",
			}
		}
	}

	return nil
}

// detectLabelListingQuery checks if the query is asking for a list of all nodes of a specific label
// Returns (isListingQuery, resolvedLabels)
func detectLabelListingQuery(query string) (bool, []ResolvedLabel) {
	queryLower := strings.ToLower(query)

	// Check for listing keywords
	hasListingKeyword := false
	for _, kw := range listingKeywords {
		if strings.Contains(queryLower, strings.ToLower(kw)) {
			hasListingKeyword = true
			break
		}
	}

	if !hasListingKeyword {
		return false, nil
	}

	// Try to resolve semantic terms to labels
	resolvedLabels := resolveSemanticTermsInQuery(query)

	return true, resolvedLabels
}

// resolveSemanticTermsInQuery finds and resolves all semantic terms in a query
func resolveSemanticTermsInQuery(query string) []ResolvedLabel {
	resolved := make([]ResolvedLabel, 0)
	seen := make(map[string]bool)
	queryLower := strings.ToLower(query)

	// Try to match each mapping key in the query
	for term, label := range db.SemanticLabelMapping {
		termLower := strings.ToLower(term)
		if strings.Contains(queryLower, termLower) {
			if !seen[label] {
				resolved = append(resolved, ResolvedLabel{
					OriginalTerm:  term,
					ResolvedLabel: label,
					Confidence:    0.95,
					Source:        "static_mapping",
				})
				seen[label] = true
			}
		}
	}

	return resolved
}

// extractKeywordsFromQuery extracts keywords from query, filtering stop words
func extractKeywordsFromQuery(query string) []string {
	words := strings.Fields(query)
	keywords := make([]string, 0)
	for _, w := range words {
		if len(w) > 2 && !isStopWord(w) {
			keywords = append(keywords, w)
		}
	}
	return keywords
}

// containsExactEntityName checks if query contains what looks like a specific entity name
func containsExactEntityName(query string) bool {
	// Check for quoted text
	if strings.Contains(query, "\"") || strings.Contains(query, "'") {
		return true
	}
	// Check for hyphenated names (e.g., "OPENING EQUIPMENT-REAR DOOR")
	if strings.Contains(query, "-") {
		return true
	}
	// Check for words that are all uppercase and longer than 2 chars
	words := strings.Fields(query)
	for _, w := range words {
		if len(w) > 2 && w == strings.ToUpper(w) && !isStopWord(w) {
			return true
		}
	}
	return false
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

// applyConditionalQueryDetection applies rule-based conditional query detection
// as post-processing to LLM analysis results. This ensures conditional queries
// like "Honda가 생산한 경쟁차 List" are properly detected even when LLM doesn't
// recognize the conditional pattern (since LLM prompt doesn't include
// is_conditional_query field).
func applyConditionalQueryDetection(query string, output *QueryAnalysisOutput) {
	// Skip if LLM already detected this as a conditional query
	if output.IsConditionalQuery && output.ConditionalAnchor != nil {
		return
	}

	// Rule-based conditional query detection
	isConditional, anchor, filters := detectConditionalQuery(query)
	if !isConditional || anchor == nil {
		return
	}

	// Extract target label hints from label listing detection
	_, resolvedLabels := detectLabelListingQuery(query)

	// Set conditional query fields
	output.IsConditionalQuery = true
	output.ConditionalAnchor = anchor
	output.ConditionalFilters = filters

	// Determine target labels: use resolved labels if available, otherwise default
	targetLabels := []string{"CompetitorVehicle", "Vehicle"}
	if len(resolvedLabels) > 0 {
		targetLabels = make([]string, 0, len(resolvedLabels))
		for _, rl := range resolvedLabels {
			targetLabels = append(targetLabels, rl.ResolvedLabel)
		}
		output.ResolvedLabels = resolvedLabels
	}

	// Update target information
	output.Target.ExpectedLabels = targetLabels
	output.QueryType = "conditional"
	output.SearchStrategy = "conditional_traversal"

	// Conditional query takes priority over label listing
	output.IsLabelListing = false
	output.ListingLabels = nil
}

// Ensure QueryAnalyzerTool implements the required interfaces
var (
	_ tool.BaseTool      = (*QueryAnalyzerTool)(nil)
	_ tool.InvokableTool = (*QueryAnalyzerTool)(nil)
)
