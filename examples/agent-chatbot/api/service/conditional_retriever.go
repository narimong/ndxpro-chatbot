package service

import (
	"context"
	"fmt"
	"strings"

	"agent-chatbot/api/service/db"
	"agent-chatbot/api/service/tools"

	"github.com/cloudwego/eino/schema"
)

// ConditionalRetriever handles exploration-first conditional queries
// This service implements the 3-step exploration pattern:
// 1. Find anchor node (e.g., "Honda" -> Manufacturer)
// 2. Explore relationships from anchor (discover hasManufacturer -> CompetitorVehicle)
// 3. Execute traversal query with filters
type ConditionalRetriever struct {
	neo4jClient  *db.Neo4jClient
	fullTextTool *tools.Neo4jFullTextSearchTool
	debugEmitter DebugEmitter
}

// ConditionalRetrieverConfig holds configuration for ConditionalRetriever
type ConditionalRetrieverConfig struct {
	Neo4jClient  *db.Neo4jClient
	FullTextTool *tools.Neo4jFullTextSearchTool
	DebugEmitter DebugEmitter
}

// NewConditionalRetriever creates a new conditional retriever
func NewConditionalRetriever(config *ConditionalRetrieverConfig) *ConditionalRetriever {
	return &ConditionalRetriever{
		neo4jClient:  config.Neo4jClient,
		fullTextTool: config.FullTextTool,
		debugEmitter: config.DebugEmitter,
	}
}

// RetrieveWithCondition executes a conditional query through exploration-first approach
// This is the main entry point for handling queries like "Honda가 생산하는 모든 차량"
func (r *ConditionalRetriever) RetrieveWithCondition(
	ctx context.Context,
	query string,
	anchor *tools.ConditionalAnchor,
	filters []tools.ConditionalFilter,
	topK int,
) ([]*schema.Document, error) {
	if topK <= 0 {
		topK = 50
	}

	// Step 1: Find anchor node
	r.emitDebug("conditional_step1_anchor_search", map[string]any{
		"keywords":        anchor.Keywords,
		"expected_labels": anchor.ExpectedLabels,
		"condition_type":  anchor.ConditionType,
	})

	anchorNode, err := r.findAnchorNode(ctx, anchor)
	if err != nil {
		r.emitDebug("conditional_step1_anchor_not_found", map[string]any{
			"error": err.Error(),
		})
		return nil, fmt.Errorf("anchor not found: %w", err)
	}

	r.emitDebug("conditional_step1_anchor_found", map[string]any{
		"uuid":   anchorNode.UUID,
		"name":   anchorNode.Name,
		"labels": anchorNode.Labels,
	})

	// Step 2: Explore structure from anchor
	r.emitDebug("conditional_step2_explore", map[string]any{
		"anchor_uuid": anchorNode.UUID,
		"anchor_name": anchorNode.Name,
	})

	schemas, err := r.neo4jClient.GetRelationshipSchema(ctx, anchorNode.UUID)
	if err != nil {
		return nil, fmt.Errorf("schema exploration failed: %w", err)
	}

	r.emitDebug("conditional_step2_schema", map[string]any{
		"relationships_found": len(schemas),
		"schemas":             schemas,
	})

	if len(schemas) == 0 {
		// No relationships found - return clarification
		return r.buildNoRelationshipsDocument(anchorNode)
	}

	// Step 3: Match condition to relationship
	matchedRel, err := r.matchConditionToRelationship(anchor.ConditionType, schemas)
	if err != nil {
		// No direct match - return schema choice document for clarification
		return r.buildSchemaChoiceDocument(anchorNode, schemas)
	}

	r.emitDebug("conditional_step3_matched", map[string]any{
		"relationship":   matchedRel.RelType,
		"direction":      matchedRel.Direction,
		"target_label":   matchedRel.ConnectedLabel,
		"expected_count": matchedRel.Count,
	})

	// Step 4: Build property filters
	propFilters := make(map[string]any)
	for _, f := range filters {
		if f.FilterType == "property" {
			propFilters[f.Attribute] = f.Value
		}
	}

	// Step 5: Execute query
	results, total, err := r.neo4jClient.GetConnectedNodesByRelationship(
		ctx,
		anchorNode.UUID,
		matchedRel.RelType,
		matchedRel.Direction,
		matchedRel.ConnectedLabel,
		propFilters,
		topK,
	)
	if err != nil {
		return nil, fmt.Errorf("conditional query failed: %w", err)
	}

	r.emitDebug("conditional_step5_results", map[string]any{
		"results_count": len(results),
		"total_count":   total,
		"filters":       propFilters,
	})

	// Step 6: Format as documents
	return r.formatResultsAsDocuments(anchorNode, matchedRel, results, total)
}

