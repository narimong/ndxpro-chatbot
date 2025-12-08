// Package service provides the comparison graph service for executing multi-entity
// comparison queries using Eino's Graph with State management.
//
// # Overview
//
// The ComparisonGraphService uses Eino's Graph composition to execute parallel
// retrieval for multiple entities and then perform comparison analysis. It supports:
//   - Parallel entity retrieval using Graph's Pregel execution mode
//   - Thread-safe state management using compose.ProcessState
//   - Gap analysis, advantage analysis, and difference analysis
//
// # Architecture
//
//	START
//	  │
//	  ├──> retrieve_Entity1 ──┐
//	  │                       ├──> aggregate ──> analyze ──> END
//	  └──> retrieve_Entity2 ──┘
//
// # Usage
//
//	service := NewComparisonGraphService(config)
//	state, err := service.ProcessComparisonQuery(ctx, "Compare A and B")
//	if err != nil {
//	    // Handle error
//	}
//	// Use state.ComparisonData for results
package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"agent-chatbot/api/service/db"
	"agent-chatbot/api/service/tools"
)

// ComparisonGraphService handles multi-entity comparison queries using Eino Graph.
type ComparisonGraphService struct {
	graphRAGRetriever  *GraphRAGRetriever
	comparisonAnalyzer *tools.ComparisonAnalyzerTool
	clarificationOrch  *ComparisonClarificationOrchestrator
	chatModel          model.ChatModel
	neo4jClient        *db.Neo4jClient
	debugEmitter       DebugEmitter
}

// ComparisonGraphConfig holds configuration for the comparison graph service.
type ComparisonGraphConfig struct {
	GraphRAGRetriever *GraphRAGRetriever
	ChatModel         model.ChatModel
	Neo4jClient       *db.Neo4jClient
	DebugEmitter      DebugEmitter
}

// NewComparisonGraphService creates a new comparison graph service.
func NewComparisonGraphService(config *ComparisonGraphConfig) *ComparisonGraphService {
	comparisonAnalyzer := tools.NewComparisonAnalyzerTool(config.ChatModel)

	clarificationOrch := NewComparisonClarificationOrchestrator(&ComparisonClarificationConfig{
		GraphRAGRetriever:  config.GraphRAGRetriever,
		ComparisonAnalyzer: comparisonAnalyzer,
	})

	return &ComparisonGraphService{
		graphRAGRetriever:  config.GraphRAGRetriever,
		comparisonAnalyzer: comparisonAnalyzer,
		clarificationOrch:  clarificationOrch,
		chatModel:          config.ChatModel,
		neo4jClient:        config.Neo4jClient,
		debugEmitter:       config.DebugEmitter,
	}
}

// SetDebugEmitter sets the debug emitter for this service.
func (s *ComparisonGraphService) SetDebugEmitter(emitter DebugEmitter) {
	s.debugEmitter = emitter
}

// ProcessComparisonQuery processes a comparison query with clarification support.
// Returns nil if the query is not a comparison query.
func (s *ComparisonGraphService) ProcessComparisonQuery(
	ctx context.Context,
	query string,
	pending *ComparisonPendingClarification,
) (*ComparisonClarificationResult, error) {
	s.emitDebug("comparison_query_start", map[string]any{
		"query":       query,
		"has_pending": pending != nil,
	})

	// Use clarification orchestrator to handle entity resolution
	result, err := s.clarificationOrch.ProcessComparisonQuery(ctx, query, pending)
	if err != nil {
		return nil, fmt.Errorf("clarification processing failed: %w", err)
	}

	// Not a comparison query
	if !result.IsComparison {
		return result, nil
	}

	// Still needs clarification
	if result.NeedsClarification {
		s.emitDebug("comparison_needs_clarification", map[string]any{
			"entity":  result.ClarificationRequest.Entity,
			"phase":   result.ClarificationRequest.Phase,
			"reason":  result.ClarificationRequest.Reason,
			"options": len(result.ClarificationRequest.Options),
		})
		return result, nil
	}

	// All entities resolved - execute comparison graph
	s.emitDebug("comparison_entities_resolved", map[string]any{
		"entities": result.ResolvedEntities,
	})

	state, err := s.ExecuteWithResolvedEntities(ctx, result.ResolvedEntities, result.ComparisonQuery)
	if err != nil {
		return nil, fmt.Errorf("comparison graph execution failed: %w", err)
	}

	// Build result documents from comparison state
	documents := s.buildResultDocuments(state)

	return &ComparisonClarificationResult{
		IsComparison:       true,
		NeedsClarification: false,
		ResolvedEntities:   result.ResolvedEntities,
		ComparisonQuery:    result.ComparisonQuery,
		Documents:          documents,
	}, nil
}

