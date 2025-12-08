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

// SetPendingClarification sets the pending clarification for a session
func (s *SessionStore) SetPendingClarification(id string, pending *model.PendingClarification) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	session.PendingClarification = pending
	session.LastActivity = time.Now()
	return nil
}

// ClearPendingClarification clears the pending clarification for a session
func (s *SessionStore) ClearPendingClarification(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	session.PendingClarification = nil
	session.LastActivity = time.Now()
	return nil
}

// GetPendingClarification gets the pending clarification for a session
func (s *SessionStore) GetPendingClarification(id string) (*model.PendingClarification, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return session.PendingClarification, nil
}

// SetPendingComparisonClarification sets the pending comparison clarification for a session.
// This is used to track multi-entity comparison queries that need disambiguation.
func (s *SessionStore) SetPendingComparisonClarification(id string, pending *ComparisonPendingClarification) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}

	// Convert service type to model type for storage
	if pending != nil {
		session.PendingComparisonClarification = &model.ComparisonPendingClarification{
			Phase:            int(pending.Phase),
			OriginalQuery:    pending.OriginalQuery,
			CurrentIndex:     pending.CurrentIndex,
			ResolvedEntities: make([]model.ResolvedEntityModel, 0, len(pending.ResolvedEntities)),
			CreatedAt:        pending.CreatedAt,
		}

		// Convert resolved entities with full info
		for _, re := range pending.ResolvedEntities {
			session.PendingComparisonClarification.ResolvedEntities = append(
				session.PendingComparisonClarification.ResolvedEntities,
				model.ResolvedEntityModel{
					OriginalName: re.OriginalName,
					ResolvedUUID: re.ResolvedUUID,
					ResolvedName: re.ResolvedName,
					Role:         re.Role,
				},
			)
		}

		// Convert ComparisonQuery if present
		if pending.ComparisonQuery != nil {
			session.PendingComparisonClarification.ComparisonQuery = &model.ComparisonQueryModel{
				OriginalQuery: pending.ComparisonQuery.OriginalQuery,
				QueryType:     pending.ComparisonQuery.QueryType,
				CompareAspect: pending.ComparisonQuery.CompareAspect,
				AnalysisGoal:  pending.ComparisonQuery.AnalysisGoal,
				Entities:      make([]model.EntityQueryModel, 0, len(pending.ComparisonQuery.Entities)),
			}
			for _, e := range pending.ComparisonQuery.Entities {
				session.PendingComparisonClarification.ComparisonQuery.Entities = append(
					session.PendingComparisonClarification.ComparisonQuery.Entities,
					model.EntityQueryModel{
						EntityName:     e.EntityName,
						EntityKeywords: e.EntityKeywords,
						TargetLabels:   e.TargetLabels,
						Role:           e.Role,
						Index:          e.Index,
					},
				)
			}
		}
	} else {
		session.PendingComparisonClarification = nil
	}
	session.LastActivity = time.Now()
	return nil
}

// ClearPendingComparisonClarification clears the pending comparison clarification for a session
func (s *SessionStore) ClearPendingComparisonClarification(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	session.PendingComparisonClarification = nil
	session.LastActivity = time.Now()
	return nil
}

// GetPendingComparisonClarification gets the pending comparison clarification for a session.
// Returns nil if no pending comparison clarification exists.
func (s *SessionStore) GetPendingComparisonClarification(id string) *ComparisonPendingClarification {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[id]
	if !ok || session.PendingComparisonClarification == nil {
		return nil
	}

	// Convert from model type back to service type
	modelPending := session.PendingComparisonClarification
	pending := &ComparisonPendingClarification{
		Phase:            ComparisonClarificationPhase(modelPending.Phase),
		OriginalQuery:    modelPending.OriginalQuery,
		CurrentIndex:     modelPending.CurrentIndex,
		ResolvedEntities: make([]ResolvedEntity, 0, len(modelPending.ResolvedEntities)),
		CreatedAt:        modelPending.CreatedAt,
	}

	// Convert resolved entities with full info
	for _, re := range modelPending.ResolvedEntities {
		pending.ResolvedEntities = append(pending.ResolvedEntities, ResolvedEntity{
			OriginalName: re.OriginalName,
			ResolvedUUID: re.ResolvedUUID,
			ResolvedName: re.ResolvedName,
			Role:         re.Role,
		})
	}

	// Convert ComparisonQuery if present
	if modelPending.ComparisonQuery != nil {
		pending.ComparisonQuery = &ComparisonQuery{
			OriginalQuery: modelPending.ComparisonQuery.OriginalQuery,
			QueryType:     modelPending.ComparisonQuery.QueryType,
			CompareAspect: modelPending.ComparisonQuery.CompareAspect,
			AnalysisGoal:  modelPending.ComparisonQuery.AnalysisGoal,
			Entities:      make([]EntityQuery, 0, len(modelPending.ComparisonQuery.Entities)),
		}
		for _, e := range modelPending.ComparisonQuery.Entities {
			pending.ComparisonQuery.Entities = append(pending.ComparisonQuery.Entities, EntityQuery{
				EntityName:     e.EntityName,
				EntityKeywords: e.EntityKeywords,
				TargetLabels:   e.TargetLabels,
				Role:           e.Role,
				Index:          e.Index,
			})
		}
	}

	return pending
}
