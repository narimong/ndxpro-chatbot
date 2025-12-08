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
	io             *socketio.Io
	sessionStore   *service.SessionStore
	chatService    *service.ChatService
	socketSessions sync.Map // socketId -> sessionId
	// Store pending clarification requests per session for response handling
	pendingClarificationRequests sync.Map // sessionId -> *service.ClarificationRequest
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

		// Clarification response handler
		socket.On("clarification:response", func(event *socketio.EventPayload) {
			h.handleClarificationResponse(socket, event)
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

	// Check if debug mode is enabled
	debugMode := false
	if debug, ok := data["debug"].(bool); ok {
		debugMode = debug
	}

	ctx := context.Background()
	messageID := uuid.New().String()

	var streamReader *schema.StreamReader[*schema.Message]
	var err error

	// Create debug emitter for clarification request storage
	var pendingClarificationReq *service.ClarificationRequest
	emitter := func(event string, eventData interface{}) {
		socket.Emit(event, eventData)
		// Store clarification request if emitted
		if event == "clarification:request" {
			log.Printf("[DEBUG] Received clarification:request event, eventData type: %T", eventData)
			if req, ok := eventData.(*service.ClarificationRequest); ok {
				pendingClarificationReq = req
				log.Printf("[DEBUG] Stored pending clarification request: %s", req.RequestID)
			} else {
				log.Printf("[DEBUG] Failed to cast eventData to *ClarificationRequest")
			}
		}
	}

	if debugMode {
		streamReader, err = h.chatService.ChatStreamWithDebug(ctx, sessionID, content, emitter)
	} else {
		streamReader, err = h.chatService.ChatStream(ctx, sessionID, content)
	}

	if err != nil {
		// Check if this is a clarification request (not an actual error)
		if err.Error() == "clarification_required" {
			// Store the pending clarification request for response handling
			if pendingClarificationReq != nil {
				h.pendingClarificationRequests.Store(sessionID, pendingClarificationReq)
			}
			// The clarification request was already emitted via emitter
			// Don't send an error - client will handle clarification:request event
			return
		}

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

// handleClarificationResponse handles user's response to a clarification request
func (h *SocketIOHandler) handleClarificationResponse(socket *socketio.Socket, event *socketio.EventPayload) {
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
			"message": "Empty clarification response",
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

	// Parse clarification response
	response := &service.ClarificationResponse{}

	if requestID, ok := data["request_id"].(string); ok {
		response.RequestID = requestID
	}
	if selectedID, ok := data["selected_id"].(string); ok {
		response.SelectedID = selectedID
	}
	if freeText, ok := data["free_text"].(string); ok {
		response.FreeText = freeText
	}

	// Check if debug mode is enabled
	debugMode := false
	if debug, ok := data["debug"].(bool); ok {
		debugMode = debug
	}

	// Get the pending clarification request
	pendingReqVal, ok := h.pendingClarificationRequests.Load(sessionID)
	if !ok {
		socket.Emit("error", map[string]string{
			"code":    "NO_PENDING_CLARIFICATION",
			"message": "No pending clarification request for this session",
		})
		return
	}
	pendingReq := pendingReqVal.(*service.ClarificationRequest)

	// Clear the pending request
	h.pendingClarificationRequests.Delete(sessionID)

	ctx := context.Background()
	messageID := uuid.New().String()

	// Process clarification response through chat service
	var streamReader *schema.StreamReader[*schema.Message]
	var err error

	if debugMode {
		emitter := func(event string, eventData interface{}) {
			socket.Emit(event, eventData)
		}
		streamReader, err = h.chatService.ProcessClarificationResponseWithDebug(
			ctx, sessionID, response, pendingReq, emitter,
		)
	} else {
		streamReader, err = h.chatService.ProcessClarificationResponse(
			ctx, sessionID, response, pendingReq,
		)
	}

	if err != nil {
		socket.Emit("error", map[string]string{
			"code":    "CLARIFICATION_ERROR",
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

	// Append to history - the original query and the final response
	if err := h.sessionStore.AppendHistory(sessionID,
		schema.UserMessage(pendingReq.OriginalQuery),
		schema.AssistantMessage(fullContent.String(), nil),
	); err != nil {
		log.Printf("Failed to append history: %v", err)
	}

	// Clear pending clarification from session
	h.sessionStore.ClearPendingClarification(sessionID)

	socket.Emit("done", map[string]string{
		"message_id":   messageID,
		"full_content": fullContent.String(),
	})
}

// StorePendingClarificationRequest stores a clarification request for later response handling
func (h *SocketIOHandler) StorePendingClarificationRequest(sessionID string, req *service.ClarificationRequest) {
	h.pendingClarificationRequests.Store(sessionID, req)
}

func (h *SocketIOHandler) Handler() http.Handler {
	return h.io.HttpHandler()
}
