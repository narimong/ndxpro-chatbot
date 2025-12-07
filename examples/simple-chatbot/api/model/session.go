package model

import (
	"time"

	"github.com/cloudwego/eino/schema"
)

// ClarificationPhase represents the phase of clarification
type ClarificationPhase string

const (
	PhaseInitialNode ClarificationPhase = "initial_node"  // Which entity?
	PhaseTargetLabel ClarificationPhase = "target_label"  // What info about entity?
)

// PendingClarification stores state for an ongoing clarification
type PendingClarification struct {
	RequestID      string             `json:"request_id"`
	Phase          ClarificationPhase `json:"phase"`
	OriginalQuery  string             `json:"original_query"`
	SourceNodeUUID string             `json:"source_node_uuid,omitempty"` // Set after Phase 1 resolved
	SourceNodeName string             `json:"source_node_name,omitempty"` // For display purposes
	CreatedAt      time.Time          `json:"created_at"`
}

type Session struct {
	ID                   string                `json:"session_id"`
	History              []*schema.Message     `json:"history"`
	SystemPrompt         string                `json:"system_prompt,omitempty"`
	PendingClarification *PendingClarification `json:"pending_clarification,omitempty"`
	CreatedAt            time.Time             `json:"created_at"`
	LastActivity         time.Time             `json:"last_activity"`
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
