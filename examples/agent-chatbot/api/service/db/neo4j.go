package db

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// QueryLoggerFunc is a callback for logging executed Cypher queries
type QueryLoggerFunc func(cypher string, params map[string]any, resultCount int, duration time.Duration, source string)

// Neo4jClient wraps the Neo4j driver for graph database operations
type Neo4jClient struct {
	driver        neo4j.DriverWithContext
	apocAvailable bool // Whether APOC procedures are available

	// Dynamic index cache
	indexedLabels   []string
	lastIndexUpdate time.Time
	indexMutex      sync.RWMutex
	indexCacheTTL   time.Duration

	// Dynamic label groups cache
	dynamicLabelGroups map[string][]string
	labelGroupsMutex   sync.RWMutex

	// Query logger for visualization
	queryLogger QueryLoggerFunc
	loggerMutex sync.RWMutex
}

// PathResult represents a shortest path result with linearized path chain
type PathResult struct {
	PathChain     string         `json:"path_chain"`     // "Vehicle[POLO] -[hasScore]-> Score[85]"
	EndNodeUUID   string         `json:"end_uuid"`
	EndNodeName   string         `json:"end_name"`
	EndNodeLabels []string       `json:"end_labels"`
	EndProperties map[string]any `json:"end_properties"` // Target node properties (essential for answers)
	Hops          int            `json:"hops"`
}

// NewNeo4jClient creates a new Neo4j client with the given connection parameters
func NewNeo4jClient(ctx context.Context, uri, username, password string) (*Neo4jClient, error) {
	driver, err := neo4j.NewDriverWithContext(
		uri,
		neo4j.BasicAuth(username, password, ""),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create neo4j driver: %w", err)
	}

	// Verify connectivity
	if err := driver.VerifyConnectivity(ctx); err != nil {
		driver.Close(ctx)
		return nil, fmt.Errorf("failed to verify neo4j connectivity: %w", err)
	}

	client := &Neo4jClient{
		driver:        driver,
		indexCacheTTL: 5 * time.Minute, // Cache labels for 5 minutes
	}

	// Check APOC availability
	client.apocAvailable = client.checkAPOCAvailable(ctx)

	return client, nil
}

// Close closes the Neo4j driver
func (c *Neo4jClient) Close(ctx context.Context) error {
	return c.driver.Close(ctx)
}

// SetQueryLogger sets a callback for logging executed Cypher queries
func (c *Neo4jClient) SetQueryLogger(logger QueryLoggerFunc) {
	c.loggerMutex.Lock()
	defer c.loggerMutex.Unlock()
	c.queryLogger = logger
}

// ClearQueryLogger removes the query logger
func (c *Neo4jClient) ClearQueryLogger() {
	c.loggerMutex.Lock()
	defer c.loggerMutex.Unlock()
	c.queryLogger = nil
}

// getQueryLogger returns the current query logger (thread-safe)
func (c *Neo4jClient) getQueryLogger() QueryLoggerFunc {
	c.loggerMutex.RLock()
	defer c.loggerMutex.RUnlock()
	return c.queryLogger
}

// ExecuteQuery executes a Cypher query and returns the results as a slice of maps
func (c *Neo4jClient) ExecuteQuery(ctx context.Context, cypher string, params map[string]any) ([]map[string]any, error) {
	return c.ExecuteQueryWithSource(ctx, cypher, params, "ExecuteQuery")
}

// ExecuteQueryWithSource executes a Cypher query with source tracking for visualization
func (c *Neo4jClient) ExecuteQueryWithSource(ctx context.Context, cypher string, params map[string]any, source string) ([]map[string]any, error) {
	startTime := time.Now()

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)

	result, err := session.Run(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}

	var records []map[string]any
	for result.Next(ctx) {
		record := result.Record()
		records = append(records, record.AsMap())
	}

	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("error iterating results: %w", err)
	}

	// Log query if logger is set
	if logger := c.getQueryLogger(); logger != nil {
		duration := time.Since(startTime)
		logger(cypher, params, len(records), duration, source)
	}

	return records, nil
}

// ExecuteWrite executes a write transaction with the given Cypher query
func (c *Neo4jClient) ExecuteWrite(ctx context.Context, cypher string, params map[string]any) error {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypher, params)
		return nil, err
	})

	if err != nil {
		return fmt.Errorf("failed to execute write: %w", err)
	}

	return nil
}

// LabelGroups defines related labels that should be searched together
// When searching for "Vehicle", we should also search "CompetitorVehicle" and "SimilarVehicle"
var LabelGroups = map[string][]string{
	"Vehicle":     {"Vehicle", "CompetitorVehicle", "SimilarVehicle"},
	"Performance": {"PerformanceTotalScore", "PerformanceAcceleration", "PerformanceBraking", "PerformanceHandling", "PerformanceRideComfort"},
	"Score":       {"SafetyScore", "FuelEfficiency", "PerformanceTotalScore"},
	"Engine":      {"Engine"},
	"Transmission": {"Transmission"},
}

// LabelDisplayNames maps Neo4j labels to user-friendly Korean names
var LabelDisplayNames = map[string]string{
	"Vehicle":                 "차량",
	"CompetitorVehicle":       "경쟁 차량",
	"SimilarVehicle":          "유사 차량",
	"Engine":                  "엔진",
	"Transmission":            "변속기",
	"Tire":                    "타이어",
	"PerformanceTotalScore":   "성능 종합 점수",
	"PerformanceAcceleration": "가속 성능",
	"PerformanceBraking":      "제동 성능",
	"PerformanceHandling":     "핸들링",
	"PerformanceRideComfort":  "승차감",
	"SafetyScore":             "안전 점수",
	"FuelEfficiency":          "연비",
	"Manufacturer":            "제조사",
	"Segment":                 "세그먼트",
	"Price":                   "가격",
	"Trim":                    "트림",
	"Part":                    "부품",
}

// SemanticLabelMapping maps Korean/English semantic terms to Neo4j labels
// This enables users to query using natural language terms instead of exact label names
var SemanticLabelMapping = map[string]string{
	// 경쟁차 관련
	"경쟁차":        "CompetitorVehicle",
	"경쟁 차량":     "CompetitorVehicle",
	"경쟁차량":      "CompetitorVehicle",
	"경쟁 모델":     "CompetitorVehicle",
	"경쟁모델":      "CompetitorVehicle",
	"competitor":   "CompetitorVehicle",
	"competitors":  "CompetitorVehicle",
	"rival":        "CompetitorVehicle",

	// 유사차 관련
	"유사차":        "SimilarVehicle",
	"유사 차량":     "SimilarVehicle",
	"유사차량":      "SimilarVehicle",
	"비슷한 차":     "SimilarVehicle",
	"similar":      "SimilarVehicle",

	// 차량 일반
	"차량":          "Vehicle",
	"차":            "Vehicle",
	"자동차":        "Vehicle",
	"vehicle":      "Vehicle",
	"vehicles":     "Vehicle",
	"car":          "Vehicle",
	"cars":         "Vehicle",

	// 엔진
	"엔진":          "Engine",
	"engine":       "Engine",
	"engines":      "Engine",
	"파워트레인":    "Engine",

	// 변속기
	"변속기":        "Transmission",
	"트랜스미션":    "Transmission",
	"transmission": "Transmission",
	"gearbox":      "Transmission",

	// 타이어
	"타이어":        "Tire",
	"tire":         "Tire",
	"tires":        "Tire",

	// 성능 점수
	"성능점수":      "PerformanceTotalScore",
	"성능 점수":     "PerformanceTotalScore",
	"성능평가":      "PerformanceTotalScore",
	"성능 평가":     "PerformanceTotalScore",
	"performance":  "PerformanceTotalScore",

	// 안전 점수
	"안전점수":      "SafetyScore",
	"안전 점수":     "SafetyScore",
	"안전성":        "SafetyScore",
	"safety":       "SafetyScore",

	// 연비
	"연비":          "FuelEfficiency",
	"fuel":         "FuelEfficiency",
	"efficiency":   "FuelEfficiency",

	// 제조사
	"제조사":        "Manufacturer",
	"브랜드":        "Manufacturer",
	"manufacturer": "Manufacturer",
	"brand":        "Manufacturer",

	// 세그먼트
	"세그먼트":      "Segment",
	"segment":      "Segment",
	"차급":          "Segment",

	// 트림
	"트림":          "Trim",
	"trim":         "Trim",
	"등급":          "Trim",

	// 부품
	"부품":          "Part",
	"part":         "Part",
	"parts":        "Part",
}