// ProcessClarificationResponse processes a user's clarification response.
func (s *ComparisonGraphService) ProcessClarificationResponse(
	ctx context.Context,
	pending *ComparisonPendingClarification,
	selectedIndex int,
	selectedUUID string,
	selectedName string,
) (*ComparisonClarificationResult, error) {
	s.emitDebug("comparison_clarification_response", map[string]any{
		"selected_index": selectedIndex,
		"selected_uuid":  selectedUUID,
		"selected_name":  selectedName,
	})

	// Process the response through the orchestrator
	result, err := s.clarificationOrch.ProcessClarificationResponse(
		ctx, pending, selectedIndex, selectedUUID, selectedName,
	)
	if err != nil {
		return nil, err
	}

	// If still needs more clarification, return
	if result.NeedsClarification {
		return result, nil
	}

	// All entities resolved - execute comparison
	state, err := s.ExecuteWithResolvedEntities(ctx, result.ResolvedEntities, result.ComparisonQuery)
	if err != nil {
		return nil, fmt.Errorf("comparison graph execution failed: %w", err)
	}

	// Build result documents
	documents := s.buildResultDocuments(state)
	result.Documents = documents

	return result, nil
}

// ExecuteWithResolvedEntities executes the comparison graph with pre-resolved entities.
func (s *ComparisonGraphService) ExecuteWithResolvedEntities(
	ctx context.Context,
	resolvedEntities []ResolvedEntity,
	query *ComparisonQuery,
) (*ComparisonState, error) {
	s.emitDebug("comparison_graph_execute", map[string]any{
		"entity_count":   len(resolvedEntities),
		"compare_aspect": query.CompareAspect,
		"analysis_goal":  query.AnalysisGoal,
	})

	// Create initial state
	state := NewComparisonState(query)

	// Update state with resolved entity info
	for _, resolved := range resolvedEntities {
		if result, ok := state.EntityResults[resolved.OriginalName]; ok {
			result.SourceUUID = resolved.ResolvedUUID
			result.SourceName = resolved.ResolvedName
		}
	}

	// Execute parallel retrieval
	var wg sync.WaitGroup
	var mu sync.Mutex
	var retrievalErrors []error

	for _, resolved := range resolvedEntities {
		wg.Add(1)
		go func(entity ResolvedEntity) {
			defer wg.Done()

			err := s.retrieveEntityData(ctx, entity, state, &mu)
			if err != nil {
				mu.Lock()
				retrievalErrors = append(retrievalErrors, err)
				mu.Unlock()
			}
		}(resolved)
	}

	wg.Wait()

	// Check for errors
	if len(retrievalErrors) > 0 {
		s.emitDebug("comparison_retrieval_errors", map[string]any{
			"error_count": len(retrievalErrors),
			"errors":      fmt.Sprintf("%v", retrievalErrors),
		})
	}

	// Perform comparison analysis
	if state.AllEntitiesRetrieved() {
		s.analyzeComparison(state)
	}

	gapsFound := 0
	if state.ComparisonData != nil {
		gapsFound = len(state.ComparisonData.Gaps)
	}

	s.emitDebug("comparison_graph_complete", map[string]any{
		"all_retrieved": state.AllEntitiesRetrieved(),
		"has_errors":    state.HasErrors(),
		"gaps_found":    gapsFound,
	})

	return state, nil
}

