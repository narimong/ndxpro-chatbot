package service

import (
	"errors"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"simple-chatbot/api/model"
)

var (
	ErrSessionNotFound = errors.New("session not found")
)

type SessionStore struct {
	sessions map[string]*model.Session
	mu       sync.RWMutex
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*model.Session),
	}
}

func (s *SessionStore) Create(systemPrompt string) *model.Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	session := &model.Session{
		ID:           uuid.New().String(),
		History:      make([]*schema.Message, 0),
		SystemPrompt: systemPrompt,
		CreatedAt:    now,
		LastActivity: now,
	}

	s.sessions[session.ID] = session
	return session
}

func (s *SessionStore) Get(id string) (*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

func (s *SessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return ErrSessionNotFound
	}
	delete(s.sessions, id)
	return nil
}

func (s *SessionStore) ClearHistory(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	session.History = make([]*schema.Message, 0)
	session.LastActivity = time.Now()
	return nil
}

func (s *SessionStore) AppendHistory(id string, msgs ...*schema.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	session.History = append(session.History, msgs...)
	session.LastActivity = time.Now()
	return nil
}

func (s *SessionStore) List() []*model.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessions := make([]*model.Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}