// ResolveSemanticTerm converts a semantic term (Korean/English) to Neo4j label
// Returns the label and a boolean indicating if a match was found
func ResolveSemanticTerm(term string) (string, bool) {
	// Normalize: lowercase for case-insensitive matching
	normalized := strings.ToLower(strings.TrimSpace(term))

	// Direct lookup
	if label, ok := SemanticLabelMapping[term]; ok {
		return label, true
	}

	// Case-insensitive lookup
	if label, ok := SemanticLabelMapping[normalized]; ok {
		return label, true
	}

	// Check if it's already a valid label
	for _, displayName := range LabelDisplayNames {
		if displayName == term {
			// Reverse lookup
			for label, name := range LabelDisplayNames {
				if name == term {
					return label, true
				}
			}
		}
	}

	return "", false
}

// ResolveSemanticTermsInQuery finds and resolves all semantic terms in a query
// Returns a slice of resolved labels
func ResolveSemanticTermsInQuery(query string) []string {
	resolved := make([]string, 0)
	seen := make(map[string]bool)

	// Try to match each mapping key in the query
	queryLower := strings.ToLower(query)
	for term, label := range SemanticLabelMapping {
		termLower := strings.ToLower(term)
		if strings.Contains(queryLower, termLower) {
			if !seen[label] {
				resolved = append(resolved, label)
				seen[label] = true
			}
		}
	}

	return resolved
}

// GetListableLabels returns labels that are suitable for listing queries
func GetListableLabels() []string {
	return []string{
		"Vehicle",
		"CompetitorVehicle",
		"SimilarVehicle",
		"Engine",
		"Transmission",
		"PerformanceTotalScore",
		"SafetyScore",
		"FuelEfficiency",
		"Manufacturer",
		"Segment",
	}
}

// GetRelatedLabels returns all labels in the same group as the given label
func GetRelatedLabels(label string) []string {
	// First, check if it's a group key
	if group, ok := LabelGroups[label]; ok {
		return group
	}
	// Check if the label is a member of any group
	for _, labels := range LabelGroups {
		for _, l := range labels {
			if l == label {
				return labels
			}
		}
	}
	// No group found, return the label itself
	return []string{label}
}

// DiscoverLabelGroups discovers related labels by analyzing suffix patterns and relationships
// This allows dynamic grouping without hardcoded label lists
func (c *Neo4jClient) DiscoverLabelGroups(ctx context.Context) (map[string][]string, error) {
	// Check cache first
	c.labelGroupsMutex.RLock()
	if c.dynamicLabelGroups != nil && len(c.dynamicLabelGroups) > 0 {
		groups := make(map[string][]string)
		for k, v := range c.dynamicLabelGroups {
			groups[k] = append([]string{}, v...)
		}
		c.labelGroupsMutex.RUnlock()
		return groups, nil
	}
	c.labelGroupsMutex.RUnlock()

	// Get all labels from schema
	schema, err := c.GetSchema(ctx, false, false)
	if err != nil {
		return nil, err
	}

	labels, ok := schema["labels"].([]any)
	if !ok {
		return LabelGroups, nil
	}

	// Convert to string slice
	allLabels := make([]string, 0, len(labels))
	for _, l := range labels {
		if label, ok := l.(string); ok {
			allLabels = append(allLabels, label)
		}
	}

	// Strategy 1: Group by common suffix patterns
	groups := c.groupByCommonSuffix(allLabels)

	// Merge with static groups (static groups take precedence)
	for k, v := range LabelGroups {
		groups[k] = v
	}

	// Cache the results
	c.labelGroupsMutex.Lock()
	c.dynamicLabelGroups = groups
	c.labelGroupsMutex.Unlock()

	return groups, nil
}

// groupByCommonSuffix groups labels by common suffix patterns
// e.g., "Vehicle", "CompetitorVehicle", "SimilarVehicle" -> group by "Vehicle"
func (c *Neo4jClient) groupByCommonSuffix(labels []string) map[string][]string {
	groups := make(map[string][]string)

	// Common suffixes to look for (in priority order)
	suffixes := []string{
		"Vehicle", "Score", "Total", "TotalScore",
		"Engine", "Transmission", "System",
		"Type", "Info", "Data", "Category",
	}

	// Track which labels have been grouped
	grouped := make(map[string]bool)

	for _, suffix := range suffixes {
		matchedLabels := make([]string, 0)
		for _, label := range labels {
			if strings.HasSuffix(label, suffix) || label == suffix {
				matchedLabels = append(matchedLabels, label)
			}
		}

		// If we found multiple labels with this suffix, create a group
		if len(matchedLabels) > 1 {
			groups[suffix] = matchedLabels
			for _, l := range matchedLabels {
				grouped[l] = true
			}
		}
	}

	return groups
}

// GetExpandedLabels returns all labels in the same group as the given label
// Uses dynamic discovery if available, falls back to static groups
func (c *Neo4jClient) GetExpandedLabels(ctx context.Context, label string) ([]string, error) {
	// First check static groups
	if group, ok := LabelGroups[label]; ok {
		return group, nil
	}

	// Try to discover dynamic groups
	dynamicGroups, err := c.DiscoverLabelGroups(ctx)
	if err != nil {
		return []string{label}, nil
	}

	// Check if label is in any group
	if group, ok := dynamicGroups[label]; ok {
		return group, nil
	}

	// Check if label is a member of any group
	for _, groupLabels := range dynamicGroups {
		for _, l := range groupLabels {
			if l == label {
				return groupLabels, nil
			}
		}
	}

	return []string{label}, nil
}

// RefreshLabelGroups clears the cached label groups to force rediscovery
func (c *Neo4jClient) RefreshLabelGroups() {
	c.labelGroupsMutex.Lock()
	c.dynamicLabelGroups = nil
	c.labelGroupsMutex.Unlock()
}

// GetLabelDisplayName returns the user-friendly display name for a label
func GetLabelDisplayName(label string) string {
	if name, ok := LabelDisplayNames[label]; ok {
		return name
	}
	return label
}

// EnsureFullTextIndex creates the full-text index if it doesn't exist
func (c *Neo4jClient) EnsureFullTextIndex(ctx context.Context) error {
	// Get all labels that have a 'name' property
	// IMPORTANT: Include all vehicle-related labels (Vehicle, CompetitorVehicle, SimilarVehicle)
	labels := []string{
		// Vehicle types (MUST include all vehicle variants)
		"Vehicle", "CompetitorVehicle", "SimilarVehicle",
		// Components
		"Engine", "Transmission", "Tire", "Trim", "Part",
		"System", "SubSystem",
		// Classifications
		"Manufacturer", "Segment", "VehicleType",
		"FuelType", "ElectrifiedType",
		// Performance & Scores
		"PerformanceTotalScore", "PerformanceAcceleration", "PerformanceBraking",
		"PerformanceHandling", "PerformanceRideComfort",
		"SafetyScore", "FuelEfficiency", "Price",
		// Others
		"Task", "Requirement", "Configuration",
	}

	// Build the label string for the index
	labelStr := ""
	for i, label := range labels {
		if i > 0 {
			labelStr += "|"
		}
		labelStr += label
	}

	// Create full-text index
	cypher := fmt.Sprintf(`
		CREATE FULLTEXT INDEX node_names IF NOT EXISTS
		FOR (n:%s)
		ON EACH [n.name]
	`, labelStr)

	return c.ExecuteWrite(ctx, cypher, nil)
}

// DiscoverLabelsWithNameProperty discovers all labels that have nodes with 'name' property
// This enables dynamic fulltext indexing for any node type
func (c *Neo4jClient) DiscoverLabelsWithNameProperty(ctx context.Context) ([]string, error) {
	// Check cache first
	c.indexMutex.RLock()
	if len(c.indexedLabels) > 0 && time.Since(c.lastIndexUpdate) < c.indexCacheTTL {
		labels := make([]string, len(c.indexedLabels))
		copy(labels, c.indexedLabels)
		c.indexMutex.RUnlock()
		return labels, nil
	}
	c.indexMutex.RUnlock()

	// Discover labels with name property
	cypher := `
		MATCH (n) WHERE n.name IS NOT NULL
		WITH DISTINCT labels(n) AS nodeLabels
		UNWIND nodeLabels AS label
		RETURN DISTINCT label ORDER BY label
	`

	results, err := c.ExecuteQuery(ctx, cypher, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to discover labels: %w", err)
	}

	labels := make([]string, 0, len(results))
	for _, r := range results {
		if label, ok := r["label"].(string); ok {
			labels = append(labels, label)
		}
	}

	// Update cache
	c.indexMutex.Lock()
	c.indexedLabels = labels
	c.lastIndexUpdate = time.Now()
	c.indexMutex.Unlock()

	return labels, nil
}

