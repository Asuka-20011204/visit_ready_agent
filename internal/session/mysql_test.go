package session

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"visitready/internal/domain"
)

func TestMySQLStoreRestoresPrivateWorkflowState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newTestMySQLStore(t, db, 8)
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	original := domain.Session{
		ID: "persisted", Status: domain.StatusWaitingClarification,
		RawInput: "最近反复心悸", Clarification: "昨晚更明显",
		ClarificationTurns: []domain.ClarificationTurn{{
			Questions: []domain.Question{{Text: "什么时候更明显？", Category: "timeline"}},
			Answer:    "昨晚更明显",
		}},
		Facts:     []domain.Fact{{Category: "symptom", Content: "心悸", SourceQuote: "心悸"}},
		ExpiresAt: now.Add(24 * time.Hour),
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM visit_sessions WHERE expires_at > ?")).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO visit_sessions (id, status, revision, owner_hash, payload, expires_at) VALUES (?, ?, ?, ?, ?, ?)")).WithArgs(original.ID, original.Status, uint64(1), original.OwnerHash, sqlmock.AnyArg(), original.ExpiresAt).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.Create(original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	original.Revision = 1
	payload, err := store.codec.encode(original)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT payload, expires_at FROM visit_sessions WHERE id = ?")).WithArgs(original.ID).WillReturnRows(sqlmock.NewRows([]string{"payload", "expires_at"}).AddRow(payload, original.ExpiresAt))
	got, err := store.Get(original.ID, now)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.RawInput != original.RawInput || got.ClarificationTurns[0].Answer != "昨晚更明显" || got.Facts[0].Content != "心悸" {
		t.Fatalf("persistent session lost workflow state: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionEncodingRetainsRecoverySnapshot(t *testing.T) {
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	original := domain.Session{
		ID: "failed", Status: domain.StatusFailed, RawInput: "仅存于服务端的健康描述",
		Failure: &domain.RunFailure{Code: "temporary", Message: "可以重试", Retryable: true, Attempts: 2, FailedAt: now},
	}
	payload, err := encodeSession(original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decodeSession(payload)
	if err != nil {
		t.Fatal(err)
	}
	if restored.RawInput != original.RawInput || restored.Failure == nil || *restored.Failure != *original.Failure {
		t.Fatalf("restored recovery snapshot = %#v", restored)
	}
}

func TestMySQLStoreReplaceIfStatusUsesRevisionCAS(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newTestMySQLStore(t, db, 8)
	now := time.Now().UTC()
	stale := domain.Session{
		ID:        "revision-cas",
		Status:    domain.StatusFailed,
		Revision:  1,
		ExpiresAt: now.Add(time.Hour),
	}
	query := regexp.QuoteMeta("UPDATE visit_sessions SET status = ?, revision = ?, payload = ?, expires_at = ? WHERE id = ? AND status = ? AND revision = ?")

	mock.ExpectExec(query).
		WithArgs(stale.Status, uint64(2), sqlmock.AnyArg(), stale.ExpiresAt, stale.ID, domain.StatusFailed, uint64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	stored, err := store.ReplaceIfStatus(stale, domain.StatusFailed)
	if err != nil {
		t.Fatalf("first ReplaceIfStatus() error = %v", err)
	}
	if stored.Revision != 2 {
		t.Fatalf("first ReplaceIfStatus() revision = %d, want 2", stored.Revision)
	}

	current := stored
	current.Failure = &domain.RunFailure{
		Code:      "temporary",
		Message:   "可以重试",
		Retryable: true,
		Attempts:  2,
		FailedAt:  now,
	}
	currentPayload, err := store.codec.encode(current)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(query).
		WithArgs(stale.Status, uint64(2), sqlmock.AnyArg(), stale.ExpiresAt, stale.ID, domain.StatusFailed, uint64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT payload, expires_at FROM visit_sessions WHERE id = ?")).
		WithArgs(stale.ID).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "expires_at"}).AddRow(currentPayload, current.ExpiresAt))
	got, err := store.ReplaceIfStatus(stale, domain.StatusFailed)
	if !errors.Is(err, ErrStateChanged) {
		t.Fatalf("stale ReplaceIfStatus() error = %v, want ErrStateChanged", err)
	}
	if got.Revision != 2 || got.Failure == nil || got.Failure.Attempts != 2 {
		t.Fatalf("stale ReplaceIfStatus() current = %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLStoreDeletesAndExpiresSessions(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newTestMySQLStore(t, db, 8)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM visit_sessions WHERE id = ?")).WithArgs("delete-me").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.Delete("delete-me"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM visit_sessions WHERE id = ?")).WithArgs("missing").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.Delete("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Delete() error = %v", err)
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM visit_sessions WHERE expires_at <= ?")).WithArgs(now).WillReturnResult(sqlmock.NewResult(0, 3))
	if removed := store.DeleteExpired(now); removed != 3 {
		t.Fatalf("DeleteExpired() = %d, want 3", removed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLStoreReadinessReflectsCapacity(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newTestMySQLStore(t, db, 8)
	query := regexp.QuoteMeta("SELECT COUNT(*) FROM visit_sessions WHERE expires_at > ?")

	mock.ExpectQuery(query).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))
	if err := store.Ready(t.Context()); err != nil {
		t.Fatalf("Ready() below capacity error = %v", err)
	}
	mock.ExpectQuery(query).WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(8))
	if err := store.Ready(t.Context()); !errors.Is(err, ErrCapacity) {
		t.Fatalf("Ready() at capacity error = %v, want ErrCapacity", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLStoreContextWriteStopsBeforeDatabaseAfterCancellation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newTestMySQLStore(t, db, 8)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.CreateContext(ctx, domain.Session{ID: "canceled", ExpiresAt: time.Now().Add(time.Hour)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateContext() error = %v, want context.Canceled", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func newTestMySQLStore(t *testing.T, db *sql.DB, maxItems int) *MySQLStore {
	t.Helper()
	store, err := NewMySQLStore(db, bytes.Repeat([]byte{0x42}, 32), maxItems)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