// retrieveEntityData retrieves data for a single entity.
func (s *ComparisonGraphService) retrieveEntityData(
	ctx context.Context,
	entity ResolvedEntity,
	state *ComparisonState,
	mu *sync.Mutex,
) error {
	s.emitDebug("entity_retrieval_start", map[string]any{
		"entity":     entity.OriginalName,
		"resolved":   entity.ResolvedName,
		"source_uuid": entity.ResolvedUUID,
	})

	var docs []*schema.Document
	var err error

	// Determine target labels from query
	targetLabels := []string{"PerformanceTotalScore"}
	for _, eq := range state.Query.Entities {
		if eq.EntityName == entity.OriginalName && len(eq.TargetLabels) > 0 {
			targetLabels = eq.TargetLabels
			break
		}
	}

	// Retrieve hierarchical data if UUID is available
	if entity.ResolvedUUID != "" {
		docs, err = s.graphRAGRetriever.RetrieveWithHierarchyByUUID(
			ctx,
			entity.ResolvedUUID,
			entity.ResolvedName,
			targetLabels,
			4, // maxDepth
		)
	} else {
		// Fallback to regular retrieval
		docs, err = s.graphRAGRetriever.Retrieve(ctx, entity.OriginalName+" "+state.Query.CompareAspect)
	}

	// Update state
	mu.Lock()
	defer mu.Unlock()

	result := state.EntityResults[entity.OriginalName]
	if err != nil {
		result.Status = StatusError
		result.Error = err.Error()
		s.emitDebug("entity_retrieval_error", map[string]any{
			"entity": entity.OriginalName,
			"error":  err.Error(),
		})
		return err
	}

	result.Documents = docs
	result.Status = StatusRetrieved
	result.ScoreData = s.extractScoreData(docs)
	result.Hierarchy = s.buildHierarchyData(docs)

	s.emitDebug("entity_retrieval_complete", map[string]any{
		"entity":      entity.OriginalName,
		"docs_count":  len(docs),
		"score_count": len(result.ScoreData),
	})

	return nil
}

// extractScoreData extracts score data from documents.
func (s *ComparisonGraphService) extractScoreData(docs []*schema.Document) map[string]float64 {
	scores := make(map[string]float64)

	for _, doc := range docs {
		if doc.MetaData == nil {
			continue
		}

		// Try to extract scores from metadata
		if scoreMap, ok := doc.MetaData["scores"].(map[string]interface{}); ok {
			for category, value := range scoreMap {
				if floatVal, ok := value.(float64); ok {
					scores[category] = floatVal
				}
			}
		}

		// Try to extract from hierarchical data
		if hierarchy, ok := doc.MetaData["hierarchy"].(map[string]interface{}); ok {
			s.extractScoresFromHierarchy(hierarchy, scores, "")
		}

		// Parse content for score patterns if no metadata
		if len(scores) == 0 && doc.Content != "" {
			s.parseScoresFromContent(doc.Content, scores)
		}
	}

	return scores
}

// extractScoresFromHierarchy recursively extracts scores from hierarchy data.
func (s *ComparisonGraphService) extractScoresFromHierarchy(
	hierarchy map[string]interface{},
	scores map[string]float64,
	prefix string,
) {
	for key, value := range hierarchy {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "/" + key
		}

		switch v := value.(type) {
		case float64:
			scores[fullKey] = v
		case map[string]interface{}:
			// Check for score value
			if score, ok := v["score"].(float64); ok {
				scores[fullKey] = score
			}
			// Recurse into children
			if children, ok := v["children"].(map[string]interface{}); ok {
				s.extractScoresFromHierarchy(children, scores, fullKey)
			}
		}
	}
}

// parseScoresFromContent parses score data from document content.
func (s *ComparisonGraphService) parseScoresFromContent(content string, scores map[string]float64) {
	// Parse markdown table format
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		// Skip header and separator lines
		if strings.HasPrefix(line, "|--") || strings.HasPrefix(line, "| Category") {
			continue
		}

		// Parse table rows like "| 가속 | 42 | 50 | 84% |"
		if strings.HasPrefix(line, "|") {
			parts := strings.Split(line, "|")
			if len(parts) >= 3 {
				category := strings.TrimSpace(parts[1])
				if category != "" && category != "Category" {
					// Try to parse score from second column
					var score float64
					if _, err := fmt.Sscanf(strings.TrimSpace(parts[2]), "%f", &score); err == nil {
						scores[category] = score
					}
				}
			}
		}
	}
}

// buildHierarchyData builds hierarchy data from documents.
func (s *ComparisonGraphService) buildHierarchyData(docs []*schema.Document) *HierarchyData {
	if len(docs) == 0 {
		return nil
	}

	hierarchy := &HierarchyData{
		Categories: make(map[string]*CategoryScore),
	}

	for _, doc := range docs {
		if doc.MetaData == nil {
			continue
		}

		// Extract hierarchy from metadata
		if h, ok := doc.MetaData["hierarchy"].(*HierarchyData); ok {
			return h
		}

		// Try to build from other metadata
		if rootName, ok := doc.MetaData["root_name"].(string); ok {
			hierarchy.RootName = rootName
		}
		if totalScore, ok := doc.MetaData["total_score"].(float64); ok {
			hierarchy.TotalScore = totalScore
		}
		if maxScore, ok := doc.MetaData["max_score"].(float64); ok {
			hierarchy.MaxScore = maxScore
		}
	}

	return hierarchy
}