// EnsureDynamicFullTextIndex creates a full-text index for ALL nodes with 'name' property
// This enables searching for any entity type, not just predefined labels
func (c *Neo4jClient) EnsureDynamicFullTextIndex(ctx context.Context) error {
	// Discover all labels with name property
	labels, err := c.DiscoverLabelsWithNameProperty(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover labels: %w", err)
	}

	if len(labels) == 0 {
		return fmt.Errorf("no labels with 'name' property found in database")
	}

	// Build the label string for the index
	labelStr := strings.Join(labels, "|")

	// Drop existing index first (to rebuild with new labels)
	dropCypher := `DROP INDEX node_names IF EXISTS`
	if err := c.ExecuteWrite(ctx, dropCypher, nil); err != nil {
		// Ignore drop errors - index might not exist
	}

	// Create new index with all discovered labels
	createCypher := fmt.Sprintf(`
		CREATE FULLTEXT INDEX node_names IF NOT EXISTS
		FOR (n:%s)
		ON EACH [n.name]
	`, labelStr)

	if err := c.ExecuteWrite(ctx, createCypher, nil); err != nil {
		return fmt.Errorf("failed to create dynamic fulltext index: %w", err)
	}

	return nil
}

// GetIndexedLabels returns the list of labels that are indexed for fulltext search
func (c *Neo4jClient) GetIndexedLabels(ctx context.Context) ([]string, error) {
	return c.DiscoverLabelsWithNameProperty(ctx)
}

// RefreshFullTextIndex forces a refresh of the fulltext index with newly discovered labels
func (c *Neo4jClient) RefreshFullTextIndex(ctx context.Context) error {
	// Clear cache to force rediscovery
	c.indexMutex.Lock()
	c.indexedLabels = nil
	c.lastIndexUpdate = time.Time{}
	c.indexMutex.Unlock()

	return c.EnsureDynamicFullTextIndex(ctx)
}

// FullTextSearch performs a full-text search on node names
func (c *Neo4jClient) FullTextSearch(ctx context.Context, query string, labelFilter string, topK int) ([]map[string]any, error) {
	if topK <= 0 {
		topK = 20
	}

	var cypher string
	params := map[string]any{
		"query": query,
		"topK":  topK,
	}

	if labelFilter != "" {
		cypher = `
			CALL db.index.fulltext.queryNodes('node_names', $query)
			YIELD node, score
			WHERE $labelFilter IN labels(node)
			RETURN
				node.uuid AS uuid,
				labels(node) AS labels,
				node.name AS name,
				properties(node) AS properties,
				score
			ORDER BY score DESC
			LIMIT $topK
		`
		params["labelFilter"] = labelFilter
	} else {
		cypher = `
			CALL db.index.fulltext.queryNodes('node_names', $query)
			YIELD node, score
			RETURN
				node.uuid AS uuid,
				labels(node) AS labels,
				node.name AS name,
				properties(node) AS properties,
				score
			ORDER BY score DESC
			LIMIT $topK
		`
	}

	return c.ExecuteQuery(ctx, cypher, params)
}

// FullTextSearchWithLabels performs a full-text search filtering by multiple labels
// This enables searching across related label groups (e.g., Vehicle + CompetitorVehicle + SimilarVehicle)
func (c *Neo4jClient) FullTextSearchWithLabels(ctx context.Context, query string, labels []string, topK int) ([]map[string]any, error) {
	if topK <= 0 {
		topK = 20
	}

	var cypher string
	params := map[string]any{
		"query": query,
		"topK":  topK,
	}

	if len(labels) > 0 {
		cypher = `
			CALL db.index.fulltext.queryNodes('node_names', $query)
			YIELD node, score
			WHERE any(label IN labels(node) WHERE label IN $labelFilter)
			RETURN
				node.uuid AS uuid,
				labels(node) AS labels,
				node.name AS name,
				properties(node) AS properties,
				score
			ORDER BY score DESC
			LIMIT $topK
		`
		params["labelFilter"] = labels
	} else {
		// No label filter - search all indexed nodes
		cypher = `
			CALL db.index.fulltext.queryNodes('node_names', $query)
			YIELD node, score
			RETURN
				node.uuid AS uuid,
				labels(node) AS labels,
				node.name AS name,
				properties(node) AS properties,
				score
			ORDER BY score DESC
			LIMIT $topK
		`
	}

	return c.ExecuteQuery(ctx, cypher, params)
}

// FullTextSearchWithLabelGroup performs full-text search expanding the label to its related group
// e.g., searching with "Vehicle" label will automatically search Vehicle, CompetitorVehicle, SimilarVehicle
func (c *Neo4jClient) FullTextSearchWithLabelGroup(ctx context.Context, query string, labelHint string, topK int) ([]map[string]any, error) {
	labels := GetRelatedLabels(labelHint)
	return c.FullTextSearchWithLabels(ctx, query, labels, topK)
}

// GenericSearchResult represents a search result with relationship context
type GenericSearchResult struct {
	UUID         string            `json:"uuid"`
	Name         string            `json:"name"`
	Labels       []string          `json:"labels"`
	Properties   map[string]any    `json:"properties"`
	Score        float64           `json:"score"`
	Neighbors    []NeighborContext `json:"neighbors,omitempty"`
}

// NeighborContext represents contextual neighbor information for a node
type NeighborContext struct {
	Relationship   string   `json:"relationship"`
	Direction      string   `json:"direction"` // "outgoing" or "incoming"
	NeighborName   string   `json:"neighbor_name"`
	NeighborUUID   string   `json:"neighbor_uuid"`
	NeighborLabels []string `json:"neighbor_labels"`
}

// GenericFullTextSearch performs a label-agnostic search and returns results with relationship context
// This is the main search function for finding any entity type
func (c *Neo4jClient) GenericFullTextSearch(ctx context.Context, query string, topK int, includeContext bool) ([]GenericSearchResult, error) {
	if topK <= 0 {
		topK = 20
	}

	var cypher string
	params := map[string]any{
		"query": query,
		"topK":  topK,
	}

	if includeContext {
		// Search with neighbor context (more expensive but informative)
		cypher = `
			CALL db.index.fulltext.queryNodes('node_names', $query)
			YIELD node, score
			WITH node, score
			ORDER BY score DESC
			LIMIT $topK
			OPTIONAL MATCH (node)-[r]-(neighbor)
			WITH node, score,
				 collect(DISTINCT {
					 relationship: type(r),
					 direction: CASE WHEN startNode(r) = node THEN 'outgoing' ELSE 'incoming' END,
					 neighbor_name: neighbor.name,
					 neighbor_uuid: neighbor.uuid,
					 neighbor_labels: labels(neighbor)
				 })[0..5] AS neighbors
			RETURN
				node.uuid AS uuid,
				node.name AS name,
				labels(node) AS labels,
				properties(node) AS properties,
				score,
				neighbors
		`
	} else {
		// Simple search without context
		cypher = `
			CALL db.index.fulltext.queryNodes('node_names', $query)
			YIELD node, score
			RETURN
				node.uuid AS uuid,
				node.name AS name,
				labels(node) AS labels,
				properties(node) AS properties,
				score
			ORDER BY score DESC
			LIMIT $topK
		`
	}

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("generic fulltext search failed: %w", err)
	}

	searchResults := make([]GenericSearchResult, 0, len(results))
	for _, r := range results {
		result := GenericSearchResult{
			Properties: make(map[string]any),
		}

		if uuid, ok := r["uuid"].(string); ok {
			result.UUID = uuid
		}
		if name, ok := r["name"].(string); ok {
			result.Name = name
		}
		if score, ok := r["score"].(float64); ok {
			result.Score = score
		}
		if labels, ok := r["labels"].([]any); ok {
			result.Labels = make([]string, 0, len(labels))
			for _, l := range labels {
				if label, ok := l.(string); ok {
					result.Labels = append(result.Labels, label)
				}
			}
		}
		if props, ok := r["properties"].(map[string]any); ok {
			result.Properties = props
		}

		// Parse neighbors if available
		if neighbors, ok := r["neighbors"].([]any); ok {
			result.Neighbors = make([]NeighborContext, 0, len(neighbors))
			for _, n := range neighbors {
				if nMap, ok := n.(map[string]any); ok {
					neighbor := NeighborContext{}
					if rel, ok := nMap["relationship"].(string); ok {
						neighbor.Relationship = rel
					}
					if dir, ok := nMap["direction"].(string); ok {
						neighbor.Direction = dir
					}
					if name, ok := nMap["neighbor_name"].(string); ok {
						neighbor.NeighborName = name
					}
					if uuid, ok := nMap["neighbor_uuid"].(string); ok {
						neighbor.NeighborUUID = uuid
					}
					if labels, ok := nMap["neighbor_labels"].([]any); ok {
						neighbor.NeighborLabels = make([]string, 0, len(labels))
						for _, l := range labels {
							if label, ok := l.(string); ok {
								neighbor.NeighborLabels = append(neighbor.NeighborLabels, label)
							}
						}
					}
					result.Neighbors = append(result.Neighbors, neighbor)
				}
			}
		}

		searchResults = append(searchResults, result)
	}

	return searchResults, nil
}