// findAnchorNode searches for the anchor entity using full-text search
func (r *ConditionalRetriever) findAnchorNode(
	ctx context.Context,
	anchor *tools.ConditionalAnchor,
) (*tools.NodeCandidate, error) {
	searchQuery := strings.Join(anchor.Keywords, " ")

	// Use label hint if available
	labelHint := ""
	if len(anchor.ExpectedLabels) > 0 {
		labelHint = anchor.ExpectedLabels[0]
	}

	candidates, err := r.fullTextTool.SearchWithFallback(ctx, searchQuery, labelHint, 5)
	if err != nil || len(candidates) == 0 {
		return nil, fmt.Errorf("no anchor found for: %s", searchQuery)
	}

	// Return the best matching candidate
	return &candidates[0], nil
}

// matchConditionToRelationship maps semantic conditions to actual relationships discovered in the graph
func (r *ConditionalRetriever) matchConditionToRelationship(
	conditionType string,
	schemas []db.RelationshipSchemaInfo,
) (*db.RelationshipSchemaInfo, error) {
	return r.matchConditionToRelationshipWithTargets(conditionType, schemas, nil)
}

// matchConditionToRelationshipWithTargets maps semantic conditions with explicit target label priority
func (r *ConditionalRetriever) matchConditionToRelationshipWithTargets(
	conditionType string,
	schemas []db.RelationshipSchemaInfo,
	targetLabels []string,
) (*db.RelationshipSchemaInfo, error) {
	// Priority 1: If target labels are specified, find relationship connecting to those labels
	if len(targetLabels) > 0 {
		for i := range schemas {
			for _, target := range targetLabels {
				if strings.EqualFold(schemas[i].ConnectedLabel, target) {
					r.emitDebug("conditional_target_label_matched", map[string]any{
						"target_label":   target,
						"relationship":   schemas[i].RelType,
						"direction":      schemas[i].Direction,
						"expected_count": schemas[i].Count,
					})
					return &schemas[i], nil
				}
			}
		}
	}

	// Priority 2: Use condition type -> relationship pattern mapping
	conditionMappings := map[string][]string{
		"produces":   {"hasmanufacturer", "manufacturedby", "producedby", "madeby", "manufacturer"},
		"belongs_to": {"hassegment", "belongsto", "hascategory", "incategory", "segment"},
		"has_engine": {"hasengine", "poweredby", "usesengine", "engine"},
		"has_trim":   {"hastrim", "trimlevel", "trim"},
	}

	patterns, ok := conditionMappings[conditionType]
	if !ok || len(patterns) == 0 {
		// No relationship mapping for this condition type
		// May be property-based filter
		return nil, fmt.Errorf("no relationship mapping for condition: %s", conditionType)
	}

	// Try to match patterns against discovered schemas
	for i := range schemas {
		relLower := strings.ToLower(schemas[i].RelType)
		for _, pattern := range patterns {
			if strings.Contains(relLower, pattern) {
				return &schemas[i], nil
			}
		}
	}

	// Priority 3: For "produces" condition, look for vehicle-related labels
	if conditionType == "produces" {
		for i := range schemas {
			labelLower := strings.ToLower(schemas[i].ConnectedLabel)
			if strings.Contains(labelLower, "vehicle") {
				return &schemas[i], nil
			}
		}
	}

	return nil, fmt.Errorf("no matching relationship found for condition: %s", conditionType)
}

