package session_test

import (
	"context"
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
	if replaced.Status != domain.StatusCompleted || replaced.Revision != got.Revision+1 {
		t.Fatalf("status = %q", replaced.Status)
	}
	if err := store.Replace(got); !errors.Is(err, session.ErrStateChanged) {
		t.Fatalf("stale Replace() error = %v, want ErrStateChanged", err)
	}
}

func TestMemoryStoreCopiesFailureMetadata(t *testing.T) {
	store := session.NewMemoryStore()
	item := domain.Session{
		ID: "failed-copy", Status: domain.StatusFailed, ExpiresAt: time.Now().Add(time.Hour),
		Failure: &domain.RunFailure{Code: "temporary", Message: "可以重试", Retryable: true, Attempts: 1, FailedAt: time.Now()},
	}
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	item.Failure.Message = "mutated outside store"
	first, err := store.Get(item.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first.Failure.Message = "mutated returned copy"
	second, err := store.Get(item.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second.Failure.Message != "可以重试" {
		t.Fatalf("stored failure shared a pointer: %#v", second.Failure)
	}
}

func TestMemoryStoreRejectsStaleSameStatusReplacement(t *testing.T) {
	store := session.NewMemoryStore()
	item := domain.Session{ID: "revision-cas", Status: domain.StatusFailed, Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	first, _ := store.Get(item.ID, time.Now())
	stale, _ := store.Get(item.ID, time.Now())
	first.Failure = &domain.RunFailure{Code: "temporary", Attempts: 2}
	stored, err := store.ReplaceIfStatus(first, domain.StatusFailed)
	if err != nil || stored.Revision != 2 {
		t.Fatalf("first replacement = %#v, %v", stored, err)
	}
	stale.Failure = &domain.RunFailure{Code: "temporary", Attempts: 2}
	current, err := store.ReplaceIfStatus(stale, domain.StatusFailed)
	if !errors.Is(err, session.ErrStateChanged) || current.Revision != 2 {
		t.Fatalf("stale replacement = %#v, %v", current, err)
	}
}

func TestMemoryStoreDeepCopiesStructuredAssessment(t *testing.T) {
	store := session.NewMemoryStore()
	original := domain.Session{
		ID: "structured", ExpiresAt: time.Now().Add(time.Hour),
		SymptomProfiles:      []domain.SymptomProfile{{Name: "心悸", AssociatedSymptoms: []string{"头晕"}, EvidenceQuotes: []string{"每天两次"}}},
		Timeline:             []domain.TimelineEvent{{TimeLabel: "昨晚", Event: "出现心悸"}},
		RiskSignals:          []domain.RiskSignal{{Title: "需要优先说明"}},
		ClarificationPrompts: []domain.Question{{Text: "是否胸痛？", Reason: "安全确认"}},
		ClarificationTurns:   []domain.ClarificationTurn{{Questions: []domain.Question{{Text: "何时开始？"}}, Answer: "昨晚"}},
		ActionItems:          []domain.ActionItem{{Title: "记录发作", Detail: "记录时间"}},
		Uncertainties:        []domain.Uncertainty{{Topic: "持续时间"}},
		Contradictions:       []domain.Contradiction{{Topic: "开始时间"}},
		ConversationSummary:  domain.ConversationSummary{Confirmed: []string{"反复心悸"}, OpenThreads: []string{"持续时间"}},
	}
	if err := store.Create(original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	original.SymptomProfiles[0].AssociatedSymptoms[0] = "外部修改"
	original.SymptomProfiles[0].EvidenceQuotes[0] = "外部修改"
	original.ActionItems[0].Title = "外部修改"
	original.ClarificationTurns[0].Questions[0].Text = "外部修改"
	original.Uncertainties[0].Topic = "外部修改"
	original.Contradictions[0].Topic = "外部修改"
	original.ConversationSummary.Confirmed[0] = "外部修改"
	original.ConversationSummary.OpenThreads[0] = "外部修改"

	got, err := store.Get("structured", time.Now())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.SymptomProfiles[0].AssociatedSymptoms[0] != "头晕" || got.SymptomProfiles[0].EvidenceQuotes[0] != "每天两次" || got.ActionItems[0].Title != "记录发作" || got.ClarificationTurns[0].Questions[0].Text != "何时开始？" {
		t.Fatalf("stored structured data shares caller memory: %#v", got)
	}
	if got.Uncertainties[0].Topic != "持续时间" || got.Contradictions[0].Topic != "开始时间" || got.ConversationSummary.Confirmed[0] != "反复心悸" || got.ConversationSummary.OpenThreads[0] != "持续时间" {
		t.Fatalf("stored review state shares caller memory: %#v", got)
	}
	got.SymptomProfiles[0].AssociatedSymptoms[0] = "读取后修改"
	got.SymptomProfiles[0].EvidenceQuotes[0] = "读取后修改"
	got.Uncertainties[0].Topic = "读取后修改"
	got.Contradictions[0].Topic = "读取后修改"
	got.ConversationSummary.Confirmed[0] = "读取后修改"
	got.ConversationSummary.OpenThreads[0] = "读取后修改"
	gotAgain, _ := store.Get("structured", time.Now())
	if gotAgain.SymptomProfiles[0].AssociatedSymptoms[0] != "头晕" || gotAgain.SymptomProfiles[0].EvidenceQuotes[0] != "每天两次" || gotAgain.Uncertainties[0].Topic != "持续时间" || gotAgain.Contradictions[0].Topic != "开始时间" || gotAgain.ConversationSummary.Confirmed[0] != "反复心悸" || gotAgain.ConversationSummary.OpenThreads[0] != "持续时间" {
		t.Fatalf("Get() returned shared nested data: %#v", gotAgain.SymptomProfiles)
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

func TestMemoryStoreReplaceIfStatusIsAtomic(t *testing.T) {
	store := session.NewMemoryStore()
	original := domain.Session{ID: "conditional", Status: domain.StatusWaitingClarification}
	if err := store.Create(original); err != nil {
		t.Fatal(err)
	}
	emergency := original
	current, err := store.Get(original.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	emergency.Revision = current.Revision
	emergency.Status = domain.StatusEmergency
	if err := store.Replace(emergency); err != nil {
		t.Fatal(err)
	}
	stale := original
	stale.Status = domain.StatusWaitingReview
	current, err = store.ReplaceIfStatus(stale, domain.StatusWaitingClarification)
	if !errors.Is(err, session.ErrStateChanged) {
		t.Fatalf("ReplaceIfStatus() error = %v", err)
	}
	if current.Status != domain.StatusEmergency {
		t.Fatalf("current status = %q", current.Status)
	}
	stored, err := store.Get(original.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusEmergency {
		t.Fatalf("stored status = %q", stored.Status)
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

func TestMemoryStoreDeletesSession(t *testing.T) {
	store := session.NewMemoryStore()
	item := domain.Session{ID: "delete-me", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Create(item); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(item.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(item.ID, time.Now()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get() after Delete error = %v", err)
	}
	if err := store.Delete(item.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("second Delete() error = %v", err)
	}
}

func TestMemoryStoreListsOnlyOwnedActiveSessions(t *testing.T) {
	store := session.NewMemoryStore()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, item := range []domain.Session{
		{ID: "newer", OwnerHash: "owner-a", Status: domain.StatusWaitingReview, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)},
		{ID: "older", OwnerHash: "owner-a", Status: domain.StatusCompleted, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
		{ID: "other", OwnerHash: "owner-b", Status: domain.StatusCompleted, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "expired", OwnerHash: "owner-a", Status: domain.StatusCompleted, CreatedAt: now, ExpiresAt: now.Add(-time.Second)},
	} {
		if err := store.Create(item); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListByOwner("owner-a", now, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "newer" || items[1].ID != "older" {
		t.Fatalf("ListByOwner() = %#v", items)
	}
}

func TestMemoryStoreContextWritesDoNotCommitAfterCancellation(t *testing.T) {
	store := session.NewMemoryStore()
	original := domain.Session{ID: "existing", Status: domain.StatusWaitingReview, Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Create(original); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.CreateContext(ctx, domain.Session{ID: "new"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateContext() error = %v, want context.Canceled", err)
	}
	updated := original
	updated.Status = domain.StatusCompleted
	if _, err := store.ReplaceIfStatusContext(ctx, updated, domain.StatusWaitingReview); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReplaceIfStatusContext() error = %v, want context.Canceled", err)
	}
	if err := store.DeleteContext(ctx, original.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext() error = %v, want context.Canceled", err)
	}

	stored, err := store.Get(original.ID, time.Now())
	if err != nil || stored.Status != domain.StatusWaitingReview {
		t.Fatalf("stored session changed after canceled writes: %#v, %v", stored, err)
	}
	if _, err := store.Get("new", time.Now()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("canceled create persisted: %v", err)
	}
}
