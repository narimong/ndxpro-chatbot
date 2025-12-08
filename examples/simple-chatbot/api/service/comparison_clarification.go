// Package service provides the comparison clarification orchestrator for handling
// multi-entity comparison queries that require user disambiguation.
//
// # Overview
//
// The ComparisonClarificationOrchestrator manages the clarification process for
// comparison queries where one or more entities are ambiguous. It processes entities
// sequentially, asking for clarification when needed.
//
// # Flow
//
//	Query → Analyze → Entity 1 Clarification? → Entity 2 Clarification? → Execute
//
// # Usage
//
//	orchestrator := NewComparisonClarificationOrchestrator(config)
//	result, err := orchestrator.ProcessComparisonQuery(ctx, query, nil)
//	if result.NeedsClarification {
//	    // Send clarification request to user
//	    // Store result.PendingState for later
//	}
package service

import (
	"context"
	"fmt"
	"time"

	"simple-chatbot/api/service/tools"
)

// ComparisonClarificationOrchestrator manages the clarification process for comparison queries.
type ComparisonClarificationOrchestrator struct {
	graphRAGRetriever  *GraphRAGRetriever
	comparisonAnalyzer *tools.ComparisonAnalyzerTool
}

// ComparisonClarificationConfig holds configuration for the orchestrator.
type ComparisonClarificationConfig struct {
	GraphRAGRetriever  *GraphRAGRetriever
	ComparisonAnalyzer *tools.ComparisonAnalyzerTool
}

// NewComparisonClarificationOrchestrator creates a new comparison clarification orchestrator.
func NewComparisonClarificationOrchestrator(config *ComparisonClarificationConfig) *ComparisonClarificationOrchestrator {
	return &ComparisonClarificationOrchestrator{
		graphRAGRetriever:  config.GraphRAGRetriever,
		comparisonAnalyzer: config.ComparisonAnalyzer,
	}
}

// ProcessComparisonQuery processes a comparison query, handling clarification if needed.
// If pending is nil, this is a new query. If pending is provided, we're continuing
// after a previous clarification request.
func (o *ComparisonClarificationOrchestrator) ProcessComparisonQuery(
	ctx context.Context,
	query string,
	pending *ComparisonPendingClarification,
) (*ComparisonClarificationResult, error) {
	// If we have a pending clarification, continue from there
	if pending != nil {
		return o.continueResolution(ctx, pending)
	}

	// Analyze the query for comparison patterns
	analysis, err := o.comparisonAnalyzer.Analyze(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("comparison analysis failed: %w", err)
	}

	// Not a comparison query - return immediately
	if !analysis.IsComparison {
		return &ComparisonClarificationResult{
			IsComparison: false,
		}, nil
	}

	// Start the resolution process
	return o.startResolution(ctx, query, analysis)
}

// startResolution begins the entity resolution process for a new comparison query.
func (o *ComparisonClarificationOrchestrator) startResolution(
	ctx context.Context,
	query string,
	analysis *tools.ComparisonAnalysisOutput,
) (*ComparisonClarificationResult, error) {
	// Build the comparison query from analysis
	compQuery := &ComparisonQuery{
		OriginalQuery: query,
		QueryType:     "comparison",
		CompareAspect: analysis.CompareAspect,
		AnalysisGoal:  analysis.AnalysisGoal,
	}

	for i, e := range analysis.Entities {
		compQuery.Entities = append(compQuery.Entities, EntityQuery{
			EntityName:     e.Name,
			EntityKeywords: e.Keywords,
			TargetLabels:   e.ExpectedLabels,
			Role:           e.Role,
			Index:          i,
		})
	}

	// Create pending clarification state
	pending := &ComparisonPendingClarification{
		Phase:            PhaseEntityResolution,
		OriginalQuery:    query,
		ComparisonQuery:  compQuery,
		ResolvedEntities: make([]ResolvedEntity, 0),
		CurrentIndex:     0,
		CreatedAt:        time.Now(),
	}

	// Start resolving entities
	return o.resolveNextEntity(ctx, pending)
}

// continueResolution continues the resolution process after user clarification.
func (o *ComparisonClarificationOrchestrator) continueResolution(
	ctx context.Context,
	pending *ComparisonPendingClarification,
) (*ComparisonClarificationResult, error) {
	// Check for timeout (60 seconds)
	if time.Since(pending.CreatedAt) > 60*time.Second {
		return nil, fmt.Errorf("clarification timeout exceeded")
	}

	// Continue with next entity
	return o.resolveNextEntity(ctx, pending)
}