// RetrieveWithConditionAndTargetLabels executes conditional query with explicit target label filter
// This is the enhanced version that accepts target labels from compound queries
func (r *ConditionalRetriever) RetrieveWithConditionAndTargetLabels(
	ctx context.Context,
	query string,
	anchor *tools.ConditionalAnchor,
	filters []tools.ConditionalFilter,
	targetLabels []string,
	topK int,
) ([]*schema.Document, error) {
	if topK <= 0 {
		topK = 50
	}

	// Step 1: Find anchor node
	r.emitDebug("conditional_step1_anchor_search", map[string]any{
		"keywords":        anchor.Keywords,
		"expected_labels": anchor.ExpectedLabels,
		"condition_type":  anchor.ConditionType,
		"target_labels":   targetLabels,
	})

	anchorNode, err := r.findAnchorNode(ctx, anchor)
	if err != nil {
		r.emitDebug("conditional_step1_anchor_not_found", map[string]any{
			"error": err.Error(),
		})
		return nil, fmt.Errorf("anchor not found: %w", err)
	}

	r.emitDebug("conditional_step1_anchor_found", map[string]any{
		"uuid":   anchorNode.UUID,
		"name":   anchorNode.Name,
		"labels": anchorNode.Labels,
	})

	// Step 2: Explore structure from anchor
	r.emitDebug("conditional_step2_explore", map[string]any{
		"anchor_uuid":   anchorNode.UUID,
		"anchor_name":   anchorNode.Name,
		"target_labels": targetLabels,
	})

	schemas, err := r.neo4jClient.GetRelationshipSchema(ctx, anchorNode.UUID)
	if err != nil {
		return nil, fmt.Errorf("schema exploration failed: %w", err)
	}

	r.emitDebug("conditional_step2_schema", map[string]any{
		"relationships_found": len(schemas),
		"schemas":             schemas,
	})

	if len(schemas) == 0 {
		return r.buildNoRelationshipsDocument(anchorNode)
	}

	// Step 3: Match condition to relationship WITH target label priority
	matchedRel, err := r.matchConditionToRelationshipWithTargets(anchor.ConditionType, schemas, targetLabels)
	if err != nil {
		return r.buildSchemaChoiceDocument(anchorNode, schemas)
	}

	r.emitDebug("conditional_step3_matched", map[string]any{
		"relationship":   matchedRel.RelType,
		"direction":      matchedRel.Direction,
		"target_label":   matchedRel.ConnectedLabel,
		"expected_count": matchedRel.Count,
	})

	// Step 4: Build property filters
	propFilters := make(map[string]any)
	for _, f := range filters {
		if f.FilterType == "property" {
			propFilters[f.Attribute] = f.Value
		}
	}

	// Step 5: Execute query
	results, total, err := r.neo4jClient.GetConnectedNodesByRelationship(
		ctx,
		anchorNode.UUID,
		matchedRel.RelType,
		matchedRel.Direction,
		matchedRel.ConnectedLabel,
		propFilters,
		topK,
	)
	if err != nil {
		return nil, fmt.Errorf("conditional query failed: %w", err)
	}

	r.emitDebug("conditional_step5_results", map[string]any{
		"results_count": len(results),
		"total_count":   total,
		"filters":       propFilters,
	})

	// Step 6: Format as documents
	return r.formatResultsAsDocuments(anchorNode, matchedRel, results, total)
}

// formatResultsAsDocuments converts query results to Eino documents
func (r *ConditionalRetriever) formatResultsAsDocuments(
	anchor *tools.NodeCandidate,
	rel *db.RelationshipSchemaInfo,
	results []map[string]any,
	total int,
) ([]*schema.Document, error) {
	var content strings.Builder

	// Header
	displayLabel := db.GetLabelDisplayName(rel.ConnectedLabel)
	content.WriteString(fmt.Sprintf("# %s 관련 %s 목록\n\n", anchor.Name, displayLabel))
	content.WriteString(fmt.Sprintf("**총 %d개** (관계: %s)\n\n", total, rel.RelType))

	// Table header
	content.WriteString("| # | 이름 | 상세 정보 |\n")
	content.WriteString("|---|------|----------|\n")

	for i, node := range results {
		name := ""
		if n, ok := node["name"].(string); ok {
			name = n
		}

		details := ""
		if props, ok := node["properties"].(map[string]any); ok {
			variantInfo := db.ExtractVariantInfo(props)
			if variantInfo != nil && !variantInfo.IsEmpty() {
				details = variantInfo.FormatDisplay()
			}
		}

		content.WriteString(fmt.Sprintf("| %d | %s | %s |\n", i+1, name, details))
	}

	if total > len(results) {
		content.WriteString(fmt.Sprintf("\n_... 외 %d개 더 있음_\n", total-len(results)))
	}

	doc := &schema.Document{
		ID:      fmt.Sprintf("conditional_%s_%s", anchor.UUID, rel.RelType),
		Content: content.String(),
		MetaData: map[string]any{
			"source":        "conditional_retriever",
			"anchor_uuid":   anchor.UUID,
			"anchor_name":   anchor.Name,
			"relationship":  rel.RelType,
			"target_label":  rel.ConnectedLabel,
			"total_count":   total,
			"shown_count":   len(results),
			"query_type":    "conditional",
			"exploration":   "success",
		},
	}

	return []*schema.Document{doc}, nil
}

