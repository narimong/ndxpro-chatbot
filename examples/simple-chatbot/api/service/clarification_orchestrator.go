package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"simple-chatbot/api/model"
	"simple-chatbot/api/service/db"
	"simple-chatbot/api/service/tools"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const (
	// ClarificationTimeout is the maximum time to wait for user clarification
	ClarificationTimeout = 60 * time.Second
	// MinSearchConfidence is the minimum confidence score for search results
	MinSearchConfidence = 0.3
)

// ClarificationDecision represents the result of query processing
type ClarificationDecision struct {
	NeedsClarification   bool                   `json:"needs_clarification"`
	ClarificationRequest *ClarificationRequest  `json:"clarification_request,omitempty"`
	Phase                model.ClarificationPhase `json:"phase,omitempty"`
	Documents            []*schema.Document     `json:"documents,omitempty"`
	SourceNodeUUID       string                 `json:"source_node_uuid,omitempty"`
	SourceNodeName       string                 `json:"source_node_name,omitempty"`
	Analysis             *tools.QueryAnalysisOutput `json:"analysis,omitempty"`
}

// ClarificationOrchestrator orchestrates the clarification flow
type ClarificationOrchestrator struct {
	clarificationSvc  *ClarificationService
	graphRAGRetriever *GraphRAGRetriever
	neo4jClient       *db.Neo4jClient
	debugEmitter      DebugEmitter
}

// ClarificationOrchestratorConfig holds configuration for ClarificationOrchestrator
type ClarificationOrchestratorConfig struct {
	ClarificationService *ClarificationService
	GraphRAGRetriever    *GraphRAGRetriever
	Neo4jClient          *db.Neo4jClient
	DebugEmitter         DebugEmitter
}

// NewClarificationOrchestrator creates a new clarification orchestrator
func NewClarificationOrchestrator(config *ClarificationOrchestratorConfig) *ClarificationOrchestrator {
	return &ClarificationOrchestrator{
		clarificationSvc:  config.ClarificationService,
		graphRAGRetriever: config.GraphRAGRetriever,
		neo4jClient:       config.Neo4jClient,
		debugEmitter:      config.DebugEmitter,
	}
}

// ProcessQuery processes a query and determines if clarification is needed
func (o *ClarificationOrchestrator) ProcessQuery(
	ctx context.Context,
	query string,
	pending *model.PendingClarification,
) (*ClarificationDecision, error) {
	// If there's a pending clarification from Phase 1, skip to Phase 2
	if pending != nil && pending.Phase == model.PhaseInitialNode && pending.SourceNodeUUID != "" {
		return o.processPhase2(ctx, query, pending)
	}

	// Step 1: Query Analysis
	o.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: "Orchestrator: Analyzing query",
		Data:    map[string]any{"query": query},
	})

	queryAnalyzer := o.clarificationSvc.GetQueryAnalyzer()
	if queryAnalyzer == nil {
		// No query analyzer available, fallback to regular retrieval
		return o.fallbackToRegularRetrieval(ctx, query)
	}

	analysis, err := queryAnalyzer.Analyze(ctx, query)
	if err != nil {
		o.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: fmt.Sprintf("Query analysis failed: %v", err),
		})
		return o.fallbackToRegularRetrieval(ctx, query)
	}

	o.emitDebug(DebugEvent{
		Type:    DebugEventPlan,
		Message: fmt.Sprintf("Analysis: confidence=%.2f, start_keywords=%v, target_labels=%v",
			analysis.Confidence, analysis.StartEntity.Keywords, analysis.Target.ExpectedLabels),
		Data: map[string]any{"analysis": analysis},
	})

	// Step 2: Check Phase 1 - Initial Node Clarification
	needsInitialNode, candidates := o.checkInitialNodeClarification(ctx, analysis, query)
	if needsInitialNode {
		return o.buildInitialNodeDecision(ctx, query, analysis, candidates)
	}

	// We have a valid source node
	sourceNode := candidates[0]
	o.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: fmt.Sprintf("Source node identified: %s (UUID: %s)", sourceNode.Name, sourceNode.UUID),
	})

	// Step 3: Check Phase 2 - Target Label Clarification
	needsTargetLabel := o.checkTargetLabelClarification(analysis)
	if needsTargetLabel {
		return o.buildTargetLabelDecision(ctx, query, analysis, sourceNode)
	}

	// Step 4: Both phases clear - proceed with retrieval
	return o.proceedWithRetrieval(ctx, query, analysis, sourceNode)
}