// analyzeComparison performs the comparison analysis.
func (s *ComparisonGraphService) analyzeComparison(state *ComparisonState) {
	refEntity := state.Query.GetReferenceEntity()
	subjEntity := state.Query.GetSubjectEntity()

	if refEntity == nil || subjEntity == nil {
		return
	}

	refResult := state.EntityResults[refEntity.EntityName]
	subjResult := state.EntityResults[subjEntity.EntityName]

	if refResult == nil || subjResult == nil {
		return
	}

	analysis := &ComparisonAnalysisData{
		ReferenceEntity: refEntity.EntityName,
		SubjectEntity:   subjEntity.EntityName,
		Advantages:      make([]ComparisonPoint, 0),
		Disadvantages:   make([]ComparisonPoint, 0),
		Gaps:            make([]GapAnalysis, 0),
	}

	// Compare scores
	for category, refValue := range refResult.ScoreData {
		subjValue, ok := subjResult.ScoreData[category]
		if !ok {
			continue
		}

		diff := subjValue - refValue
		point := ComparisonPoint{
			Category:     category,
			RefValue:     refValue,
			SubjValue:    subjValue,
			Difference:   diff,
			Significance: DetermineSignificance(diff),
		}

		if diff > 0 {
			analysis.Advantages = append(analysis.Advantages, point)
		} else if diff < 0 {
			analysis.Disadvantages = append(analysis.Disadvantages, point)

			// Create gap analysis for the analysis goal
			if state.Query.AnalysisGoal == AnalysisGoalGap || state.Query.AnalysisGoal == AnalysisGoalDifference {
				gap := GapAnalysis{
					Category:       category,
					Gap:            math.Abs(diff),
					Priority:       DeterminePriority(math.Abs(diff)),
					Recommendation: s.generateRecommendation(category, math.Abs(diff)),
				}
				analysis.Gaps = append(analysis.Gaps, gap)
			}
		}
	}

	// Sort gaps by priority (high priority first)
	sort.Slice(analysis.Gaps, func(i, j int) bool {
		return analysis.Gaps[i].Priority < analysis.Gaps[j].Priority
	})

	// Generate summary
	analysis.Summary = s.generateSummary(analysis, state.Query.AnalysisGoal)

	state.ComparisonData = analysis
}

// generateRecommendation generates a recommendation for a gap.
func (s *ComparisonGraphService) generateRecommendation(category string, gap float64) string {
	priorityStr := "낮음"
	if gap >= 5 {
		priorityStr = "높음"
	} else if gap >= 2 {
		priorityStr = "중간"
	}

	return fmt.Sprintf("%s 항목에서 %.1f점 개선 필요 (우선순위: %s)", category, gap, priorityStr)
}

// generateSummary generates a natural language summary of the comparison.
func (s *ComparisonGraphService) generateSummary(analysis *ComparisonAnalysisData, goal string) string {
	var summary strings.Builder

	summary.WriteString(fmt.Sprintf("%s 대비 %s 분석 결과:\n",
		analysis.SubjectEntity, analysis.ReferenceEntity))

	switch goal {
	case AnalysisGoalGap:
		summary.WriteString(fmt.Sprintf("- 보완 필요 항목: %d개\n", len(analysis.Gaps)))
		if len(analysis.Gaps) > 0 {
			highPriority := 0
			for _, gap := range analysis.Gaps {
				if gap.Priority == PriorityHigh {
					highPriority++
				}
			}
			if highPriority > 0 {
				summary.WriteString(fmt.Sprintf("- 높은 우선순위 항목: %d개\n", highPriority))
			}
		}

	case AnalysisGoalAdvantage:
		summary.WriteString(fmt.Sprintf("- 우위 항목: %d개\n", len(analysis.Advantages)))

	case AnalysisGoalDifference:
		summary.WriteString(fmt.Sprintf("- 우위 항목: %d개\n", len(analysis.Advantages)))
		summary.WriteString(fmt.Sprintf("- 열위 항목: %d개\n", len(analysis.Disadvantages)))

	default:
		summary.WriteString(fmt.Sprintf("- 장점: %d개, 개선필요: %d개\n",
			len(analysis.Advantages), len(analysis.Gaps)))
	}

	return summary.String()
}