// GenericFullTextSearchWithFallback attempts multiple search strategies
// Strategy 1: Exact search
// Strategy 2: Wildcard search (if exact returns no results)
// Strategy 3: Fuzzy search (if wildcard returns no results)
func (c *Neo4jClient) GenericFullTextSearchWithFallback(ctx context.Context, query string, topK int) ([]GenericSearchResult, error) {
	// Strategy 1: Try exact search first
	results, err := c.GenericFullTextSearch(ctx, query, topK, true)
	if err == nil && len(results) > 0 {
		return results, nil
	}

	// Strategy 2: Try wildcard search
	wildcardQuery := buildWildcardQuery(query)
	results, err = c.GenericFullTextSearch(ctx, wildcardQuery, topK, true)
	if err == nil && len(results) > 0 {
		return results, nil
	}

	// Strategy 3: Try fuzzy search
	fuzzyQuery := buildFuzzyQuery(query)
	results, err = c.GenericFullTextSearch(ctx, fuzzyQuery, topK, true)
	if err == nil && len(results) > 0 {
		return results, nil
	}

	// Return empty results if all strategies fail
	return []GenericSearchResult{}, nil
}

// buildWildcardQuery adds wildcards to search terms
func buildWildcardQuery(query string) string {
	// Handle exact phrases with quotes
	if strings.HasPrefix(query, "\"") && strings.HasSuffix(query, "\"") {
		return query
	}

	// Split into words and add wildcards
	words := strings.Fields(query)
	wildcardParts := make([]string, len(words))
	for i, word := range words {
		// Add suffix wildcard
		wildcardParts[i] = word + "*"
	}
	return strings.Join(wildcardParts, " ")
}

// buildFuzzyQuery adds fuzzy matching to search terms
func buildFuzzyQuery(query string) string {
	// Handle exact phrases with quotes
	if strings.HasPrefix(query, "\"") && strings.HasSuffix(query, "\"") {
		return query
	}

	// Split into words and add fuzzy matching
	words := strings.Fields(query)
	fuzzyParts := make([]string, len(words))
	for i, word := range words {
		// Add fuzzy matching with edit distance ~1
		fuzzyParts[i] = word + "~1"
	}
	return strings.Join(fuzzyParts, " ")
}

// GetNodeByUUID retrieves a node by its UUID
func (c *Neo4jClient) GetNodeByUUID(ctx context.Context, uuid string) (map[string]any, error) {
	cypher := `
		MATCH (n {uuid: $uuid})
		RETURN
			n.uuid AS uuid,
			labels(n) AS labels,
			properties(n) AS properties
	`

	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{"uuid": uuid})
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("node not found: %s", uuid)
	}

	return results[0], nil
}

// TraverseGraph traverses the graph from a starting node
func (c *Neo4jClient) TraverseGraph(ctx context.Context, startUUID string, relTypes []string, direction string, maxDepth int) ([]map[string]any, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}

	// Build relationship pattern
	relPattern := ""
	if len(relTypes) > 0 {
		relPattern = ":"
		for i, rt := range relTypes {
			if i > 0 {
				relPattern += "|"
			}
			relPattern += rt
		}
	}

	// Build direction pattern
	var cypher string
	params := map[string]any{
		"startUUID": startUUID,
		"maxDepth":  maxDepth,
	}

	switch direction {
	case "INCOMING":
		cypher = fmt.Sprintf(`
			MATCH (start {uuid: $startUUID})
			MATCH (start)<-[r%s*1..%d]-(end)
			RETURN
				start.uuid AS start_uuid,
				labels(start) AS start_labels,
				start.name AS start_name,
				type(r[-1]) AS relationship,
				end.uuid AS end_uuid,
				labels(end) AS end_labels,
				end.name AS end_name,
				properties(end) AS end_properties
		`, relPattern, maxDepth)
	case "BOTH":
		cypher = fmt.Sprintf(`
			MATCH (start {uuid: $startUUID})
			MATCH (start)-[r%s*1..%d]-(end)
			RETURN
				start.uuid AS start_uuid,
				labels(start) AS start_labels,
				start.name AS start_name,
				type(r[-1]) AS relationship,
				end.uuid AS end_uuid,
				labels(end) AS end_labels,
				end.name AS end_name,
				properties(end) AS end_properties
		`, relPattern, maxDepth)
	default: // OUTGOING
		cypher = fmt.Sprintf(`
			MATCH (start {uuid: $startUUID})
			MATCH (start)-[r%s*1..%d]->(end)
			RETURN
				start.uuid AS start_uuid,
				labels(start) AS start_labels,
				start.name AS start_name,
				type(r[-1]) AS relationship,
				end.uuid AS end_uuid,
				labels(end) AS end_labels,
				end.name AS end_name,
				properties(end) AS end_properties
		`, relPattern, maxDepth)
	}

	return c.ExecuteQuery(ctx, cypher, params)
}

// GetSchema retrieves the database schema (labels and relationship types)
func (c *Neo4jClient) GetSchema(ctx context.Context, includeProperties bool, includeCounts bool) (map[string]any, error) {
	schema := make(map[string]any)

	// Get labels
	labelsResult, err := c.ExecuteQuery(ctx, "CALL db.labels() YIELD label RETURN collect(label) as labels", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	if len(labelsResult) > 0 {
		schema["labels"] = labelsResult[0]["labels"]
	}

	// Get relationship types
	relTypesResult, err := c.ExecuteQuery(ctx, "CALL db.relationshipTypes() YIELD relationshipType RETURN collect(relationshipType) as types", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get relationship types: %w", err)
	}
	if len(relTypesResult) > 0 {
		schema["relationships"] = relTypesResult[0]["types"]
	}

	// Optionally get properties for each label
	if includeProperties {
		labels, ok := schema["labels"].([]any)
		if ok {
			labelProps := make(map[string]any)
			for _, labelAny := range labels {
				label, ok := labelAny.(string)
				if !ok {
					continue
				}
				cypher := fmt.Sprintf("MATCH (n:%s) WITH n LIMIT 1 RETURN keys(n) as properties", label)
				propsResult, err := c.ExecuteQuery(ctx, cypher, nil)
				if err == nil && len(propsResult) > 0 {
					labelProps[label] = propsResult[0]["properties"]
				}
			}
			schema["label_properties"] = labelProps
		}
	}

	// Optionally get node counts
	if includeCounts {
		labels, ok := schema["labels"].([]any)
		if ok {
			labelCounts := make(map[string]any)
			for _, labelAny := range labels {
				label, ok := labelAny.(string)
				if !ok {
					continue
				}
				cypher := fmt.Sprintf("MATCH (n:%s) RETURN count(n) as count", label)
				countResult, err := c.ExecuteQuery(ctx, cypher, nil)
				if err == nil && len(countResult) > 0 {
					labelCounts[label] = countResult[0]["count"]
				}
			}
			schema["label_counts"] = labelCounts
		}
	}

	return schema, nil
}

// checkAPOCAvailable checks if APOC procedures are available in Neo4j
func (c *Neo4jClient) checkAPOCAvailable(ctx context.Context) bool {
	cypher := `
		CALL dbms.procedures() YIELD name
		WHERE name STARTS WITH 'apoc.path'
		RETURN count(*) > 0 AS available
	`
	results, err := c.ExecuteQuery(ctx, cypher, nil)
	if err != nil || len(results) == 0 {
		return false
	}
	if available, ok := results[0]["available"].(bool); ok {
		return available
	}
	return false
}

// IsAPOCAvailable returns whether APOC procedures are available
func (c *Neo4jClient) IsAPOCAvailable() bool {
	return c.apocAvailable
}

// ShortestPathToLabel finds shortest paths from source node to any node with the target label
// Uses APOC if available, falls back to native Cypher otherwise
func (c *Neo4jClient) ShortestPathToLabel(ctx context.Context, sourceUUID string, targetLabel string, maxHops int, topK int) ([]PathResult, error) {
	if maxHops <= 0 {
		maxHops = 8
	}
	if topK <= 0 {
		topK = 3
	}

	var cypher string
	if c.apocAvailable {
		cypher = c.buildAPOCShortestPathQuery(targetLabel)
	} else {
		cypher = c.buildNativeShortestPathQuery(targetLabel, maxHops)
	}

	params := map[string]any{
		"sourceUUID": sourceUUID,
		"maxHops":    maxHops,
		"topK":       topK,
	}

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("shortest path query failed: %w", err)
	}

	return c.parsePathResults(results)
}