// ProcessClarificationResponse processes the user's clarification response
func (o *ClarificationOrchestrator) ProcessClarificationResponse(
	ctx context.Context,
	pending *model.PendingClarification,
	selectedID string,
	freeText string,
	originalRequest *ClarificationRequest,
) (*ClarificationDecision, error) {
	switch pending.Phase {
	case model.PhaseInitialNode:
		return o.resolveInitialNodeClarification(ctx, pending, selectedID, freeText, originalRequest)
	case model.PhaseTargetLabel:
		return o.resolveTargetLabelClarification(ctx, pending, selectedID, freeText, originalRequest)
	default:
		return nil, fmt.Errorf("unknown clarification phase: %s", pending.Phase)
	}
}

// checkInitialNodeClarification checks if initial node clarification is needed
func (o *ClarificationOrchestrator) checkInitialNodeClarification(
	ctx context.Context,
	analysis *tools.QueryAnalysisOutput,
	query string,
) (needsClarification bool, candidates []tools.NodeCandidate) {
	// Case 1: No keywords extracted
	if len(analysis.StartEntity.Keywords) == 0 {
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: "No keywords extracted from query",
		})
		return true, nil
	}

	// Case 2: Try full-text search
	candidates, err := o.graphRAGRetriever.SearchInitialNodes(ctx, query, analysis.StartEntity.Keywords, 10)
	if err != nil {
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: fmt.Sprintf("Search failed: %v", err),
		})
		return true, nil
	}

	// Case 3: No results
	if len(candidates) == 0 {
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: "No candidates found in search",
		})
		return true, nil
	}

	// Case 4: Low confidence results (top result score < threshold)
	topScore := candidates[0].Score
	if topScore < MinSearchConfidence {
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: fmt.Sprintf("Low confidence results: top score %.2f < %.2f", topScore, MinSearchConfidence),
			Data:    map[string]any{"candidates": candidates[:min(5, len(candidates))]},
		})
		return true, candidates
	}

	// Case 5: Multiple ambiguous results (top 2 scores are close)
	if len(candidates) > 1 {
		secondScore := candidates[1].Score
		if topScore > 0 && (topScore-secondScore)/topScore < 0.2 {
			o.emitDebug(DebugEvent{
				Type:    DebugEventClarification,
				Message: fmt.Sprintf("Ambiguous results: scores %.2f and %.2f are close", topScore, secondScore),
			})
			// Only clarify if both scores are relatively low
			if topScore < 0.7 {
				return true, candidates
			}
		}
	}

	// Case 6: Multiple variants of same vehicle name (e.g., Tucson with different engines/trims)
	if len(candidates) > 1 && o.hasSameNameVariants(candidates) {
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: fmt.Sprintf("Multiple variants found for same vehicle: %s", candidates[0].Name),
			Data:    map[string]any{"variants_count": len(candidates)},
		})
		return true, candidates
	}

	return false, candidates
}

// hasSameNameVariants checks if candidates have the same name (different variants)
// e.g., two Tucsons with different engines (1.6T gsl vs 1.6T dsl)
func (o *ClarificationOrchestrator) hasSameNameVariants(candidates []tools.NodeCandidate) bool {
	if len(candidates) < 2 {
		return false
	}

	// Count candidates with same name as first candidate
	firstName := candidates[0].Name
	sameNameCount := 0
	for _, c := range candidates {
		if c.Name == firstName {
			sameNameCount++
		}
	}

	// If 2+ candidates have same name, they are variants
	return sameNameCount >= 2
}

// detectHierarchicalQuery checks if the query asks for hierarchical/detailed data
// Keywords like "하위", "세부", "모두", "전체", "상세" indicate hierarchical queries
func (o *ClarificationOrchestrator) detectHierarchicalQuery(query string) bool {
	hierarchicalKeywords := []string{
		"하위", "세부", "모두", "전체", "상세",
		"breakdown", "details", "all", "complete",
	}
	queryLower := strings.ToLower(query)
	for _, kw := range hierarchicalKeywords {
		if strings.Contains(queryLower, kw) {
			return true
		}
	}
	return false
}

