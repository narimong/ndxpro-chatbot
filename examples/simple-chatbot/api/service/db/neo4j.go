package db

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Neo4jClient wraps the Neo4j driver for graph database operations
type Neo4jClient struct {
	driver        neo4j.DriverWithContext
	apocAvailable bool // Whether APOC procedures are available
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

	client := &Neo4jClient{driver: driver}

	// Check APOC availability
	client.apocAvailable = client.checkAPOCAvailable(ctx)

	return client, nil
}

// Close closes the Neo4j driver
func (c *Neo4jClient) Close(ctx context.Context) error {
	return c.driver.Close(ctx)
}

// ExecuteQuery executes a Cypher query and returns the results as a slice of maps
func (c *Neo4jClient) ExecuteQuery(ctx context.Context, cypher string, params map[string]any) ([]map[string]any, error) {
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
	"Vehicle":               "차량",
	"CompetitorVehicle":     "경쟁 차량",
	"SimilarVehicle":        "유사 차량",
	"Engine":                "엔진",
	"Transmission":          "변속기",
	"Tire":                  "타이어",
	"PerformanceTotalScore": "성능 종합 점수",
	"PerformanceAcceleration": "가속 성능",
	"PerformanceBraking":    "제동 성능",
	"PerformanceHandling":   "핸들링",
	"PerformanceRideComfort": "승차감",
	"SafetyScore":           "안전 점수",
	"FuelEfficiency":        "연비",
	"Manufacturer":          "제조사",
	"Segment":               "세그먼트",
	"Price":                 "가격",
	"Trim":                  "트림",
	"Part":                  "부품",
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