// buildAPOCShortestPathQuery builds a Cypher query using APOC for label-based shortest path
func (c *Neo4jClient) buildAPOCShortestPathQuery(targetLabel string) string {
	return fmt.Sprintf(`
		MATCH (source {uuid: $sourceUUID})
		CALL apoc.path.expandConfig(source, {
			relationshipFilter: null,
			labelFilter: ">%s",
			minLevel: 1,
			maxLevel: $maxHops,
			limit: 20,
			bfs: true,
			uniqueness: "NODE_PATH"
		}) YIELD path
		WITH path,
			 [n IN nodes(path) | n.name] AS names,
			 [n IN nodes(path) | labels(n)[0]] AS nodeLabels,
			 [r IN relationships(path) | type(r)] AS rels,
			 last(nodes(path)) AS endNode
		RETURN
			reduce(s = '', i IN range(0, size(names)-1) |
				s + nodeLabels[i] + '[' +
				CASE WHEN names[i] IS NULL THEN 'null'
				     WHEN size(toString(names[i])) > 30 THEN left(toString(names[i]), 27) + '...'
				     ELSE toString(names[i]) END + ']' +
				CASE WHEN i < size(rels) THEN ' -[' + rels[i] + ']-> ' ELSE '' END
			) AS pathChain,
			endNode.uuid AS endUUID,
			endNode.name AS endName,
			labels(endNode) AS endLabels,
			properties(endNode) AS endProperties,
			length(path) AS hops
		ORDER BY hops ASC
		LIMIT $topK
	`, targetLabel)
}

// buildNativeShortestPathQuery builds a native Cypher query for shortest path (fallback)
func (c *Neo4jClient) buildNativeShortestPathQuery(targetLabel string, maxHops int) string {
	return fmt.Sprintf(`
		MATCH (source {uuid: $sourceUUID})
		MATCH (target:%s)
		WHERE source <> target
		MATCH path = shortestPath((source)-[*1..%d]-(target))
		WITH path,
			 [n IN nodes(path) | n.name] AS names,
			 [n IN nodes(path) | labels(n)[0]] AS nodeLabels,
			 [r IN relationships(path) | type(r)] AS rels,
			 last(nodes(path)) AS endNode
		RETURN
			reduce(s = '', i IN range(0, size(names)-1) |
				s + nodeLabels[i] + '[' +
				CASE WHEN names[i] IS NULL THEN 'null'
				     WHEN size(toString(names[i])) > 30 THEN left(toString(names[i]), 27) + '...'
				     ELSE toString(names[i]) END + ']' +
				CASE WHEN i < size(rels) THEN ' -[' + rels[i] + ']-> ' ELSE '' END
			) AS pathChain,
			endNode.uuid AS endUUID,
			endNode.name AS endName,
			labels(endNode) AS endLabels,
			properties(endNode) AS endProperties,
			length(path) AS hops
		ORDER BY hops ASC
		LIMIT $topK
	`, targetLabel, maxHops)
}

// parsePathResults converts query results to PathResult slice
func (c *Neo4jClient) parsePathResults(results []map[string]any) ([]PathResult, error) {
	paths := make([]PathResult, 0, len(results))

	for _, r := range results {
		path := PathResult{}

		if pathChain, ok := r["pathChain"].(string); ok {
			path.PathChain = pathChain
		}
		if uuid, ok := r["endUUID"].(string); ok {
			path.EndNodeUUID = uuid
		}
		if name, ok := r["endName"].(string); ok {
			path.EndNodeName = name
		}
		if labels, ok := r["endLabels"].([]any); ok {
			path.EndNodeLabels = make([]string, 0, len(labels))
			for _, l := range labels {
				if label, ok := l.(string); ok {
					path.EndNodeLabels = append(path.EndNodeLabels, label)
				}
			}
		}
		if props, ok := r["endProperties"].(map[string]any); ok {
			path.EndProperties = props
		}
		if hops, ok := r["hops"].(int64); ok {
			path.Hops = int(hops)
		}

		paths = append(paths, path)
	}

	return paths, nil
}

// GetPopularVehicles retrieves popular vehicles from the database
func (c *Neo4jClient) GetPopularVehicles(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 6
	}

	cypher := `
		MATCH (v:Vehicle)
		RETURN v.uuid AS uuid, v.name AS name, labels(v) AS labels
		ORDER BY v.name
		LIMIT $limit
	`

	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{"limit": limit})
	if err != nil {
		return nil, fmt.Errorf("failed to get popular vehicles: %w", err)
	}

	return results, nil
}

// GetReachableLabels returns all distinct labels reachable from a source node within maxHops
func (c *Neo4jClient) GetReachableLabels(ctx context.Context, sourceUUID string, maxHops int) ([]string, error) {
	if maxHops <= 0 {
		maxHops = 5
	}

	var cypher string
	if c.apocAvailable {
		cypher = `
			MATCH (source {uuid: $sourceUUID})
			CALL apoc.path.expandConfig(source, {
				relationshipFilter: null,
				minLevel: 1,
				maxLevel: $maxHops,
				uniqueness: "NODE_GLOBAL"
			}) YIELD path
			WITH DISTINCT last(nodes(path)) AS endNode
			RETURN DISTINCT labels(endNode)[0] AS label
			LIMIT 30
		`
	} else {
		cypher = fmt.Sprintf(`
			MATCH (source {uuid: $sourceUUID})
			MATCH path = (source)-[*1..%d]-(endNode)
			WITH DISTINCT endNode
			RETURN DISTINCT labels(endNode)[0] AS label
			LIMIT 30
		`, maxHops)
	}

	params := map[string]any{
		"sourceUUID": sourceUUID,
		"maxHops":    maxHops,
	}

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get reachable labels: %w", err)
	}

	labels := make([]string, 0, len(results))
	for _, r := range results {
		if label, ok := r["label"].(string); ok {
			labels = append(labels, label)
		}
	}

	return labels, nil
}

// HierarchicalNode represents a node with its children in a hierarchy
type HierarchicalNode struct {
	UUID       string              `json:"uuid"`
	Name       string              `json:"name"`
	Labels     []string            `json:"labels"`
	Properties map[string]any      `json:"properties"`
	Depth      int                 `json:"depth"`
	ParentUUID string              `json:"parent_uuid,omitempty"`
	Children   []*HierarchicalNode `json:"children,omitempty"`
}

