package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"agent-chatbot/api/model"
	"agent-chatbot/api/service"
)

type SessionHandler struct {
	sessionStore *service.SessionStore
}

func NewSessionHandler(sessionStore *service.SessionStore) *SessionHandler {
	return &SessionHandler{
		sessionStore: sessionStore,
	}
}

func (h *SessionHandler) Create(c *gin.Context) {
	var req model.CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = model.CreateSessionRequest{}
	}

	session := h.sessionStore.Create(req.SystemPrompt)

	c.JSON(http.StatusCreated, model.SessionResponse{
		SessionID: session.ID,
		CreatedAt: session.CreatedAt,
	})
}

func (h *SessionHandler) List(c *gin.Context) {
	sessions := h.sessionStore.List()

	responses := make([]*model.SessionResponse, len(sessions))
	for i, s := range sessions {
		responses[i] = &model.SessionResponse{
			SessionID: s.ID,
			CreatedAt: s.CreatedAt,
		}
	}

	c.JSON(http.StatusOK, model.SessionListResponse{
		Sessions: responses,
	})
}

func (h *SessionHandler) Get(c *gin.Context) {
	id := c.Param("id")

	session, err := h.sessionStore.Get(id)
	if err != nil {
		if errors.Is(err, service.ErrSessionNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, model.SessionDetailResponse{
		SessionID:    session.ID,
		History:      session.History,
		SystemPrompt: session.SystemPrompt,
		CreatedAt:    session.CreatedAt,
		LastActivity: session.LastActivity,
	})
}

func (h *SessionHandler) Delete(c *gin.Context) {
	id := c.Param("id")

	if err := h.sessionStore.Delete(id); err != nil {
		if errors.Is(err, service.ErrSessionNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "session deleted"})
}

func (h *SessionHandler) Clear(c *gin.Context) {
	id := c.Param("id")

	if err := h.sessionStore.ClearHistory(id); err != nil {
		if errors.Is(err, service.ErrSessionNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "history cleared"})
}
