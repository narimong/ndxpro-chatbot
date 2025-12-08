// Package service provides comparison query processing types and structures.
//
// # Overview
//
// This file defines the core data structures for handling multi-entity comparison
// queries such as:
//
//	"POLO와 Tucson의 성능 평가 점수를 비교해서 Tucson이 보완해야 할 부분에 대해 알려줘"
//
// # Architecture
//
// The comparison system uses the following key structures:
//   - ComparisonQuery: Represents a decomposed comparison query with entities and goals
//   - ComparisonState: Manages state during Eino Graph execution
//   - ComparisonAnalysisData: Holds the final comparison analysis results
//   - ComparisonPendingClarification: Tracks clarification state for multi-entity queries
//
// # Usage
//
//	query := &ComparisonQuery{
//	    OriginalQuery: "Compare A and B",
//	    Entities: []EntityQuery{{EntityName: "A", Role: "reference"}, {EntityName: "B", Role: "subject"}},
//	    CompareAspect: "performance scores",
//	    AnalysisGoal: "gap_analysis",
//	}
package service

import (
	"time"

	"github.com/cloudwego/eino/schema"
)

// ComparisonQuery represents a decomposed comparison query.
// It contains information about the entities being compared and the analysis goal.
type ComparisonQuery struct {
	// OriginalQuery is the original user query string
	OriginalQuery string `json:"original_query"`

	// QueryType identifies this as a comparison query (always "comparison")
	QueryType string `json:"query_type"`

	// Entities contains the list of entities to compare
	Entities []EntityQuery `json:"entities"`

	// CompareAspect describes what aspect is being compared (e.g., "성능 평가 점수")
	CompareAspect string `json:"compare_aspect"`

	// AnalysisGoal specifies the type of analysis to perform
	// Possible values: "gap_analysis", "advantage_analysis", "difference_analysis", "similarity_analysis"
	AnalysisGoal string `json:"analysis_goal"`
}

// EntityQuery represents a single entity in a comparison query.
type EntityQuery struct {
	// EntityName is the name of the entity (e.g., "POLO", "Tucson")
	EntityName string `json:"entity_name"`

	// EntityKeywords are alternative keywords to search for this entity
	EntityKeywords []string `json:"entity_keywords"`

	// TargetLabels are the Neo4j labels to search for (e.g., ["Vehicle"])
	TargetLabels []string `json:"target_labels"`

	// Role indicates the entity's role in the comparison
	// "reference" = the baseline entity (usually mentioned first)
	// "subject" = the entity being analyzed/improved
	Role string `json:"role"`

	// Index is the position of this entity in the comparison query
	Index int `json:"index"`
}

// ComparisonState holds the accumulated state during Eino Graph execution.
// It is thread-safe and managed by Eino's state management system.
type ComparisonState struct {
	// Query contains the decomposed comparison query
	Query *ComparisonQuery `json:"query"`

	// EntityResults maps entity names to their retrieval results
	EntityResults map[string]*EntityResult `json:"entity_results"`

	// ComparisonData contains the final comparison analysis
	ComparisonData *ComparisonAnalysisData `json:"comparison_data,omitempty"`

	// NeedsClarification indicates if user clarification is needed
	NeedsClarification bool `json:"needs_clarification"`

	// ClarificationFor indicates which entity needs clarification
	ClarificationFor string `json:"clarification_for,omitempty"`
}

// EntityResult holds the retrieval results for a single entity.
type EntityResult struct {
	// EntityName is the name of the entity
	EntityName string `json:"entity_name"`

	// SourceUUID is the Neo4j UUID of the resolved entity node
	SourceUUID string `json:"source_uuid"`

	// SourceName is the full name of the resolved entity (including variant info)
	SourceName string `json:"source_name"`

	// Documents contains the retrieved documents for this entity
	Documents []*schema.Document `json:"documents"`

	// ScoreData maps category names to their score values
	ScoreData map[string]float64 `json:"score_data,omitempty"`

	// Hierarchy contains hierarchical score data if available
	Hierarchy *HierarchyData `json:"hierarchy,omitempty"`

	// Status indicates the retrieval status
	// Possible values: "pending", "retrieved", "error"
	Status string `json:"status"`

	// Error contains error message if Status is "error"
	Error string `json:"error,omitempty"`
}

