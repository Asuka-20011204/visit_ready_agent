package session_test

import (
	"errors"
	"testing"
	"time"

	"visitready/internal/domain"
	"visitready/internal/session"
)

func TestMemoryStoreCreateGetAndReplaceUseCopies(t *testing.T) {
	store := session.NewMemoryStore()
	original := domain.Session{
		ID:        "session-1",
		Status:    domain.StatusWaitingReview,
		Facts:     []domain.Fact{{Content: "咳嗽三天"}},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Create(original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	original.Facts[0].Content = "changed outside store"
	got, err := store.Get("session-1", time.Now())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Facts[0].Content != "咳嗽三天" {
		t.Fatalf("stored fact = %q", got.Facts[0].Content)
	}

	got.Status = domain.StatusCompleted
	if err := store.Replace(got); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	replaced, err := store.Get("session-1", time.Now())
	if err != nil {
		t.Fatalf("Get() after Replace error = %v", err)
	}
	if replaced.Status != domain.StatusCompleted {
		t.Fatalf("status = %q", replaced.Status)
	}
}

func TestMemoryStoreRejectsDuplicateAndMissingSessions(t *testing.T) {
	store := session.NewMemoryStore()
	item := domain.Session{ID: "session-1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Create(item); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Create(item); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	if err := store.Replace(domain.Session{ID: "missing"}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("missing Replace() error = %v", err)
	}
}

func TestMemoryStoreExpiresSessions(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	for _, item := range []domain.Session{
		{ID: "expired", ExpiresAt: now.Add(-time.Second)},
		{ID: "active", ExpiresAt: now.Add(time.Minute)},
	} {
		if err := store.Create(item); err != nil {
			t.Fatalf("Create(%q) error = %v", item.ID, err)
		}
	}

	if _, err := store.Get("expired", now); !errors.Is(err, session.ErrExpired) {
		t.Fatalf("expired Get() error = %v", err)
	}
	if removed := store.DeleteExpired(now); removed != 0 {
		t.Fatalf("DeleteExpired() after lazy deletion = %d", removed)
	}
	if _, err := store.Get("active", now); err != nil {
		t.Fatalf("active Get() error = %v", err)
	}
}

func TestMemoryStoreEnforcesCapacity(t *testing.T) {
	store := session.NewMemoryStoreWithLimit(1)
	if err := store.Create(domain.Session{ID: "first"}); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if err := store.Create(domain.Session{ID: "second"}); !errors.Is(err, session.ErrCapacity) {
		t.Fatalf("second Create() error = %v", err)
	}
}
