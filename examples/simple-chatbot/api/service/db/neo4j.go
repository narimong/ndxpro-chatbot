package db

import (
	"context"
	"fmt"
	"strings"

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

	results, err := c.ExecuteQuery(ctx, cypher, params)
	if err != nil {
		return nil, nil, fmt.Errorf("performance score details query failed: %w", err)
	}

	if len(results) == 0 {
		return nil, nil, fmt.Errorf("no score details found for: %s", scoreNodeUUID)
	}

	r := results[0]

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