// buildResultDocuments builds schema.Document objects from comparison state.
func (s *ComparisonGraphService) buildResultDocuments(state *ComparisonState) []*schema.Document {
	docs := make([]*schema.Document, 0)

	// Add individual entity documents
	for _, result := range state.EntityResults {
		if result.Status == StatusRetrieved {
			docs = append(docs, result.Documents...)
		}
	}

	// Add comparison summary document
	if state.ComparisonData != nil {
		summaryDoc := &schema.Document{
			ID:      "comparison_summary",
			Content: s.buildComparisonContent(state),
			MetaData: map[string]any{
				"type":             "comparison_summary",
				"reference_entity": state.ComparisonData.ReferenceEntity,
				"subject_entity":   state.ComparisonData.SubjectEntity,
				"analysis_goal":    state.Query.AnalysisGoal,
				"gaps_count":       len(state.ComparisonData.Gaps),
				"advantages_count": len(state.ComparisonData.Advantages),
			},
		}
		docs = append([]*schema.Document{summaryDoc}, docs...)
	}

	return docs
}

// buildComparisonContent builds the comparison content string.
func (s *ComparisonGraphService) buildComparisonContent(state *ComparisonState) string {
	var content strings.Builder

	analysis := state.ComparisonData
	if analysis == nil {
		return ""
	}

	// Header
	content.WriteString(fmt.Sprintf("## %s vs %s 비교 분석\n\n",
		analysis.ReferenceEntity, analysis.SubjectEntity))

	// Summary
	content.WriteString("### 요약\n")
	content.WriteString(analysis.Summary)
	content.WriteString("\n")

	// Comparison table
	if len(analysis.Advantages) > 0 || len(analysis.Disadvantages) > 0 {
		content.WriteString("### 상세 비교\n\n")
		content.WriteString("| 항목 | " + analysis.ReferenceEntity + " | " + analysis.SubjectEntity + " | 차이 | 평가 |\n")
		content.WriteString("|------|------|------|------|------|\n")

		// Advantages (subject is better)
		for _, point := range analysis.Advantages {
			content.WriteString(fmt.Sprintf("| %s | %.1f | %.1f | +%.1f | 우위 |\n",
				point.Category, point.RefValue, point.SubjValue, point.Difference))
		}

		// Disadvantages (subject is worse)
		for _, point := range analysis.Disadvantages {
			content.WriteString(fmt.Sprintf("| %s | %.1f | %.1f | %.1f | 열위 |\n",
				point.Category, point.RefValue, point.SubjValue, point.Difference))
		}

		content.WriteString("\n")
	}

	// Gap analysis
	if state.Query.AnalysisGoal == AnalysisGoalGap && len(analysis.Gaps) > 0 {
		content.WriteString(fmt.Sprintf("### %s 보완 필요 항목\n\n", analysis.SubjectEntity))

		for i, gap := range analysis.Gaps {
			priorityEmoji := "🔴"
			if gap.Priority == PriorityMedium {
				priorityEmoji = "🟡"
			} else if gap.Priority == PriorityLow {
				priorityEmoji = "🟢"
			}

			content.WriteString(fmt.Sprintf("%d. %s %s (%.1f점 차이)\n",
				i+1, priorityEmoji, gap.Category, gap.Gap))
			content.WriteString(fmt.Sprintf("   - %s\n", gap.Recommendation))
		}
	}

	return content.String()
}

// BuildComparisonContext builds the context string for LLM response generation.
func (s *ComparisonGraphService) BuildComparisonContext(state *ComparisonState) string {
	return s.buildComparisonContent(state)
}

// emitDebug emits a debug event if an emitter is configured.
func (s *ComparisonGraphService) emitDebug(event string, data map[string]any) {
	if s.debugEmitter != nil {
		s.debugEmitter("debug:comparison_graph", DebugEvent{
			Type:    DebugEventStep,
			Message: event,
			Data:    data,
		})
	}
}

// GetClarificationOrchestrator returns the clarification orchestrator.
func (s *ComparisonGraphService) GetClarificationOrchestrator() *ComparisonClarificationOrchestrator {
	return s.clarificationOrch
}

// GetComparisonAnalyzer returns the comparison analyzer.
func (s *ComparisonGraphService) GetComparisonAnalyzer() *tools.ComparisonAnalyzerTool {
	return s.comparisonAnalyzer
}