// HierarchyData represents hierarchical score structure.
type HierarchyData struct {
	// RootName is the name of the root score category
	RootName string `json:"root_name"`

	// TotalScore is the overall score value
	TotalScore float64 `json:"total_score"`

	// MaxScore is the maximum possible score
	MaxScore float64 `json:"max_score"`

	// Categories maps category names to their scores
	Categories map[string]*CategoryScore `json:"categories"`
}

// CategoryScore represents a score category with optional sub-scores.
type CategoryScore struct {
	// Name is the category name
	Name string `json:"name"`

	// Value is the category's score value
	Value float64 `json:"value"`

	// MaxValue is the maximum possible score for this category
	MaxValue float64 `json:"max_value"`

	// Ratio is the percentage (Value/MaxValue * 100)
	Ratio float64 `json:"ratio"`

	// SubScores maps sub-category names to their score values
	SubScores map[string]float64 `json:"sub_scores,omitempty"`
}

// ComparisonAnalysisData holds the results of the comparison analysis.
type ComparisonAnalysisData struct {
	// ReferenceEntity is the name of the reference (baseline) entity
	ReferenceEntity string `json:"reference_entity"`

	// SubjectEntity is the name of the entity being analyzed
	SubjectEntity string `json:"subject_entity"`

	// Advantages lists items where subject outperforms reference
	Advantages []ComparisonPoint `json:"advantages"`

	// Disadvantages lists items where subject underperforms reference
	Disadvantages []ComparisonPoint `json:"disadvantages"`

	// Gaps contains detailed gap analysis for improvements
	Gaps []GapAnalysis `json:"gaps"`

	// Summary is a natural language summary of the comparison
	Summary string `json:"summary"`
}

// ComparisonPoint represents a single comparison item.
type ComparisonPoint struct {
	// Category is the main category name
	Category string `json:"category"`

	// SubCategory is the optional sub-category name
	SubCategory string `json:"sub_category,omitempty"`

	// RefValue is the reference entity's score
	RefValue float64 `json:"ref_value"`

	// SubjValue is the subject entity's score
	SubjValue float64 `json:"subj_value"`

	// Difference is SubjValue - RefValue
	Difference float64 `json:"difference"`

	// Significance indicates the importance of this difference
	// Possible values: "major" (>=5), "minor" (1-5), "negligible" (<1)
	Significance string `json:"significance"`
}

// GapAnalysis represents an area needing improvement.
type GapAnalysis struct {
	// Category is the category name where improvement is needed
	Category string `json:"category"`

	// SubCategory is the optional sub-category name
	SubCategory string `json:"sub_category,omitempty"`

	// Gap is the absolute difference (always positive)
	Gap float64 `json:"gap"`

	// Recommendation is a suggested improvement action
	Recommendation string `json:"recommendation"`

	// Priority indicates urgency (1=high, 2=medium, 3=low)
	Priority int `json:"priority"`
}

// ComparisonClarificationPhase represents the current phase of comparison clarification.
type ComparisonClarificationPhase int

const (
	// PhaseEntityResolution indicates we're resolving entity ambiguity
	PhaseEntityResolution ComparisonClarificationPhase = iota + 1

	// PhaseTargetResolution indicates we're resolving target label ambiguity
	PhaseTargetResolution

	// PhaseComplete indicates all clarifications are resolved
	PhaseComplete
)

