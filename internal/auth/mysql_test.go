package auth

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMySQLStoreCreatesAccountsAndEnforcesTokenExpiry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &MySQLStore{db: db}
	record := accountRecord{Account: Account{ID: "account-1", Email: "person@example.com"}, PasswordHash: []byte("argon2id$hash")}

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO visit_accounts (id, email, password_hash) VALUES (?, ?, ?)")).
		WithArgs(record.ID, record.Email, record.PasswordHash).WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM visit_account_sessions WHERE token_hash = ? AND expires_at > ?")).
		WithArgs("hashed-token", now).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow("account-1"))
	accountID, err := store.AccountByToken(context.Background(), "hashed-token", now)
	if err != nil || accountID != "account-1" {
		t.Fatalf("token account=%q err=%v", accountID, err)
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM visit_account_sessions WHERE token_hash = ? AND expires_at > ?")).
		WithArgs("expired-token", now).WillReturnError(sql.ErrNoRows)
	if _, err := store.AccountByToken(context.Background(), "expired-token", now); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expired lookup error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