// resolveNextEntity attempts to resolve the next entity in the comparison.
func (o *ComparisonClarificationOrchestrator) resolveNextEntity(
	ctx context.Context,
	pending *ComparisonPendingClarification,
) (*ComparisonClarificationResult, error) {
	// Check if all entities have been resolved
	if pending.CurrentIndex >= len(pending.ComparisonQuery.Entities) {
		// All entities resolved - return success
		return &ComparisonClarificationResult{
			IsComparison:       true,
			NeedsClarification: false,
			ResolvedEntities:   pending.ResolvedEntities,
			ComparisonQuery:    pending.ComparisonQuery,
		}, nil
	}

	// Get the current entity to resolve
	entity := pending.ComparisonQuery.Entities[pending.CurrentIndex]

	// Search for candidates
	candidates, err := o.searchEntityCandidates(ctx, entity)
	if err != nil {
		return nil, fmt.Errorf("failed to search entity %s: %w", entity.EntityName, err)
	}

	// Check if clarification is needed
	needsClarification, reason := o.checkNeedsClarification(candidates)

	if needsClarification {
		// Build clarification options
		options := o.buildClarificationOptions(candidates)

		return &ComparisonClarificationResult{
			IsComparison:       true,
			NeedsClarification: true,
			ClarificationRequest: &ComparisonClarificationRequest{
				Type:    "comparison_entity",
				Phase:   fmt.Sprintf("entity_%d", pending.CurrentIndex+1),
				Entity:  entity.EntityName,
				Message: o.buildClarificationMessage(entity.EntityName, reason),
				Options: options,
				Reason:  reason,
			},
			PendingState: pending,
		}, nil
	}

	// No clarification needed - auto-select best candidate
	if len(candidates) > 0 {
		resolved := ResolvedEntity{
			OriginalName: entity.EntityName,
			ResolvedUUID: candidates[0].UUID,
			ResolvedName: candidates[0].Name,
			Role:         entity.Role,
		}
		pending.ResolvedEntities = append(pending.ResolvedEntities, resolved)
	} else {
		// No candidates found - use entity name as-is
		resolved := ResolvedEntity{
			OriginalName: entity.EntityName,
			ResolvedUUID: "",
			ResolvedName: entity.EntityName,
			Role:         entity.Role,
		}
		pending.ResolvedEntities = append(pending.ResolvedEntities, resolved)
	}

	// Move to next entity
	pending.CurrentIndex++

	// Continue with next entity
	return o.resolveNextEntity(ctx, pending)
}

// ProcessClarificationResponse handles the user's clarification response.
func (o *ComparisonClarificationOrchestrator) ProcessClarificationResponse(
	ctx context.Context,
	pending *ComparisonPendingClarification,
	selectedIndex int,
	selectedUUID string,
	selectedName string,
) (*ComparisonClarificationResult, error) {
	// Validate pending state
	if pending == nil {
		return nil, fmt.Errorf("no pending clarification state")
	}

	if pending.CurrentIndex >= len(pending.ComparisonQuery.Entities) {
		return nil, fmt.Errorf("invalid current index in pending state")
	}

	// Get the current entity
	entity := pending.ComparisonQuery.Entities[pending.CurrentIndex]

	// Store the resolved entity
	resolved := ResolvedEntity{
		OriginalName: entity.EntityName,
		ResolvedUUID: selectedUUID,
		ResolvedName: selectedName,
		Role:         entity.Role,
	}
	pending.ResolvedEntities = append(pending.ResolvedEntities, resolved)

	// Move to next entity
	pending.CurrentIndex++

	// Continue resolution
	return o.resolveNextEntity(ctx, pending)
}

// searchEntityCandidates searches for entity candidates using the GraphRAG retriever.
func (o *ComparisonClarificationOrchestrator) searchEntityCandidates(
	ctx context.Context,
	entity EntityQuery,
) ([]tools.NodeCandidate, error) {
	// Build search keywords
	keywords := entity.EntityKeywords
	if len(keywords) == 0 {
		keywords = []string{entity.EntityName}
	}

	// Search using the retriever
	candidates, err := o.graphRAGRetriever.SearchInitialNodes(ctx, entity.EntityName, keywords, 5)
	if err != nil {
		return nil, err
	}

	return candidates, nil
}

// checkNeedsClarification determines if user clarification is needed.
func (o *ComparisonClarificationOrchestrator) checkNeedsClarification(
	candidates []tools.NodeCandidate,
) (bool, string) {
	// No candidates found
	if len(candidates) == 0 {
		return true, "no_candidates"
	}

	// Only one candidate - no clarification needed
	if len(candidates) == 1 {
		return false, ""
	}

	// Check if scores are too close (ambiguous)
	if len(candidates) >= 2 {
		topScore := candidates[0].Score
		secondScore := candidates[1].Score

		// If top score is 0, all are equally unknown
		if topScore == 0 {
			return true, "ambiguous_scores"
		}

		// Calculate relative difference
		scoreDiff := (topScore - secondScore) / topScore
		if scoreDiff < 0.2 { // Less than 20% difference
			return true, "ambiguous_scores"
		}
	}

	// Check for multiple variants (same base name, different trims/engines)
	if o.hasMultipleVariants(candidates) {
		return true, "multiple_variants"
	}

	return false, ""
}