// GetHierarchicalChildren retrieves all children of a node up to maxDepth
// Returns a flat list of nodes with their depth information
// Used for "하위 점수 모두" type queries
func (c *Neo4jClient) GetHierarchicalChildren(
	ctx context.Context,
	sourceUUID string,
	maxDepth int,
	relationshipTypes []string,
) ([]*HierarchicalNode, error) {
	if maxDepth <= 0 {
		maxDepth = 4
	}

	// Build relationship filter
	relFilter := ""
	if len(relationshipTypes) > 0 {
		relFilter = ":"
		for i, rt := range relationshipTypes {
			if i > 0 {
				relFilter += "|"
			}
			relFilter += rt
		}
	}

	// Query to get all descendants with their depth
	cypher := fmt.Sprintf(`
		MATCH (source {uuid: $uuid})
		OPTIONAL MATCH path = (source)-[r%s*1..%d]->(child)
		WITH source, child, length(path) as depth
		WHERE child IS NOT NULL
		RETURN DISTINCT
			child.uuid AS uuid,
			child.name AS name,
			labels(child) AS labels,
			properties(child) AS properties,
			depth
		ORDER BY depth, child.name
	`, relFilter, maxDepth)

	params := map[string]any{
		"uuid": sourceUUID,
	}

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("hierarchical query failed: %w", err)
	}

	// Parse results into HierarchicalNode list
	nodes := make([]*HierarchicalNode, 0, len(results))
	for _, r := range results {
		node := &HierarchicalNode{
			Children: make([]*HierarchicalNode, 0),
		}

		if uuid, ok := r["uuid"].(string); ok {
			node.UUID = uuid
		}
		if name, ok := r["name"].(string); ok {
			node.Name = name
		}
		if labels, ok := r["labels"].([]any); ok {
			node.Labels = make([]string, 0, len(labels))
			for _, l := range labels {
				if label, ok := l.(string); ok {
					node.Labels = append(node.Labels, label)
				}
			}
		}
		if props, ok := r["properties"].(map[string]any); ok {
			node.Properties = props
		}
		if depth, ok := r["depth"].(int64); ok {
			node.Depth = int(depth)
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// GetScoreHierarchy retrieves the complete score hierarchy for a vehicle
// Optimized for Autobild score structure: Vehicle → TotalScore → SubScores → Individual Scores
func (c *Neo4jClient) GetScoreHierarchy(
	ctx context.Context,
	vehicleUUID string,
) (*HierarchicalNode, []*HierarchicalNode, error) {
	cypher := `
		MATCH (v {uuid: $uuid})

		// Get vehicle info
		WITH v

		// Level 1: AutobildTotalScore or any direct score
		OPTIONAL MATCH (v)-[:hasAutobildScore|hasScore*1..2]->(scoreRoot)
		WHERE any(label IN labels(scoreRoot) WHERE label CONTAINS 'Score' OR label CONTAINS 'Total')

		// Get all sub-scores from the score root
		OPTIONAL MATCH path = (scoreRoot)-[:hasSubScore*1..4]->(subScore)

		WITH v, scoreRoot, subScore, length(path) as depth
		WHERE subScore IS NOT NULL

		RETURN
			v.name AS vehicleName,
			v.uuid AS vehicleUUID,
			labels(v) AS vehicleLabels,
			properties(v) AS vehicleProps,
			scoreRoot.uuid AS rootUUID,
			scoreRoot.name AS rootName,
			labels(scoreRoot) AS rootLabels,
			properties(scoreRoot) AS rootProps,
			collect(DISTINCT {
				uuid: subScore.uuid,
				name: subScore.name,
				labels: labels(subScore),
				properties: properties(subScore),
				depth: depth
			}) AS subScores
	`

	params := map[string]any{"uuid": vehicleUUID}

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, nil, fmt.Errorf("score hierarchy query failed: %w", err)
	}

	if len(results) == 0 {
		return nil, nil, fmt.Errorf("no score hierarchy found for vehicle: %s", vehicleUUID)
	}

	r := results[0]

	// Parse root node
	root := &HierarchicalNode{
		Children: make([]*HierarchicalNode, 0),
	}
	if uuid, ok := r["rootUUID"].(string); ok {
		root.UUID = uuid
	}
	if name, ok := r["rootName"].(string); ok {
		root.Name = name
	}
	if labels, ok := r["rootLabels"].([]any); ok {
		root.Labels = make([]string, 0, len(labels))
		for _, l := range labels {
			if label, ok := l.(string); ok {
				root.Labels = append(root.Labels, label)
			}
		}
	}
	if props, ok := r["rootProps"].(map[string]any); ok {
		root.Properties = props
	}

	// Parse sub-scores
	subScores := make([]*HierarchicalNode, 0)
	if scores, ok := r["subScores"].([]any); ok {
		for _, s := range scores {
			if scoreMap, ok := s.(map[string]any); ok {
				node := &HierarchicalNode{
					Children: make([]*HierarchicalNode, 0),
				}
				if uuid, ok := scoreMap["uuid"].(string); ok {
					node.UUID = uuid
				}
				if name, ok := scoreMap["name"].(string); ok {
					node.Name = name
				}
				if labels, ok := scoreMap["labels"].([]any); ok {
					node.Labels = make([]string, 0, len(labels))
					for _, l := range labels {
						if label, ok := l.(string); ok {
							node.Labels = append(node.Labels, label)
						}
					}
				}
				if props, ok := scoreMap["properties"].(map[string]any); ok {
					node.Properties = props
				}
				if depth, ok := scoreMap["depth"].(int64); ok {
					node.Depth = int(depth)
				}
				subScores = append(subScores, node)
			}
		}
	}

	return root, subScores, nil
}

// GetPerformanceScoreDetails retrieves detailed performance scores for a specific score node
// This is optimized for getting all sub-scores under a PerformanceTotalScore or similar
func (c *Neo4jClient) GetPerformanceScoreDetails(
	ctx context.Context,
	scoreNodeUUID string,
	maxDepth int,
) (*HierarchicalNode, []*HierarchicalNode, error) {
	if maxDepth <= 0 {
		maxDepth = 4
	}

	cypher := fmt.Sprintf(`
		MATCH (root {uuid: $uuid})

		// Get all sub-scores with their hierarchy and parent information
		OPTIONAL MATCH path = (root)-[:hasSubScore*1..%d]->(subScore)

		WITH root, path, subScore, length(path) as depth, nodes(path) as pathNodes
		WHERE subScore IS NOT NULL

		// Calculate parent UUID: for depth 1, parent is root; for deeper levels, parent is previous node in path
		WITH root, subScore, depth,
			 CASE WHEN depth = 1 THEN root.uuid
				  ELSE pathNodes[size(pathNodes)-2].uuid END as parentUUID

		// Order by depth to build hierarchy correctly
		ORDER BY depth, subScore.name

		RETURN
			root.uuid AS rootUUID,
			root.name AS rootName,
			labels(root) AS rootLabels,
			properties(root) AS rootProps,
			collect({
				uuid: subScore.uuid,
				name: subScore.name,
				labels: labels(subScore),
				properties: properties(subScore),
				depth: depth,
				parentUUID: parentUUID
			}) AS children
	`, maxDepth)

	params := map[string]any{"uuid": scoreNodeUUID}

	// 디버그 로깅
	log.Printf("[Cypher] GetPerformanceScoreDetails: uuid=%s, maxDepth=%d", scoreNodeUUID, maxDepth)

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, nil, fmt.Errorf("performance score details query failed: %w", err)
	}

	if len(results) == 0 {
		log.Printf("[Cypher] No results found for uuid=%s", scoreNodeUUID)
		return nil, nil, fmt.Errorf("no score details found for: %s", scoreNodeUUID)
	}

	r := results[0]

	// 결과 로깅
	if childList, ok := r["children"].([]any); ok {
		log.Printf("[Cypher] Found %d children nodes for root", len(childList))
		for i, c := range childList {
			if i >= 10 { // 처음 10개만 로깅
				log.Printf("[Cypher]   ... and %d more nodes", len(childList)-10)
				break
			}
			if childMap, ok := c.(map[string]any); ok {
				log.Printf("[Cypher]   [%d] depth=%v, name=%s, parentUUID=%v",
					i, childMap["depth"], childMap["name"], childMap["parentUUID"])
			}
		}
	}

	// Parse root
	root := &HierarchicalNode{
		Children: make([]*HierarchicalNode, 0),
	}
	if uuid, ok := r["rootUUID"].(string); ok {
		root.UUID = uuid
	}
	if name, ok := r["rootName"].(string); ok {
		root.Name = name
	}
	if labels, ok := r["rootLabels"].([]any); ok {
		root.Labels = make([]string, 0, len(labels))
		for _, l := range labels {
			if label, ok := l.(string); ok {
				root.Labels = append(root.Labels, label)
			}
		}
	}
	if props, ok := r["rootProps"].(map[string]any); ok {
		root.Properties = props
	}

	// Parse children
	children := make([]*HierarchicalNode, 0)
	if childList, ok := r["children"].([]any); ok {
		for _, c := range childList {
			if childMap, ok := c.(map[string]any); ok {
				node := &HierarchicalNode{
					Children: make([]*HierarchicalNode, 0),
				}
				if uuid, ok := childMap["uuid"].(string); ok {
					node.UUID = uuid
				}
				if name, ok := childMap["name"].(string); ok {
					node.Name = name
				}
				if labels, ok := childMap["labels"].([]any); ok {
					node.Labels = make([]string, 0, len(labels))
					for _, l := range labels {
						if label, ok := l.(string); ok {
							node.Labels = append(node.Labels, label)
						}
					}
				}
				if props, ok := childMap["properties"].(map[string]any); ok {
					node.Properties = props
				}
				if depth, ok := childMap["depth"].(int64); ok {
					node.Depth = int(depth)
				}
				if parentUUID, ok := childMap["parentUUID"].(string); ok {
					node.ParentUUID = parentUUID
				}
				children = append(children, node)
			}
		}
	}

	return root, children, nil
}

// VariantPropertyKeys defines the Korean property keys used for vehicle variants
// These are the actual property names stored in Neo4j for vehicle variant information
var VariantPropertyKeys = struct {
	Engine    string
	Trim      string
	ModelYear string
	Fuel      string
	XEV       string
}{
	Engine:    "엔진",
	Trim:      "TRIM",
	ModelYear: "연식",
	Fuel:      "연료",
	XEV:       "xEV",
}

// VariantInfo holds extracted variant information from a vehicle node
// Used to distinguish between different variants of the same vehicle model
type VariantInfo struct {
	Engine    string `json:"engine,omitempty"`
	Trim      string `json:"trim,omitempty"`
	ModelYear string `json:"model_year,omitempty"`
	Fuel      string `json:"fuel,omitempty"`
	XEV       string `json:"xev,omitempty"`
}

