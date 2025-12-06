package handler

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	socketio "github.com/doquangtan/socket.io/v4"
	"github.com/google/uuid"

	"github.com/cloudwego/eino/schema"

	"simple-chatbot/api/service"
)

type SocketIOHandler struct {
	io           *socketio.Io
	sessionStore *service.SessionStore
	chatService  *service.ChatService
	socketSessions sync.Map // socketId -> sessionId
}

func NewSocketIOHandler(sessionStore *service.SessionStore, chatService *service.ChatService) *SocketIOHandler {
	io := socketio.New()

	h := &SocketIOHandler{
		io:           io,
		sessionStore: sessionStore,
		chatService:  chatService,
	}

	h.setup()
	return h
}

func (h *SocketIOHandler) setup() {
	h.io.OnConnection(func(socket *socketio.Socket) {
		log.Printf("Client connected: %s", socket.Id)

		socket.On("join", func(event *socketio.EventPayload) {
			h.handleJoin(socket, event)
		})

		socket.On("chat", func(event *socketio.EventPayload) {
			h.handleChat(socket, event)
		})

		socket.On("clear", func(event *socketio.EventPayload) {
			h.handleClear(socket)
		})

		socket.On("disconnect", func(event *socketio.EventPayload) {
			h.socketSessions.Delete(socket.Id)
			log.Printf("Client disconnected: %s", socket.Id)
		})
	})
}

func (h *SocketIOHandler) handleJoin(socket *socketio.Socket, event *socketio.EventPayload) {
	if len(event.Data) == 0 {
		socket.Emit("error", map[string]string{
			"code":    "INVALID_MESSAGE",
			"message": "No session_id provided",
		})
		return
	}

	data, ok := event.Data[0].(map[string]interface{})
	if !ok {
		socket.Emit("error", map[string]string{
			"code":    "INVALID_MESSAGE",
			"message": "Invalid message format",
		})
		return
	}

	sessionID, ok := data["session_id"].(string)
	if !ok || sessionID == "" {
		socket.Emit("error", map[string]string{
			"code":    "INVALID_MESSAGE",
			"message": "No session_id provided",
		})
		return
	}

	if _, err := h.sessionStore.Get(sessionID); err != nil {
		socket.Emit("error", map[string]string{
			"code":    "SESSION_NOT_FOUND",
			"message": "Session not found",
		})
		return
	}

	h.socketSessions.Store(socket.Id, sessionID)
	log.Printf("Socket %s joined session %s", socket.Id, sessionID)

	socket.Emit("joined", map[string]string{
		"session_id": sessionID,
	})
}

func (h *SocketIOHandler) getSessionID(socket *socketio.Socket) (string, bool) {
	val, ok := h.socketSessions.Load(socket.Id)
	if !ok {
		return "", false
	}
	return val.(string), true
}

func (h *SocketIOHandler) handleChat(socket *socketio.Socket, event *socketio.EventPayload) {
	sessionID, ok := h.getSessionID(socket)
	if !ok {
		socket.Emit("error", map[string]string{
			"code":    "NOT_JOINED",
			"message": "Please join a session first",
		})
		return
	}

	if len(event.Data) == 0 {
		socket.Emit("error", map[string]string{
			"code":    "INVALID_MESSAGE",
			"message": "Empty message",
		})
		return
	}

	data, ok := event.Data[0].(map[string]interface{})
	if !ok {
		socket.Emit("error", map[string]string{
			"code":    "INVALID_MESSAGE",
			"message": "Invalid message format",
		})
		return
	}

	content, ok := data["content"].(string)
	if !ok || strings.TrimSpace(content) == "" {
		socket.Emit("error", map[string]string{
			"code":    "INVALID_MESSAGE",
			"message": "Empty message content",
		})
		return
	}

	ctx := context.Background()
	messageID := uuid.New().String()

	streamReader, err := h.chatService.ChatStream(ctx, sessionID, content)
	if err != nil {
		socket.Emit("error", map[string]string{
			"code":    "CHAT_ERROR",
			"message": err.Error(),
		})
		return
	}
	defer streamReader.Close()

	var fullContent strings.Builder

	for {
		chunk, err := streamReader.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			socket.Emit("error", map[string]string{
				"code":    "STREAM_ERROR",
				"message": err.Error(),
			})
			return
		}

		if chunk.Content != "" {
			fullContent.WriteString(chunk.Content)
			socket.Emit("chunk", map[string]string{
				"content":    chunk.Content,
				"message_id": messageID,
			})
		}
	}

	if err := h.sessionStore.AppendHistory(sessionID,
		schema.UserMessage(content),
		schema.AssistantMessage(fullContent.String(), nil),
	); err != nil {
		log.Printf("Failed to append history: %v", err)
	}

	socket.Emit("done", map[string]string{
		"message_id":   messageID,
		"full_content": fullContent.String(),
	})
}

func (h *SocketIOHandler) handleClear(socket *socketio.Socket) {
	sessionID, ok := h.getSessionID(socket)
	if !ok {
		socket.Emit("error", map[string]string{
			"code":    "NOT_JOINED",
			"message": "Please join a session first",
		})
		return
	}

	if err := h.sessionStore.ClearHistory(sessionID); err != nil {
		socket.Emit("error", map[string]string{
			"code":    "SESSION_ERROR",
			"message": err.Error(),
		})
		return
	}
	socket.Emit("cleared", nil)
}

func (h *SocketIOHandler) Handler() http.Handler {
	return h.io.HttpHandler()
}