// hasMultipleVariants checks if candidates represent multiple variants of the same vehicle.
func (o *ComparisonClarificationOrchestrator) hasMultipleVariants(candidates []tools.NodeCandidate) bool {
	if len(candidates) < 2 {
		return false
	}

	// Check if multiple candidates have variant info
	variantCount := 0
	for _, c := range candidates {
		if c.VariantInfo != nil && (c.VariantInfo.Engine != "" || c.VariantInfo.Trim != "") {
			variantCount++
		}
	}

	// If most candidates have variants, it's likely multiple variants
	return variantCount >= 2
}

// buildClarificationOptions builds the clarification options from candidates.
func (o *ComparisonClarificationOrchestrator) buildClarificationOptions(candidates []tools.NodeCandidate) []ComparisonClarificationOption {
	options := make([]ComparisonClarificationOption, 0, len(candidates))

	for i, c := range candidates {
		option := ComparisonClarificationOption{
			Index: i,
			UUID:  c.UUID,
			Name:  c.Name,
		}

		// Add variant info if available
		if c.VariantInfo != nil {
			if c.VariantInfo.Engine != "" || c.VariantInfo.Trim != "" {
				details := ""
				if c.VariantInfo.Engine != "" {
					details = c.VariantInfo.Engine
				}
				if c.VariantInfo.Trim != "" {
					if details != "" {
						details += ", "
					}
					details += c.VariantInfo.Trim
				}
				if c.VariantInfo.ModelYear != "" {
					details += " (" + c.VariantInfo.ModelYear + ")"
				}
				option.Details = details
			}
		}

		// Add neighbor summary if available (convert []db.NeighborInfo to string)
		if len(c.NeighborSummary) > 0 {
			var contextParts []string
			for _, n := range c.NeighborSummary {
				contextParts = append(contextParts, fmt.Sprintf("%s: %s", n.Relationship, n.Name))
			}
			if len(contextParts) > 0 {
				option.Context = fmt.Sprintf("연결: %s", contextParts[0])
				if len(contextParts) > 1 {
					option.Context += fmt.Sprintf(" 외 %d개", len(contextParts)-1)
				}
			}
		}

		options = append(options, option)
	}

	return options
}

// buildClarificationMessage builds a user-friendly clarification message.
func (o *ComparisonClarificationOrchestrator) buildClarificationMessage(entityName string, reason string) string {
	switch reason {
	case "no_candidates":
		return fmt.Sprintf("'%s'을(를) 찾을 수 없습니다. 정확한 이름을 입력해주세요.", entityName)
	case "ambiguous_scores":
		return fmt.Sprintf("'%s'에 해당하는 여러 항목이 있습니다. 어떤 것을 비교할까요?", entityName)
	case "multiple_variants":
		return fmt.Sprintf("'%s'의 여러 변형(트림/엔진)이 있습니다. 어떤 것을 비교할까요?", entityName)
	default:
		return fmt.Sprintf("'%s'을(를) 명확히 해주세요.", entityName)
	}
}

// GetResolvedEntityByRole returns the resolved entity with the specified role.
func (p *ComparisonPendingClarification) GetResolvedEntityByRole(role string) *ResolvedEntity {
	for i := range p.ResolvedEntities {
		if p.ResolvedEntities[i].Role == role {
			return &p.ResolvedEntities[i]
		}
	}
	return nil
}

// GetReferenceEntity returns the resolved reference entity.
func (p *ComparisonPendingClarification) GetReferenceEntity() *ResolvedEntity {
	return p.GetResolvedEntityByRole(RoleReference)
}

// GetSubjectEntity returns the resolved subject entity.
func (p *ComparisonPendingClarification) GetSubjectEntity() *ResolvedEntity {
	return p.GetResolvedEntityByRole(RoleSubject)
}

// AllEntitiesResolved checks if all entities have been resolved.
func (p *ComparisonPendingClarification) AllEntitiesResolved() bool {
	if p.ComparisonQuery == nil {
		return false
	}
	return len(p.ResolvedEntities) >= len(p.ComparisonQuery.Entities)
}

// IsExpired checks if the clarification state has expired.
func (p *ComparisonPendingClarification) IsExpired() bool {
	return time.Since(p.CreatedAt) > 60*time.Second
}