// ExtractVariantInfo extracts variant information from node properties
// Handles Korean property keys as stored in Neo4j
// Note: 연식 (ModelYear) is stored as float64 in Neo4j, so we handle multiple types
func ExtractVariantInfo(properties map[string]any) *VariantInfo {
	if properties == nil {
		return &VariantInfo{}
	}

	info := &VariantInfo{}

	// Engine (string)
	if v, ok := properties[VariantPropertyKeys.Engine].(string); ok {
		info.Engine = v
	}

	// Trim (string)
	if v, ok := properties[VariantPropertyKeys.Trim].(string); ok {
		info.Trim = v
	}

	// ModelYear - handles float64, int, int64, and string types
	// Neo4j stores numeric values as float64, e.g., 2022.0
	switch v := properties[VariantPropertyKeys.ModelYear].(type) {
	case string:
		info.ModelYear = v
	case float64:
		info.ModelYear = fmt.Sprintf("%.0f", v) // 2022.0 → "2022"
	case int:
		info.ModelYear = fmt.Sprintf("%d", v)
	case int64:
		info.ModelYear = fmt.Sprintf("%d", v)
	}

	// Fuel (string)
	if v, ok := properties[VariantPropertyKeys.Fuel].(string); ok {
		info.Fuel = v
	}

	// XEV (string)
	if v, ok := properties[VariantPropertyKeys.XEV].(string); ok {
		info.XEV = v
	}

	return info
}

// FormatDisplay creates a display string for variant info
// e.g., "1.6T HEV, Premium, 2024"
func (v *VariantInfo) FormatDisplay() string {
	if v == nil {
		return ""
	}

	parts := make([]string, 0, 4)

	// Engine + Fuel type + xEV combined (e.g., "1.6T gsl MHEV", "2.0D dsl MHEV")
	if v.Engine != "" {
		enginePart := v.Engine
		if v.Fuel != "" {
			enginePart += " " + v.Fuel
		}
		if v.XEV != "" {
			enginePart += " " + v.XEV
		}
		parts = append(parts, enginePart)
	}

	// Trim
	if v.Trim != "" {
		parts = append(parts, v.Trim)
	}

	// Model year
	if v.ModelYear != "" {
		parts = append(parts, v.ModelYear)
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, ", ")
}

// IsEmpty returns true if no variant info is available
func (v *VariantInfo) IsEmpty() bool {
	if v == nil {
		return true
	}
	return v.Engine == "" && v.Trim == "" && v.ModelYear == "" && v.Fuel == "" && v.XEV == ""
}

// EnrichCandidatesWithEngine adds engine info from relationships to candidates
// This is a batch operation to minimize database queries
func (c *Neo4jClient) EnrichCandidatesWithEngine(ctx context.Context, uuids []string) (map[string]string, error) {
	if len(uuids) == 0 {
		return map[string]string{}, nil
	}

	cypher := `
		UNWIND $uuids AS uuid
		MATCH (v {uuid: uuid})
		OPTIONAL MATCH (v)-[:hasEngine]->(e:Engine)
		RETURN v.uuid AS uuid, e.name AS engineName
	`

	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{"uuids": uuids})
	if err != nil {
		return nil, err
	}

	engineMap := make(map[string]string)
	for _, r := range results {
		if uuid, ok := r["uuid"].(string); ok {
			if engineName, ok := r["engineName"].(string); ok && engineName != "" {
				engineMap[uuid] = engineName
			}
		}
	}

	return engineMap, nil
}

// NeighborInfo represents a 1-hop neighbor node
type NeighborInfo struct {
	Relationship string   `json:"relationship"`
	Outgoing     bool     `json:"outgoing"`
	Name         string   `json:"name"`
	UUID         string   `json:"uuid"`
	Labels       []string `json:"labels"`
}

// GetOneHopNeighborSummary returns a summary of 1-hop neighbors for display
// This provides generic context for any node type
func (c *Neo4jClient) GetOneHopNeighborSummary(ctx context.Context, uuid string, limit int) ([]NeighborInfo, error) {
	if limit <= 0 {
		limit = 5
	}

	cypher := `
		MATCH (n {uuid: $uuid})-[r]-(neighbor)
		RETURN
			type(r) AS relationship,
			startNode(r) = n AS outgoing,
			neighbor.name AS name,
			neighbor.uuid AS uuid,
			labels(neighbor) AS labels
		LIMIT $limit
	`

	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{
		"uuid":  uuid,
		"limit": limit,
	})
	if err != nil {
		return nil, err
	}

	neighbors := make([]NeighborInfo, 0, len(results))
	for _, r := range results {
		neighbor := NeighborInfo{}

		if rel, ok := r["relationship"].(string); ok {
			neighbor.Relationship = rel
		}
		if outgoing, ok := r["outgoing"].(bool); ok {
			neighbor.Outgoing = outgoing
		}
		if name, ok := r["name"].(string); ok {
			neighbor.Name = name
		}
		if uuid, ok := r["uuid"].(string); ok {
			neighbor.UUID = uuid
		}
		if labels, ok := r["labels"].([]any); ok {
			for _, l := range labels {
				if label, ok := l.(string); ok {
					neighbor.Labels = append(neighbor.Labels, label)
				}
			}
		}

		neighbors = append(neighbors, neighbor)
	}

	return neighbors, nil
}

// GetRelationshipDisplayName returns Korean display name for relationship types
func GetRelationshipDisplayName(relType string) string {
	displayNames := map[string]string{
		"hasEngine":           "엔진",
		"hasAutobildScore":    "평가",
		"hasSubScore":         "하위점수",
		"hasPerformanceScore": "성능",
		"hasCostScore":        "비용",
		"SIMILAR_TO":          "유사차량",
		"COMPETITOR_OF":       "경쟁차량",
		"hasBrand":            "브랜드",
		"hasSegment":          "세그먼트",
		"hasBodyType":         "차체타입",
	}

	if name, ok := displayNames[relType]; ok {
		return name
	}
	return relType
}

// =============================================================================
// Label-Based Listing Methods (for "list all X" queries)
// =============================================================================

// LabelListingResult represents the result of a label-based listing query
type LabelListingResult struct {
	Items      []LabelListingItem `json:"items"`
	TotalCount int                `json:"total_count"`
	HasMore    bool               `json:"has_more"`
}

// LabelListingItem represents a single item in a label listing
type LabelListingItem struct {
	UUID        string         `json:"uuid"`
	Name        string         `json:"name"`
	Labels      []string       `json:"labels"`
	Properties  map[string]any `json:"properties,omitempty"`
	VariantInfo *VariantInfo   `json:"variant_info,omitempty"`
}

// ListNodesByLabel retrieves all nodes with a specific label
// Returns nodes ordered by name with pagination support
func (c *Neo4jClient) ListNodesByLabel(ctx context.Context, label string, limit int, orderBy string) ([]map[string]any, int, error) {
	// Validate and set defaults
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200 // Cap to prevent excessive results
	}
	if orderBy == "" {
		orderBy = "name"
	}

	// Validate label to prevent injection
	if !isValidLabel(label) {
		return nil, 0, fmt.Errorf("invalid label: %s", label)
	}

	// Count total nodes
	countCypher := fmt.Sprintf(`MATCH (n:%s) RETURN count(n) as total`, label)
	countResults, err := c.ExecuteQuery(ctx, countCypher, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("count query failed: %w", err)
	}

	total := 0
	if len(countResults) > 0 {
		if t, ok := countResults[0]["total"].(int64); ok {
			total = int(t)
		}
	}

	// Query nodes
	cypher := fmt.Sprintf(`
		MATCH (n:%s)
		RETURN
			n.uuid AS uuid,
			n.name AS name,
			labels(n) AS labels,
			properties(n) AS properties
		ORDER BY n.%s
		LIMIT $limit
	`, label, orderBy)

	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{
		"limit": limit,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing query failed: %w", err)
	}

	return results, total, nil
}

// ListNodesByLabels retrieves nodes matching any of the given labels
// Used for label groups (e.g., Vehicle + CompetitorVehicle + SimilarVehicle)
func (c *Neo4jClient) ListNodesByLabels(ctx context.Context, labels []string, limit int, orderBy string) ([]map[string]any, int, error) {
	if len(labels) == 0 {
		return nil, 0, fmt.Errorf("at least one label is required")
	}

	// Validate and set defaults
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if orderBy == "" {
		orderBy = "name"
	}

	// Validate all labels
	for _, label := range labels {
		if !isValidLabel(label) {
			return nil, 0, fmt.Errorf("invalid label: %s", label)
		}
	}

	// Build label condition: (n:Label1 OR n:Label2 OR ...)
	labelConditions := make([]string, len(labels))
	for i, label := range labels {
		labelConditions[i] = fmt.Sprintf("n:%s", label)
	}
	labelCondition := strings.Join(labelConditions, " OR ")

	// Count total nodes
	countCypher := fmt.Sprintf(`MATCH (n) WHERE %s RETURN count(n) as total`, labelCondition)
	countResults, err := c.ExecuteQuery(ctx, countCypher, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("count query failed: %w", err)
	}

	total := 0
	if len(countResults) > 0 {
		if t, ok := countResults[0]["total"].(int64); ok {
			total = int(t)
		}
	}

	// Query nodes
	cypher := fmt.Sprintf(`
		MATCH (n) WHERE %s
		RETURN
			n.uuid AS uuid,
			n.name AS name,
			labels(n) AS labels,
			properties(n) AS properties
		ORDER BY n.%s
		LIMIT $limit
	`, labelCondition, orderBy)

	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{
		"limit": limit,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing query failed: %w", err)
	}

	return results, total, nil
}

