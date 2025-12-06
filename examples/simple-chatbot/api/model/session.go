package model

import (
	"time"

	"github.com/cloudwego/eino/schema"
)

type Session struct {
	ID           string            `json:"session_id"`
	History      []*schema.Message `json:"history"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	LastActivity time.Time         `json:"last_activity"`
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
