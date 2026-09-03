package session

import (
	"errors"
	"strings"
	"sync"
	"time"

	"visitready/internal/domain"
)

var (
	ErrAlreadyExists = errors.New("session already exists")
	ErrNotFound      = errors.New("session not found")
	ErrExpired       = errors.New("session expired")
	ErrCapacity      = errors.New("session store capacity reached")
)

type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]domain.Session
	maxItems int
}

func NewMemoryStore() *MemoryStore {
	return NewMemoryStoreWithLimit(1024)
}

func NewMemoryStoreWithLimit(maxItems int) *MemoryStore {
	if maxItems <= 0 {
		maxItems = 1024
	}
	return &MemoryStore{sessions: make(map[string]domain.Session), maxItems: maxItems}
}

func (s *MemoryStore) Create(item domain.Session) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("session ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[item.ID]; exists {
		return ErrAlreadyExists
	}
	if len(s.sessions) >= s.maxItems {
		return ErrCapacity
	}
	s.sessions[item.ID] = clone(item)
	return nil
}

func (s *MemoryStore) Get(id string, now time.Time) (domain.Session, error) {
	s.mu.RLock()
	item, exists := s.sessions[id]
	s.mu.RUnlock()
	if !exists {
		return domain.Session{}, ErrNotFound
	}
	if !item.ExpiresAt.IsZero() && !item.ExpiresAt.After(now) {
		s.mu.Lock()
		current, stillExists := s.sessions[id]
		if stillExists && current.ExpiresAt.Equal(item.ExpiresAt) {
			delete(s.sessions, id)
		}
		s.mu.Unlock()
		return domain.Session{}, ErrExpired
	}
	return clone(item), nil
}

func (s *MemoryStore) Replace(item domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[item.ID]; !exists {
		return ErrNotFound
	}
	s.sessions[item.ID] = clone(item)
	return nil
}

func (s *MemoryStore) DeleteExpired(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, item := range s.sessions {
		if !item.ExpiresAt.IsZero() && !item.ExpiresAt.After(now) {
			delete(s.sessions, id)
			removed++
		}
	}
	return removed
}

func clone(source domain.Session) domain.Session {
	result := source
	result.Facts = append([]domain.Fact(nil), source.Facts...)
	result.MissingFields = append([]string(nil), source.MissingFields...)
	result.ClarificationQuestions = append([]string(nil), source.ClarificationQuestions...)
	result.Questions = append([]domain.Question(nil), source.Questions...)
	result.Sources = append([]domain.Source(nil), source.Sources...)
	result.Events = append([]domain.AgentEvent(nil), source.Events...)
	return result
}