// String returns the string representation of the phase.
func (p ComparisonClarificationPhase) String() string {
	switch p {
	case PhaseEntityResolution:
		return "entity_resolution"
	case PhaseTargetResolution:
		return "target_resolution"
	case PhaseComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// ComparisonPendingClarification tracks the state of pending clarifications.
type ComparisonPendingClarification struct {
	// Phase indicates the current clarification phase
	Phase ComparisonClarificationPhase `json:"phase"`

	// OriginalQuery is the original user query
	OriginalQuery string `json:"original_query"`

	// ComparisonQuery contains the decomposed query
	ComparisonQuery *ComparisonQuery `json:"comparison_query"`

	// ResolvedEntities contains entities that have been resolved
	ResolvedEntities []ResolvedEntity `json:"resolved_entities"`

	// CurrentIndex is the index of the entity currently being processed
	CurrentIndex int `json:"current_index"`

	// CreatedAt is when this clarification state was created
	CreatedAt time.Time `json:"created_at"`
}

// ResolvedEntity represents an entity that has been disambiguated.
type ResolvedEntity struct {
	// OriginalName is the name as it appeared in the query
	OriginalName string `json:"original_name"`

	// ResolvedUUID is the Neo4j UUID of the resolved node
	ResolvedUUID string `json:"resolved_uuid"`

	// ResolvedName is the full name including variant info
	ResolvedName string `json:"resolved_name"`

	// Role is the entity's role in the comparison
	Role string `json:"role"`
}

// ComparisonClarificationResult is returned by the clarification orchestrator.
type ComparisonClarificationResult struct {
	// IsComparison indicates if the query is a comparison query
	IsComparison bool `json:"is_comparison"`

	// NeedsClarification indicates if user input is needed
	NeedsClarification bool `json:"needs_clarification"`

	// ClarificationRequest contains the clarification request if needed
	ClarificationRequest *ComparisonClarificationRequest `json:"clarification_request,omitempty"`

	// PendingState contains the state to be stored for continuation
	PendingState *ComparisonPendingClarification `json:"pending_state,omitempty"`

	// ResolvedEntities contains all resolved entities when complete
	ResolvedEntities []ResolvedEntity `json:"resolved_entities,omitempty"`

	// ComparisonQuery contains the decomposed query
	ComparisonQuery *ComparisonQuery `json:"comparison_query,omitempty"`

	// Documents contains retrieved documents when clarification is complete
	Documents []*schema.Document `json:"documents,omitempty"`
}

// ComparisonClarificationRequest represents a request for user clarification.
type ComparisonClarificationRequest struct {
	// Type is the type of clarification needed
	// Possible values: "comparison_entity", "comparison_target"
	Type string `json:"type"`

	// Phase identifies which entity is being clarified (e.g., "entity_1", "entity_2")
	Phase string `json:"phase"`

	// Entity is the name of the entity needing clarification
	Entity string `json:"entity"`

	// Message is the user-facing clarification message
	Message string `json:"message"`

	// Options contains the available choices
	Options []ComparisonClarificationOption `json:"options"`

	// Reason explains why clarification is needed
	// Possible values: "no_candidates", "ambiguous_scores", "multiple_variants"
	Reason string `json:"reason"`
}

// ComparisonClarificationOption represents a single option for comparison clarification.
type ComparisonClarificationOption struct {
	// Index is the option index for selection
	Index int `json:"index"`

	// UUID is the Neo4j UUID of the candidate node
	UUID string `json:"uuid"`

	// Name is the display name of the candidate
	Name string `json:"name"`

	// Details provides additional information (engine, trim, year)
	Details string `json:"details,omitempty"`

	// Context provides neighbor summary or other context
	Context string `json:"context,omitempty"`
}

// AnalysisGoal constants for comparison analysis types.
const (
	// AnalysisGoalGap identifies gap analysis (find areas needing improvement)
	AnalysisGoalGap = "gap_analysis"

	// AnalysisGoalAdvantage identifies advantage analysis (find strengths)
	AnalysisGoalAdvantage = "advantage_analysis"

	// AnalysisGoalDifference identifies difference analysis (find all differences)
	AnalysisGoalDifference = "difference_analysis"

	// AnalysisGoalSimilarity identifies similarity analysis (find common points)
	AnalysisGoalSimilarity = "similarity_analysis"
)

// EntityRole constants for entity roles in comparison.
const (
	// RoleReference is the baseline entity for comparison
	RoleReference = "reference"

	// RoleSubject is the entity being analyzed/improved
	RoleSubject = "subject"
)

// EntityStatus constants for retrieval status.
const (
	// StatusPending indicates retrieval has not started
	StatusPending = "pending"

	// StatusRetrieved indicates retrieval completed successfully
	StatusRetrieved = "retrieved"

	// StatusError indicates retrieval failed
	StatusError = "error"
)

// Significance constants for comparison point significance.
const (
	// SignificanceMajor indicates a major difference (>=5 points)
	SignificanceMajor = "major"

	// SignificanceMinor indicates a minor difference (1-5 points)
	SignificanceMinor = "minor"

	// SignificanceNegligible indicates a negligible difference (<1 point)
	SignificanceNegligible = "negligible"
)

// Priority constants for gap analysis priority.
const (
	// PriorityHigh indicates high priority (gap >= 5)
	PriorityHigh = 1

	// PriorityMedium indicates medium priority (gap 2-5)
	PriorityMedium = 2

	// PriorityLow indicates low priority (gap < 2)
	PriorityLow = 3
)

// Helper functions

// DeterminePriority returns the priority based on gap size.
func DeterminePriority(gap float64) int {
	if gap >= 5 {
		return PriorityHigh
	}
	if gap >= 2 {
		return PriorityMedium
	}
	return PriorityLow
}

// DetermineSignificance returns the significance based on difference.
func DetermineSignificance(diff float64) string {
	absDiff := diff
	if absDiff < 0 {
		absDiff = -absDiff
	}

	if absDiff >= 5 {
		return SignificanceMajor
	}
	if absDiff >= 1 {
		return SignificanceMinor
	}
	return SignificanceNegligible
}

// NewComparisonState creates a new ComparisonState with initialized maps.
func NewComparisonState(query *ComparisonQuery) *ComparisonState {
	state := &ComparisonState{
		Query:         query,
		EntityResults: make(map[string]*EntityResult),
		ComparisonData: &ComparisonAnalysisData{
			Advantages:    make([]ComparisonPoint, 0),
			Disadvantages: make([]ComparisonPoint, 0),
			Gaps:          make([]GapAnalysis, 0),
		},
	}

	// Initialize entity results for each entity
	for _, entity := range query.Entities {
		state.EntityResults[entity.EntityName] = &EntityResult{
			EntityName: entity.EntityName,
			Status:     StatusPending,
			ScoreData:  make(map[string]float64),
		}
	}

	return state
}

// GetReferenceEntity returns the reference entity from the comparison query.
func (q *ComparisonQuery) GetReferenceEntity() *EntityQuery {
	for i := range q.Entities {
		if q.Entities[i].Role == RoleReference {
			return &q.Entities[i]
		}
	}
	// Default to first entity if no explicit reference
	if len(q.Entities) > 0 {
		return &q.Entities[0]
	}
	return nil
}

// GetSubjectEntity returns the subject entity from the comparison query.
func (q *ComparisonQuery) GetSubjectEntity() *EntityQuery {
	for i := range q.Entities {
		if q.Entities[i].Role == RoleSubject {
			return &q.Entities[i]
		}
	}
	// Default to second entity if no explicit subject
	if len(q.Entities) > 1 {
		return &q.Entities[1]
	}
	return nil
}

// AllEntitiesRetrieved checks if all entities have been successfully retrieved.
func (s *ComparisonState) AllEntitiesRetrieved() bool {
	for _, result := range s.EntityResults {
		if result.Status != StatusRetrieved {
			return false
		}
	}
	return true
}

// HasErrors checks if any entity retrieval failed.
func (s *ComparisonState) HasErrors() bool {
	for _, result := range s.EntityResults {
		if result.Status == StatusError {
			return true
		}
	}
	return false
}

// GetErrorMessages returns all error messages from failed retrievals.
func (s *ComparisonState) GetErrorMessages() []string {
	var errors []string
	for _, result := range s.EntityResults {
		if result.Status == StatusError && result.Error != "" {
			errors = append(errors, result.Error)
		}
	}
	return errors
}