// checkTargetLabelClarification checks if target label clarification is needed
func (o *ClarificationOrchestrator) checkTargetLabelClarification(analysis *tools.QueryAnalysisOutput) bool {
	// Case 1: No target labels extracted
	if len(analysis.Target.ExpectedLabels) == 0 {
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: "No target labels extracted",
		})
		return true
	}

	// Case 2: Low confidence
	if analysis.Confidence < 0.5 {
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: fmt.Sprintf("Low confidence: %.2f < 0.5", analysis.Confidence),
		})
		return true
	}

	return false
}

// enrichCandidatesWithNeighborInfo adds 1-hop neighbor info to candidates for better context
// This provides generic context for any node type, not just vehicles
func (o *ClarificationOrchestrator) enrichCandidatesWithNeighborInfo(
	ctx context.Context,
	candidates []tools.NodeCandidate,
) []tools.NodeCandidate {
	if o.neo4jClient == nil {
		return candidates
	}

	for i := range candidates {
		// Skip if variant info is already sufficient
		if candidates[i].VariantInfo != nil && !candidates[i].VariantInfo.IsEmpty() {
			continue
		}

		// Get 1-hop neighbors for additional context
		neighbors, err := o.neo4jClient.GetOneHopNeighborSummary(ctx, candidates[i].UUID, 5)
		if err == nil && len(neighbors) > 0 {
			candidates[i].NeighborSummary = neighbors
		}
	}

	return candidates
}

// buildInitialNodeDecision builds a clarification decision for initial node selection
func (o *ClarificationOrchestrator) buildInitialNodeDecision(
	ctx context.Context,
	query string,
	analysis *tools.QueryAnalysisOutput,
	candidates []tools.NodeCandidate,
) (*ClarificationDecision, error) {
	requestID := uuid.New().String()

	// Enrich candidates with 1-hop neighbor info for better disambiguation
	enrichedCandidates := o.enrichCandidatesWithNeighborInfo(ctx, candidates)

	req := o.clarificationSvc.BuildInitialNodeRequest(ctx, query, enrichedCandidates, requestID)

	return &ClarificationDecision{
		NeedsClarification:   true,
		ClarificationRequest: req,
		Phase:                model.PhaseInitialNode,
		Analysis:             analysis,
	}, nil
}

// buildTargetLabelDecision builds a clarification decision for target label selection
func (o *ClarificationOrchestrator) buildTargetLabelDecision(
	ctx context.Context,
	query string,
	analysis *tools.QueryAnalysisOutput,
	sourceNode tools.NodeCandidate,
) (*ClarificationDecision, error) {
	requestID := uuid.New().String()

	// Get reachable labels from the source node
	reachableLabels, err := o.neo4jClient.GetReachableLabels(ctx, sourceNode.UUID, 4)
	if err != nil {
		o.emitDebug(DebugEvent{
			Type:    DebugEventStep,
			Message: fmt.Sprintf("Failed to get reachable labels: %v", err),
		})
		reachableLabels = nil
	}

	req := o.clarificationSvc.BuildTargetLabelRequest(ctx, query, sourceNode, reachableLabels, requestID)

	return &ClarificationDecision{
		NeedsClarification:   true,
		ClarificationRequest: req,
		Phase:                model.PhaseTargetLabel,
		SourceNodeUUID:       sourceNode.UUID,
		SourceNodeName:       sourceNode.Name,
		Analysis:             analysis,
	}, nil
}

