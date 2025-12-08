package model

import "time"

type WSResponse struct {
	Type      string      `json:"type"`
	Payload   interface{} `json:"payload,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

type ChunkPayload struct {
	Content   string `json:"content"`
	MessageID string `json:"message_id"`
}

type DonePayload struct {
	MessageID   string `json:"message_id"`
	FullContent string `json:"full_content"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	WSRespTypeChunk   = "chunk"
	WSRespTypeDone    = "done"
	WSRespTypeError   = "error"
	WSRespTypePong    = "pong"
	WSRespTypeCleared = "cleared"
)

func NewWSResponse(typ string, payload interface{}) *WSResponse {
	return &WSResponse{
		Type:      typ,
		Payload:   payload,
		Timestamp: time.Now().UnixMilli(),
	}
}

func NewChunkResponse(messageID, content string) *WSResponse {
	return NewWSResponse(WSRespTypeChunk, ChunkPayload{
		Content:   content,
		MessageID: messageID,
	})
}

func NewDoneResponse(messageID, fullContent string) *WSResponse {
	return NewWSResponse(WSRespTypeDone, DonePayload{
		MessageID:   messageID,
		FullContent: fullContent,
	})
}

func NewErrorResponse(code, message string) *WSResponse {
	return NewWSResponse(WSRespTypeError, ErrorPayload{
		Code:    code,
		Message: message,
	})
}

func NewPongResponse() *WSResponse {
	return NewWSResponse(WSRespTypePong, nil)
}

func NewClearedResponse() *WSResponse {
	return NewWSResponse(WSRespTypeCleared, nil)
}