// CountNodesByLabel counts nodes with a specific label
func (c *Neo4jClient) CountNodesByLabel(ctx context.Context, label string) (int64, error) {
	if !isValidLabel(label) {
		return 0, fmt.Errorf("invalid label: %s", label)
	}

	cypher := fmt.Sprintf(`MATCH (n:%s) RETURN count(n) AS count`, label)

	results, err := c.ExecuteQuery(ctx, cypher, nil)
	if err != nil {
		return 0, fmt.Errorf("count query failed: %w", err)
	}

	if len(results) > 0 {
		if count, ok := results[0]["count"].(int64); ok {
			return count, nil
		}
	}

	return 0, nil
}

// isValidLabel checks if a label name is safe to use in a Cypher query
// Only allows alphanumeric characters and underscores
func isValidLabel(label string) bool {
	if len(label) == 0 || len(label) > 100 {
		return false
	}
	for _, r := range label {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// LabelExists checks if a label exists in the database
func (c *Neo4jClient) LabelExists(ctx context.Context, label string) bool {
	cypher := `CALL db.labels() YIELD label WHERE label = $label RETURN count(*) > 0 as exists`
	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{"label": label})
	if err != nil || len(results) == 0 {
		return false
	}
	if exists, ok := results[0]["exists"].(bool); ok {
		return exists
	}
	return false
}

// GetAllLabelsWithCounts returns all labels with their node counts
// Useful for presenting listing options to users
func (c *Neo4jClient) GetAllLabelsWithCounts(ctx context.Context) (map[string]int64, error) {
	cypher := `
		CALL db.labels() YIELD label
		CALL {
			WITH label
			MATCH (n) WHERE label IN labels(n)
			RETURN count(n) as cnt
		}
		RETURN label, cnt
		ORDER BY cnt DESC
	`

	results, err := c.ExecuteQuery(ctx, cypher, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels with counts: %w", err)
	}

	labelCounts := make(map[string]int64)
	for _, r := range results {
		if label, ok := r["label"].(string); ok {
			if cnt, ok := r["cnt"].(int64); ok {
				labelCounts[label] = cnt
			}
		}
	}

	return labelCounts, nil
}

// GetListableLabelOptions returns label options suitable for listing queries
// Includes display names and node counts for user presentation
func (c *Neo4jClient) GetListableLabelOptions(ctx context.Context) ([]LabelOption, error) {
	listableLabels := GetListableLabels()
	options := make([]LabelOption, 0, len(listableLabels))

	for _, label := range listableLabels {
		count, err := c.CountNodesByLabel(ctx, label)
		if err != nil {
			continue
		}

		displayName := LabelDisplayNames[label]
		if displayName == "" {
			displayName = label
		}

		options = append(options, LabelOption{
			Label:       label,
			DisplayName: displayName,
			NodeCount:   int(count),
		})
	}

	return options, nil
}

// LabelOption represents a label choice for clarification
type LabelOption struct {
	Label       string `json:"label"`        // Neo4j label name
	DisplayName string `json:"display_name"` // Korean display name
	Description string `json:"description"`  // Optional description
	NodeCount   int    `json:"node_count"`   // Number of nodes with this label
}

// =============================================================================
// Exploration-First: 관계 구조 탐색 메서드
// =============================================================================

// RelationshipSchemaInfo describes relationships discovered from a node
type RelationshipSchemaInfo struct {
	RelType        string   `json:"rel_type"`         // e.g., "hasManufacturer"
	Direction      string   `json:"direction"`        // "incoming" or "outgoing"
	ConnectedLabel string   `json:"connected_label"`  // e.g., "CompetitorVehicle"
	Count          int      `json:"count"`            // Number of connected nodes
	SampleNodes    []string `json:"sample_nodes"`     // Sample node names (up to 5)
}

// GetRelationshipSchema discovers all relationship types connected to a node
// This is the core method for the exploration-first approach
// It answers: "What relationships exist from this anchor node?"
func (c *Neo4jClient) GetRelationshipSchema(ctx context.Context, uuid string) ([]RelationshipSchemaInfo, error) {
	cypher := `
		MATCH (n {uuid: $uuid})-[r]-(m)
		WITH type(r) AS relType,
			 CASE WHEN startNode(r) = n THEN 'outgoing' ELSE 'incoming' END AS direction,
			 labels(m)[0] AS connectedLabel,
			 m.name AS nodeName
		RETURN relType, direction, connectedLabel,
			   count(*) AS cnt,
			   collect(DISTINCT nodeName)[0..5] AS sampleNodes
		ORDER BY cnt DESC
	`

	results, err := c.ExecuteQuery(ctx, cypher, map[string]any{"uuid": uuid})
	if err != nil {
		return nil, fmt.Errorf("relationship schema query failed: %w", err)
	}

	schemas := make([]RelationshipSchemaInfo, 0, len(results))
	for _, r := range results {
		schema := RelationshipSchemaInfo{}

		if relType, ok := r["relType"].(string); ok {
			schema.RelType = relType
		}
		if direction, ok := r["direction"].(string); ok {
			schema.Direction = direction
		}
		if connectedLabel, ok := r["connectedLabel"].(string); ok {
			schema.ConnectedLabel = connectedLabel
		}
		if cnt, ok := r["cnt"].(int64); ok {
			schema.Count = int(cnt)
		}
		if samples, ok := r["sampleNodes"].([]any); ok {
			schema.SampleNodes = make([]string, 0, len(samples))
			for _, s := range samples {
				if name, ok := s.(string); ok {
					schema.SampleNodes = append(schema.SampleNodes, name)
				}
			}
		}

		schemas = append(schemas, schema)
	}

	return schemas, nil
}

// GetConnectedNodesByRelationship traverses a specific relationship from anchor
// and returns connected nodes with optional filters
// This method executes the actual query after structure exploration
func (c *Neo4jClient) GetConnectedNodesByRelationship(
	ctx context.Context,
	anchorUUID string,
	relationship string,
	direction string, // "incoming", "outgoing", "both"
	targetLabel string, // Optional: filter by label
	propertyFilters map[string]any, // Optional: WHERE conditions
	limit int,
) ([]map[string]any, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	// Build direction pattern
	var relPattern string
	switch direction {
	case "incoming":
		relPattern = fmt.Sprintf("<-[r:%s]-", relationship)
	case "outgoing":
		relPattern = fmt.Sprintf("-[r:%s]->", relationship)
	default:
		relPattern = fmt.Sprintf("-[r:%s]-", relationship)
	}

	// Build label filter
	labelFilter := ""
	if targetLabel != "" && isValidLabel(targetLabel) {
		labelFilter = ":" + targetLabel
	}

	// Build property WHERE clause
	whereClauses := []string{}
	params := map[string]any{"uuid": anchorUUID, "limit": limit}

	for key, value := range propertyFilters {
		paramKey := "prop_" + sanitizeParamName(key)
		whereClauses = append(whereClauses, fmt.Sprintf("m.`%s` = $%s", key, paramKey))
		params[paramKey] = value
	}

	whereClause := ""
	if len(whereClauses) > 0 {
		whereClause = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Build and execute main query
	cypher := fmt.Sprintf(`
		MATCH (n {uuid: $uuid})%s(m%s)
		%s
		WITH m
		ORDER BY m.name
		LIMIT $limit
		RETURN m.uuid AS uuid, m.name AS name, labels(m) AS labels, properties(m) AS properties
	`, relPattern, labelFilter, whereClause)

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, 0, fmt.Errorf("connected nodes query failed: %w", err)
	}

	// Get total count (without limit)
	countCypher := fmt.Sprintf(`
		MATCH (n {uuid: $uuid})%s(m%s)
		%s
		RETURN count(m) AS total
	`, relPattern, labelFilter, whereClause)

	countParams := make(map[string]any)
	for k, v := range params {
		if k != "limit" {
			countParams[k] = v
		}
	}

	total := len(results)
	countResults, err := c.ExecuteQuery(ctx, countCypher, countParams)
	if err == nil && len(countResults) > 0 {
		if t, ok := countResults[0]["total"].(int64); ok {
			total = int(t)
		}
	}

	return results, total, nil
}

// sanitizeParamName removes characters that might cause issues in parameter names
func sanitizeParamName(name string) string {
	// Replace Korean characters and special chars with underscore
	result := strings.Builder{}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			result.WriteRune(r)
		} else {
			result.WriteRune('_')
		}
	}
	return result.String()
}