// resolveInitialNodeClarification resolves initial node selection
func (o *ClarificationOrchestrator) resolveInitialNodeClarification(
	ctx context.Context,
	pending *model.PendingClarification,
	selectedID string,
	freeText string,
	originalRequest *ClarificationRequest,
) (*ClarificationDecision, error) {
	var sourceUUID, sourceName string

	if selectedID != "" && originalRequest != nil {
		// Find selected option
		for _, opt := range originalRequest.Options {
			if opt.ID == selectedID {
				sourceUUID = opt.TargetLabel // UUID is stored in TargetLabel for initial node options
				sourceName = opt.Label
				break
			}
		}
	}

	if sourceUUID == "" && freeText != "" {
		// Try to search with free text
		candidates, err := o.graphRAGRetriever.SearchInitialNodes(ctx, freeText, []string{freeText}, 3)
		if err == nil && len(candidates) > 0 {
			sourceUUID = candidates[0].UUID
			sourceName = candidates[0].Name
		}
	}

	if sourceUUID == "" {
		// Still no source - fallback to regular retrieval with original query
		return o.fallbackToRegularRetrieval(ctx, pending.OriginalQuery)
	}

	// Now check if we need Phase 2 (target label clarification)
	queryAnalyzer := o.clarificationSvc.GetQueryAnalyzer()
	analysis, err := queryAnalyzer.Analyze(ctx, pending.OriginalQuery)
	if err != nil {
		// Proceed with all reachable labels
		analysis = &tools.QueryAnalysisOutput{
			Confidence: 0.3,
		}
	}

	// Create a mock source node for Phase 2 check
	sourceNode := tools.NodeCandidate{
		UUID: sourceUUID,
		Name: sourceName,
	}

	// Check if target label clarification is needed
	if o.checkTargetLabelClarification(analysis) {
		return o.buildTargetLabelDecision(ctx, pending.OriginalQuery, analysis, sourceNode)
	}

	// Both phases clear - proceed with retrieval
	return o.proceedWithRetrieval(ctx, pending.OriginalQuery, analysis, sourceNode)
}

// resolveTargetLabelClarification resolves target label selection
func (o *ClarificationOrchestrator) resolveTargetLabelClarification(
	ctx context.Context,
	pending *model.PendingClarification,
	selectedID string,
	freeText string,
	originalRequest *ClarificationRequest,
) (*ClarificationDecision, error) {
	var targetLabels []string

	if selectedID != "" && originalRequest != nil {
		// Find selected option
		for _, opt := range originalRequest.Options {
			if opt.ID == selectedID {
				targetLabels = []string{opt.TargetLabel}
				break
			}
		}
	}

	if len(targetLabels) == 0 && freeText != "" {
		// Use LLM to interpret free text
		result, err := o.clarificationSvc.ResolveClarification(ctx, originalRequest, &ClarificationResponse{
			RequestID: pending.RequestID,
			FreeText:  freeText,
		})
		if err == nil && len(result.TargetLabels) > 0 {
			targetLabels = result.TargetLabels
		}
	}

	if len(targetLabels) == 0 {
		// Fallback: get all reachable labels
		reachableLabels, _ := o.neo4jClient.GetReachableLabels(ctx, pending.SourceNodeUUID, 4)
		if len(reachableLabels) > 0 {
			targetLabels = reachableLabels[:min(3, len(reachableLabels))]
		}
	}

	// Check if original query asks for hierarchical data
	isHierarchical := o.detectHierarchicalQuery(pending.OriginalQuery)

	var docs []*schema.Document
	var err error

	if isHierarchical && len(targetLabels) > 0 {
		// Use hierarchical retrieval for "하위 점수 모두" type queries
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: fmt.Sprintf("Using hierarchical retrieval for query: %s", pending.OriginalQuery),
			Data:    map[string]any{"target_labels": targetLabels},
		})
		docs, err = o.graphRAGRetriever.RetrieveWithHierarchyByUUID(
			ctx,
			pending.SourceNodeUUID,
			pending.SourceNodeName,
			targetLabels,
			4, // maxDepth = 4 levels
		)
	} else {
		// Use standard retrieval
		docs, err = o.graphRAGRetriever.RetrieveWithSourceAndTargets(
			ctx,
			pending.SourceNodeUUID,
			targetLabels,
			5,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}

	return &ClarificationDecision{
		NeedsClarification: false,
		Documents:          docs,
		SourceNodeUUID:     pending.SourceNodeUUID,
		SourceNodeName:     pending.SourceNodeName,
	}, nil
}