// buildSchemaChoiceDocument creates a clarification document showing available paths
func (r *ConditionalRetriever) buildSchemaChoiceDocument(
	anchor *tools.NodeCandidate,
	schemas []db.RelationshipSchemaInfo,
) ([]*schema.Document, error) {
	var content strings.Builder

	content.WriteString(fmt.Sprintf("# %s에서 탐색 가능한 경로\n\n", anchor.Name))
	content.WriteString("다음 중 원하시는 정보를 선택해주세요:\n\n")

	for i, schema := range schemas {
		displayLabel := db.GetLabelDisplayName(schema.ConnectedLabel)
		direction := "→"
		if schema.Direction == "incoming" {
			direction = "←"
		}

		samples := ""
		maxSamples := 3
		if len(schema.SampleNodes) > 0 {
			showCount := len(schema.SampleNodes)
			if showCount > maxSamples {
				showCount = maxSamples
			}
			samples = fmt.Sprintf(" (예: %s)", strings.Join(schema.SampleNodes[:showCount], ", "))
		}

		content.WriteString(fmt.Sprintf("%d. %s %s [%s] %s - %d개 연결%s\n",
			i+1, anchor.Name, direction, schema.RelType, displayLabel, schema.Count, samples))
	}

	doc := &schema.Document{
		ID:      "conditional_clarification",
		Content: content.String(),
		MetaData: map[string]any{
			"source":              "conditional_clarification",
			"needs_clarification": true,
			"anchor_uuid":         anchor.UUID,
			"anchor_name":         anchor.Name,
			"available_schemas":   schemas,
		},
	}

	return []*schema.Document{doc}, nil
}

// buildNoRelationshipsDocument creates a document when no relationships are found
func (r *ConditionalRetriever) buildNoRelationshipsDocument(
	anchor *tools.NodeCandidate,
) ([]*schema.Document, error) {
	var content strings.Builder

	content.WriteString(fmt.Sprintf("# %s 검색 결과\n\n", anchor.Name))
	content.WriteString(fmt.Sprintf("**%s** 노드를 찾았으나 연결된 관계가 없습니다.\n\n", anchor.Name))

	if len(anchor.Labels) > 0 {
		content.WriteString(fmt.Sprintf("노드 유형: %s\n", strings.Join(anchor.Labels, ", ")))
	}

	doc := &schema.Document{
		ID:      fmt.Sprintf("conditional_no_relations_%s", anchor.UUID),
		Content: content.String(),
		MetaData: map[string]any{
			"source":       "conditional_retriever",
			"anchor_uuid":  anchor.UUID,
			"anchor_name":  anchor.Name,
			"no_relations": true,
		},
	}

	return []*schema.Document{doc}, nil
}

// emitDebug sends debug events if emitter is configured
func (r *ConditionalRetriever) emitDebug(event string, data map[string]any) {
	if r.debugEmitter != nil {
		r.debugEmitter("debug:conditional_retriever", DebugEvent{
			Type:    DebugEventStep,
			Message: event,
			Data:    data,
		})
	}
}

// RetrieveByRelationship is a helper method for direct relationship traversal
// Use this when you already know which relationship to follow
func (r *ConditionalRetriever) RetrieveByRelationship(
	ctx context.Context,
	anchorUUID string,
	anchorName string,
	relationship string,
	direction string,
	targetLabel string,
	filters map[string]any,
	limit int,
) ([]*schema.Document, error) {
	results, total, err := r.neo4jClient.GetConnectedNodesByRelationship(
		ctx,
		anchorUUID,
		relationship,
		direction,
		targetLabel,
		filters,
		limit,
	)
	if err != nil {
		return nil, err
	}

	anchor := &tools.NodeCandidate{
		UUID: anchorUUID,
		Name: anchorName,
	}

	rel := &db.RelationshipSchemaInfo{
		RelType:        relationship,
		Direction:      direction,
		ConnectedLabel: targetLabel,
		Count:          total,
	}

	return r.formatResultsAsDocuments(anchor, rel, results, total)
}
