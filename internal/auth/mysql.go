package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const mysqlTimeout = 5 * time.Second

var mysqlSchema = []string{`CREATE TABLE IF NOT EXISTS visit_accounts (
  id VARCHAR(64) PRIMARY KEY,
  email VARCHAR(320) NOT NULL,
  password_hash VARBINARY(255) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uq_visit_accounts_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`, `CREATE TABLE IF NOT EXISTS visit_account_sessions (
  token_hash CHAR(64) PRIMARY KEY,
  account_id VARCHAR(64) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  INDEX idx_visit_account_sessions_expires (expires_at),
  CONSTRAINT fk_visit_account_sessions_account FOREIGN KEY (account_id) REFERENCES visit_accounts(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`}

type MySQLStore struct{ db *sql.DB }

func OpenMySQLStore(ctx context.Context, dsn string) (*MySQLStore, error) {
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("MySQL DSN is invalid")
	}
	config.ParseTime = true
	if config.Loc == nil {
		config.Loc = time.UTC
	}
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open account store: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(3 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect account store: %w", err)
	}
	for _, statement := range mysqlSchema {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize account schema: %w", err)
		}
	}
	return &MySQLStore{db: db}, nil
}

func (s *MySQLStore) Close() error { return s.db.Close() }

func (s *MySQLStore) Create(parent context.Context, record accountRecord) error {
	ctx, cancel := context.WithTimeout(parent, mysqlTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, "INSERT INTO visit_accounts (id, email, password_hash) VALUES (?, ?, ?)", record.ID, record.Email, record.PasswordHash)
	if err == nil {
		return nil
	}
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return ErrEmailExists
	}
	return fmt.Errorf("create account: %w", err)
}

func (s *MySQLStore) ByEmail(parent context.Context, email string) (accountRecord, error) {
	ctx, cancel := context.WithTimeout(parent, mysqlTimeout)
	defer cancel()
	var record accountRecord
	err := s.db.QueryRowContext(ctx, "SELECT id, email, password_hash FROM visit_accounts WHERE email = ?", email).Scan(&record.ID, &record.Email, &record.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return accountRecord{}, ErrAccountNotFound
	}
	if err != nil {
		return accountRecord{}, fmt.Errorf("get account by email: %w", err)
	}
	return record, nil
}

func (s *MySQLStore) ByID(parent context.Context, id string) (accountRecord, error) {
	ctx, cancel := context.WithTimeout(parent, mysqlTimeout)
	defer cancel()
	var record accountRecord
	err := s.db.QueryRowContext(ctx, "SELECT id, email, password_hash FROM visit_accounts WHERE id = ?", id).Scan(&record.ID, &record.Email, &record.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return accountRecord{}, ErrAccountNotFound
	}
	if err != nil {
		return accountRecord{}, fmt.Errorf("get account by id: %w", err)
	}
	return record, nil
}

func (s *MySQLStore) PutToken(parent context.Context, hash, accountID string, expiresAt time.Time) error {
	ctx, cancel := context.WithTimeout(parent, mysqlTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, "INSERT INTO visit_account_sessions (token_hash, account_id, expires_at) VALUES (?, ?, ?)", hash, accountID, expiresAt.UTC())
	if err != nil {
		return fmt.Errorf("create account session: %w", err)
	}
	return nil
}

func (s *MySQLStore) AccountByToken(parent context.Context, hash string, now time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(parent, mysqlTimeout)
	defer cancel()
	var accountID string
	err := s.db.QueryRowContext(ctx, "SELECT account_id FROM visit_account_sessions WHERE token_hash = ? AND expires_at > ?", hash, now.UTC()).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", fmt.Errorf("get account session: %w", err)
	}
	return accountID, nil
}

func (s *MySQLStore) DeleteToken(parent context.Context, hash string) error {
	ctx, cancel := context.WithTimeout(parent, mysqlTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, "DELETE FROM visit_account_sessions WHERE token_hash = ?", hash)
	if err != nil {
		return fmt.Errorf("delete account session: %w", err)
	}
	return nil
}