// proceedWithRetrieval proceeds with retrieval when no clarification is needed
func (o *ClarificationOrchestrator) proceedWithRetrieval(
	ctx context.Context,
	query string,
	analysis *tools.QueryAnalysisOutput,
	sourceNode tools.NodeCandidate,
) (*ClarificationDecision, error) {
	// Check if query asks for hierarchical data
	isHierarchical := o.detectHierarchicalQuery(query)

	o.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: "Proceeding with retrieval",
		Data: map[string]any{
			"source":         sourceNode.Name,
			"target_labels":  analysis.Target.ExpectedLabels,
			"is_hierarchical": isHierarchical,
		},
	})

	var docs []*schema.Document
	var err error

	if isHierarchical && len(analysis.Target.ExpectedLabels) > 0 {
		// Use hierarchical retrieval for queries asking for detailed data
		o.emitDebug(DebugEvent{
			Type:    DebugEventClarification,
			Message: fmt.Sprintf("Using hierarchical retrieval for query: %s", query),
			Data:    map[string]any{"target_labels": analysis.Target.ExpectedLabels},
		})
		docs, err = o.graphRAGRetriever.RetrieveWithHierarchyByUUID(
			ctx,
			sourceNode.UUID,
			sourceNode.Name,
			analysis.Target.ExpectedLabels,
			4, // maxDepth = 4 levels
		)
	} else {
		// Use standard retrieval
		docs, err = o.graphRAGRetriever.RetrieveWithSourceAndTargets(
			ctx,
			sourceNode.UUID,
			analysis.Target.ExpectedLabels,
			5,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}

	return &ClarificationDecision{
		NeedsClarification: false,
		Documents:          docs,
		SourceNodeUUID:     sourceNode.UUID,
		SourceNodeName:     sourceNode.Name,
		Analysis:           analysis,
	}, nil
}

// processPhase2 processes Phase 2 when we already have a source node from Phase 1
func (o *ClarificationOrchestrator) processPhase2(
	ctx context.Context,
	query string,
	pending *model.PendingClarification,
) (*ClarificationDecision, error) {
	// Re-analyze the query to check target labels
	queryAnalyzer := o.clarificationSvc.GetQueryAnalyzer()
	analysis, err := queryAnalyzer.Analyze(ctx, query)
	if err != nil {
		analysis = &tools.QueryAnalysisOutput{Confidence: 0.3}
	}

	sourceNode := tools.NodeCandidate{
		UUID: pending.SourceNodeUUID,
		Name: pending.SourceNodeName,
	}

	// Check if target label clarification is needed
	if o.checkTargetLabelClarification(analysis) {
		return o.buildTargetLabelDecision(ctx, query, analysis, sourceNode)
	}

	// Proceed with retrieval
	return o.proceedWithRetrieval(ctx, query, analysis, sourceNode)
}

// fallbackToRegularRetrieval falls back to regular retrieval without clarification
func (o *ClarificationOrchestrator) fallbackToRegularRetrieval(
	ctx context.Context,
	query string,
) (*ClarificationDecision, error) {
	o.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: "Falling back to regular retrieval",
	})

	docs, err := o.graphRAGRetriever.Retrieve(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("fallback retrieval failed: %w", err)
	}

	return &ClarificationDecision{
		NeedsClarification: false,
		Documents:          docs,
	}, nil
}

// CheckPendingClarificationTimeout checks if a pending clarification has timed out
func (o *ClarificationOrchestrator) CheckPendingClarificationTimeout(pending *model.PendingClarification) bool {
	if pending == nil {
		return false
	}
	return time.Since(pending.CreatedAt) > ClarificationTimeout
}

// HandleClarificationTimeout handles a timed out clarification by falling back to regular retrieval
func (o *ClarificationOrchestrator) HandleClarificationTimeout(
	ctx context.Context,
	pending *model.PendingClarification,
) (*ClarificationDecision, error) {
	o.emitDebug(DebugEvent{
		Type:    DebugEventStep,
		Message: "Clarification timed out, falling back to regular retrieval",
	})
	return o.fallbackToRegularRetrieval(ctx, pending.OriginalQuery)
}

// emitDebug emits a debug event if emitter is set
func (o *ClarificationOrchestrator) emitDebug(event DebugEvent) {
	if o.debugEmitter != nil {
		o.debugEmitter("debug:clarification_orchestrator", event)
	}
}

// SerializeClarificationRequest serializes a clarification request to JSON for storage
func SerializeClarificationRequest(req *ClarificationRequest) (string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DeserializeClarificationRequest deserializes a clarification request from JSON
func DeserializeClarificationRequest(data string) (*ClarificationRequest, error) {
	var req ClarificationRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		return nil, err
	}
	return &req, nil
}

// Helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
