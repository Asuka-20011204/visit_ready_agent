package session

import (
	"context"
	"errors"
	"sort"
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
	ErrStateChanged  = errors.New("session state changed")
)

type Store interface {
	Create(item domain.Session) error
	Get(id string, now time.Time) (domain.Session, error)
	Replace(item domain.Session) error
	ReplaceIfStatus(item domain.Session, expected domain.AgentStatus) (domain.Session, error)
	Delete(id string) error
	DeleteExpired(now time.Time) int
	ListByOwner(ownerHash string, now time.Time, limit int) ([]domain.Session, error)
}

type HealthStore interface {
	Store
	Ready(context.Context) error
	Close() error
}

type ContextStore interface {
	Store
	CreateContext(context.Context, domain.Session) error
	ReplaceContext(context.Context, domain.Session) error
	ReplaceIfStatusContext(context.Context, domain.Session, domain.AgentStatus) (domain.Session, error)
	DeleteContext(context.Context, string) error
}

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
	return s.CreateContext(context.Background(), item)
}

func (s *MemoryStore) CreateContext(ctx context.Context, item domain.Session) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("session ID is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := s.sessions[item.ID]; exists {
		return ErrAlreadyExists
	}
	if len(s.sessions) >= s.maxItems {
		return ErrCapacity
	}
	stored := clone(item)
	if stored.Revision == 0 {
		stored.Revision = 1
	}
	s.sessions[item.ID] = stored
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
	return s.ReplaceContext(context.Background(), item)
}

func (s *MemoryStore) ReplaceContext(ctx context.Context, item domain.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, exists := s.sessions[item.ID]
	if !exists {
		return ErrNotFound
	}
	if item.Revision != current.Revision {
		return ErrStateChanged
	}
	next := clone(item)
	next.Revision = current.Revision + 1
	s.sessions[item.ID] = next
	return nil
}

func (s *MemoryStore) ReplaceIfStatus(item domain.Session, expected domain.AgentStatus) (domain.Session, error) {
	return s.ReplaceIfStatusContext(context.Background(), item, expected)
}

func (s *MemoryStore) ReplaceIfStatusContext(ctx context.Context, item domain.Session, expected domain.AgentStatus) (domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	current, exists := s.sessions[item.ID]
	if !exists {
		return domain.Session{}, ErrNotFound
	}
	if current.Status != expected || current.Revision != item.Revision {
		return clone(current), ErrStateChanged
	}
	next := clone(item)
	next.Revision = current.Revision + 1
	s.sessions[item.ID] = next
	return clone(next), nil
}

func (s *MemoryStore) Delete(id string) error {
	return s.DeleteContext(context.Background(), id)
}

func (s *MemoryStore) DeleteContext(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := s.sessions[id]; !exists {
		return ErrNotFound
	}
	delete(s.sessions, id)
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

func (s *MemoryStore) ListByOwner(ownerHash string, now time.Time, limit int) ([]domain.Session, error) {
	if ownerHash == "" || limit <= 0 {
		return []domain.Session{}, nil
	}
	s.mu.RLock()
	result := make([]domain.Session, 0, min(limit, len(s.sessions)))
	for _, item := range s.sessions {
		if item.OwnerHash == ownerHash && (item.ExpiresAt.IsZero() || item.ExpiresAt.After(now)) {
			result = append(result, clone(item))
		}
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *MemoryStore) Ready(context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sessions) >= s.maxItems {
		return ErrCapacity
	}
	return nil
}

func (*MemoryStore) Close() error { return nil }

func clone(source domain.Session) domain.Session {
	result := source
	if source.Failure != nil {
		failure := *source.Failure
		result.Failure = &failure
	}
	result.Facts = append([]domain.Fact(nil), source.Facts...)
	result.SymptomProfiles = append([]domain.SymptomProfile(nil), source.SymptomProfiles...)
	for index := range result.SymptomProfiles {
		result.SymptomProfiles[index].AssociatedSymptoms = append([]string(nil), source.SymptomProfiles[index].AssociatedSymptoms...)
		result.SymptomProfiles[index].EvidenceQuotes = append([]string(nil), source.SymptomProfiles[index].EvidenceQuotes...)
	}
	result.Timeline = append([]domain.TimelineEvent(nil), source.Timeline...)
	result.RiskSignals = append([]domain.RiskSignal(nil), source.RiskSignals...)
	result.MissingFields = append([]string(nil), source.MissingFields...)
	result.ClarificationQuestions = append([]string(nil), source.ClarificationQuestions...)
	result.ClarificationPrompts = append([]domain.Question(nil), source.ClarificationPrompts...)
	result.ClarificationTurns = append([]domain.ClarificationTurn(nil), source.ClarificationTurns...)
	for index := range result.ClarificationTurns {
		result.ClarificationTurns[index].Questions = append([]domain.Question(nil), source.ClarificationTurns[index].Questions...)
	}
	result.Questions = append([]domain.Question(nil), source.Questions...)
	result.ActionItems = append([]domain.ActionItem(nil), source.ActionItems...)
	result.Sources = append([]domain.Source(nil), source.Sources...)
	result.Uncertainties = append([]domain.Uncertainty(nil), source.Uncertainties...)
	result.Contradictions = append([]domain.Contradiction(nil), source.Contradictions...)
	result.Medications = append([]string(nil), source.Medications...)
	result.Allergies = append([]string(nil), source.Allergies...)
	result.ChronicConditions = append([]string(nil), source.ChronicConditions...)
	result.TraumaHistory = append([]string(nil), source.TraumaHistory...)
	result.SafetyNotes = append([]string(nil), source.SafetyNotes...)
	result.DeniedConditions = append([]string(nil), source.DeniedConditions...)
	result.Measurements = append([]string(nil), source.Measurements...)
	result.Tests = append([]string(nil), source.Tests...)
	result.ConversationSummary = source.ConversationSummary
	result.ConversationSummary.Confirmed = append([]string(nil), source.ConversationSummary.Confirmed...)
	result.ConversationSummary.OpenThreads = append([]string(nil), source.ConversationSummary.OpenThreads...)
	result.Events = append([]domain.AgentEvent(nil), source.Events...)
	result.NodeTimings = append([]domain.NodeTiming(nil), source.NodeTimings...)
	return result
}
