package model

import "encoding/json"

type CreateSessionRequest struct {
	SystemPrompt string `json:"system_prompt,omitempty"`
}

type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type ChatPayload struct {
	Content string `json:"content"`
}

const (
	WSTypeChat  = "chat"
	WSTypePing  = "ping"
	WSTypeClear = "clear"
)
