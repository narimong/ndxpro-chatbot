package model

import (
	"time"

	"github.com/cloudwego/eino/schema"
)

// ClarificationPhase represents the phase of clarification
type ClarificationPhase string

const (
	PhaseInitialNode  ClarificationPhase = "initial_node"   // Which entity?
	PhaseTargetLabel  ClarificationPhase = "target_label"   // What info about entity?
	PhaseLabelListing ClarificationPhase = "label_listing"  // Which label type to list?
	PhaseConditional  ClarificationPhase = "conditional"    // Which relationship to follow?
)

// PendingClarification stores state for an ongoing clarification
type PendingClarification struct {
	RequestID      string             `json:"request_id"`
	Phase          ClarificationPhase `json:"phase"`
	OriginalQuery  string             `json:"original_query"`
	SourceNodeUUID string             `json:"source_node_uuid,omitempty"` // Set after Phase 1 resolved
	SourceNodeName string             `json:"source_node_name,omitempty"` // For display purposes
	CreatedAt      time.Time          `json:"created_at"`

	// Label Listing specific fields (for PhaseLabelListing)
	DetectedTerm    string   `json:"detected_term,omitempty"`    // Original term detected (e.g., "경쟁차")
	SuggestedLabels []string `json:"suggested_labels,omitempty"` // Suggested labels from semantic mapping
	SelectedLabel   string   `json:"selected_label,omitempty"`   // User-selected label

	// Conditional query specific fields (for PhaseConditional)
	AnchorUUID           string   `json:"anchor_uuid,omitempty"`            // Anchor node UUID
	AnchorName           string   `json:"anchor_name,omitempty"`            // Anchor node name
	AvailableRelations   []string `json:"available_relations,omitempty"`    // Available relationship types
	SelectedRelation     string   `json:"selected_relation,omitempty"`      // User-selected relationship
	ConditionalDirection string   `json:"conditional_direction,omitempty"`  // "incoming" or "outgoing"
	TargetLabel          string   `json:"target_label,omitempty"`           // Target label for traversal
}

// LabelListingPendingClarification stores state for label listing clarification
type LabelListingPendingClarification struct {
	RequestID       string    `json:"request_id"`
	OriginalQuery   string    `json:"original_query"`
	DetectedTerm    string    `json:"detected_term"`     // Original semantic term (e.g., "경쟁차")
	SuggestedLabels []string  `json:"suggested_labels"`  // Mapped labels from semantic term
	AllLabels       []string  `json:"all_labels"`        // All available listable labels
	CreatedAt       time.Time `json:"created_at"`
}

// ComparisonPendingClarification stores state for an ongoing comparison clarification.
// This is used when multiple entities need disambiguation during a comparison query.
type ComparisonPendingClarification struct {
	Phase            int                      `json:"phase"`              // Current phase (1=entity_resolution, 2=target_resolution)
	OriginalQuery    string                   `json:"original_query"`     // Original comparison query
	CurrentIndex     int                      `json:"current_index"`      // Index of entity being clarified
	ResolvedEntities []ResolvedEntityModel    `json:"resolved_entities"`  // Resolved entities with full info
	ComparisonQuery  *ComparisonQueryModel    `json:"comparison_query"`   // The decomposed comparison query
	CreatedAt        time.Time                `json:"created_at"`
}

// ResolvedEntityModel stores information about a resolved entity.
type ResolvedEntityModel struct {
	OriginalName string `json:"original_name"`
	ResolvedUUID string `json:"resolved_uuid"`
	ResolvedName string `json:"resolved_name"`
	Role         string `json:"role"`
}

// ComparisonQueryModel stores the decomposed comparison query.
type ComparisonQueryModel struct {
	OriginalQuery string             `json:"original_query"`
	QueryType     string             `json:"query_type"`
	Entities      []EntityQueryModel `json:"entities"`
	CompareAspect string             `json:"compare_aspect"`
	AnalysisGoal  string             `json:"analysis_goal"`
}

// EntityQueryModel stores information about an entity in the comparison.
type EntityQueryModel struct {
	EntityName     string   `json:"entity_name"`
	EntityKeywords []string `json:"entity_keywords"`
	TargetLabels   []string `json:"target_labels"`
	Role           string   `json:"role"`
	Index          int      `json:"index"`
}

type Session struct {
	ID                             string                          `json:"session_id"`
	History                        []*schema.Message               `json:"history"`
	SystemPrompt                   string                          `json:"system_prompt,omitempty"`
	PendingClarification           *PendingClarification           `json:"pending_clarification,omitempty"`
	PendingComparisonClarification *ComparisonPendingClarification `json:"pending_comparison_clarification,omitempty"`
	CreatedAt                      time.Time                       `json:"created_at"`
	LastActivity                   time.Time                       `json:"last_activity"`
}

type SessionResponse struct {
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
}

type SessionDetailResponse struct {
	SessionID    string            `json:"session_id"`
	History      []*schema.Message `json:"history"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	LastActivity time.Time         `json:"last_activity"`
}

type SessionListResponse struct {
	Sessions []*SessionResponse `json:"sessions"`
}
