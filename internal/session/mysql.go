package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"visitready/internal/domain"
)

const mysqlSchema = `CREATE TABLE IF NOT EXISTS visit_sessions (
  id VARCHAR(64) PRIMARY KEY,
  status VARCHAR(32) NOT NULL,
  revision BIGINT UNSIGNED NOT NULL,
  owner_hash CHAR(64) NOT NULL,
  payload LONGBLOB NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  INDEX idx_visit_sessions_expires_at (expires_at),
  INDEX idx_visit_sessions_owner_created (owner_hash, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`

const mysqlOperationTimeout = 5 * time.Second

type persistedSession struct {
	Session            domain.Session               `json:"session"`
	OwnerHash          string                       `json:"owner_hash,omitempty"`
	RawInput           string                       `json:"raw_input,omitempty"`
	Clarification      string                       `json:"clarification,omitempty"`
	ClarificationTurns []persistedClarificationTurn `json:"clarification_turns,omitempty"`
}

type persistedClarificationTurn struct {
	Questions []domain.Question `json:"questions"`
	Answer    string            `json:"answer"`
}

type MySQLStore struct {
	db       *sql.DB
	maxItems int
	codec    *sessionCodec
}

func OpenMySQLStore(ctx context.Context, dsn string, encryptionKey []byte, maxItems int) (*MySQLStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("MySQL DSN is required")
	}
	driverConfig, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("MySQL DSN is invalid")
	}
	driverConfig.ParseTime = true
	if driverConfig.Loc == nil {
		driverConfig.Loc = time.UTC
	}
	db, err := sql.Open("mysql", driverConfig.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open MySQL session store: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(3 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect MySQL session store: %w", err)
	}
	if _, err := db.ExecContext(ctx, mysqlSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize MySQL session store: %w", err)
	}
	codec, err := newSessionCodec(encryptionKey)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateMySQLSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &MySQLStore{db: db, maxItems: normalizedMaxItems(maxItems), codec: codec}
	if err := store.encryptLegacyRows(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func NewMySQLStore(db *sql.DB, encryptionKey []byte, maxItems int) (*MySQLStore, error) {
	codec, err := newSessionCodec(encryptionKey)
	if err != nil {
		return nil, err
	}
	return &MySQLStore{db: db, maxItems: normalizedMaxItems(maxItems), codec: codec}, nil
}

func normalizedMaxItems(maxItems int) int {
	if maxItems <= 0 {
		return 1024
	}
	return maxItems
}

func (s *MySQLStore) Create(item domain.Session) error {
	return s.CreateContext(context.Background(), item)
}

func (s *MySQLStore) CreateContext(parent context.Context, item domain.Session) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("session ID is required")
	}
	if err := parent.Err(); err != nil {
		return err
	}
	if item.Revision == 0 {
		item.Revision = 1
	}
	payload, err := s.codec.encode(item)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, mysqlOperationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM visit_sessions WHERE expires_at > ?", time.Now().UTC()).Scan(&count); err != nil {
		return fmt.Errorf("count active sessions: %w", err)
	}
	if count >= s.maxItems {
		return ErrCapacity
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO visit_sessions (id, status, revision, owner_hash, payload, expires_at) VALUES (?, ?, ?, ?, ?, ?)", item.ID, item.Status, item.Revision, item.OwnerHash, payload, item.ExpiresAt)
	if err != nil {
		var mysqlErr *mysqldriver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return ErrAlreadyExists
		}
		return fmt.Errorf("insert session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session create: %w", err)
	}
	return nil
}

func (s *MySQLStore) Get(id string, now time.Time) (domain.Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mysqlOperationTimeout)
	defer cancel()
	return s.getWithContext(ctx, id, now)
}

func (s *MySQLStore) getWithContext(ctx context.Context, id string, now time.Time) (domain.Session, error) {
	var payload []byte
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, "SELECT payload, expires_at FROM visit_sessions WHERE id = ?", id).Scan(&payload, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("select session: %w", err)
	}
	if !expiresAt.After(now) {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM visit_sessions WHERE id = ? AND expires_at <= ?", id, now)
		return domain.Session{}, ErrExpired
	}
	item, _, err := s.codec.decode(payload)
	if err != nil {
		return domain.Session{}, err
	}
	item.ExpiresAt = expiresAt
	return clone(item), nil
}

func (s *MySQLStore) Replace(item domain.Session) error {
	return s.ReplaceContext(context.Background(), item)
}

func (s *MySQLStore) ReplaceContext(parent context.Context, item domain.Session) error {
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, mysqlOperationTimeout)
	defer cancel()
	next := clone(item)
	next.Revision = item.Revision + 1
	payload, err := s.codec.encode(next)
	if err != nil {
		return err
	}
	query := "UPDATE visit_sessions SET status = ?, revision = ?, payload = ?, expires_at = ? WHERE id = ? AND revision = ?"
	result, err := s.db.ExecContext(ctx, query, next.Status, next.Revision, payload, next.ExpiresAt, next.ID, item.Revision)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read update result: %w", err)
	}
	if affected > 0 {
		return nil
	}
	if _, getErr := s.getWithContext(ctx, item.ID, time.Now()); getErr != nil {
		return getErr
	}
	return ErrStateChanged
}

func (s *MySQLStore) ReplaceIfStatus(item domain.Session, expected domain.AgentStatus) (domain.Session, error) {
	return s.ReplaceIfStatusContext(context.Background(), item, expected)
}

func (s *MySQLStore) ReplaceIfStatusContext(parent context.Context, item domain.Session, expected domain.AgentStatus) (domain.Session, error) {
	if err := parent.Err(); err != nil {
		return domain.Session{}, err
	}
	ctx, cancel := context.WithTimeout(parent, mysqlOperationTimeout)
	defer cancel()
	expectedRevision := item.Revision
	next := clone(item)
	next.Revision = expectedRevision + 1
	payload, err := s.codec.encode(next)
	if err != nil {
		return domain.Session{}, err
	}
	query := "UPDATE visit_sessions SET status = ?, revision = ?, payload = ?, expires_at = ? WHERE id = ? AND status = ? AND revision = ?"
	result, err := s.db.ExecContext(ctx, query, next.Status, next.Revision, payload, next.ExpiresAt, next.ID, expected, expectedRevision)
	if err != nil {
		return domain.Session{}, fmt.Errorf("conditionally update session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Session{}, fmt.Errorf("read conditional update result: %w", err)
	}
	if affected > 0 {
		return clone(next), nil
	}
	current, getErr := s.getWithContext(ctx, item.ID, time.Now())
	if getErr != nil {
		return domain.Session{}, getErr
	}
	return current, ErrStateChanged
}

func (s *MySQLStore) Delete(id string) error {
	return s.DeleteContext(context.Background(), id)
}

func (s *MySQLStore) DeleteContext(parent context.Context, id string) error {
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, mysqlOperationTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx, "DELETE FROM visit_sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return requireAffected(result)
}

func (s *MySQLStore) DeleteExpired(now time.Time) int {
	ctx, cancel := context.WithTimeout(context.Background(), mysqlOperationTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx, "DELETE FROM visit_sessions WHERE expires_at <= ?", now)
	if err != nil {
		return 0
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0
	}
	return int(affected)
}

func (s *MySQLStore) Ready(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM visit_sessions WHERE expires_at > ?", time.Now().UTC()).Scan(&count); err != nil {
		return fmt.Errorf("count active sessions: %w", err)
	}
	if count >= s.maxItems {
		return ErrCapacity
	}
	return nil
}

func (s *MySQLStore) ListByOwner(ownerHash string, now time.Time, limit int) ([]domain.Session, error) {
	if ownerHash == "" || limit <= 0 {
		return []domain.Session{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), mysqlOperationTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT payload, expires_at FROM visit_sessions WHERE owner_hash = ? AND expires_at > ? ORDER BY updated_at DESC LIMIT ?", ownerHash, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list owned sessions: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Session, 0, limit)
	for rows.Next() {
		var payload []byte
		var expiresAt time.Time
		if err := rows.Scan(&payload, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan owned session: %w", err)
		}
		item, _, err := s.codec.decode(payload)
		if err != nil {
			return nil, err
		}
		item.ExpiresAt = expiresAt
		result = append(result, clone(item))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owned sessions: %w", err)
	}
	return result, nil
}

func (s *MySQLStore) Close() error { return s.db.Close() }

func migrateMySQLSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "ALTER TABLE visit_sessions MODIFY COLUMN payload LONGBLOB NOT NULL"); err != nil {
		return fmt.Errorf("migrate session payload column: %w", err)
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE visit_sessions ADD COLUMN revision BIGINT UNSIGNED NOT NULL DEFAULT 1 AFTER status"); err != nil {
		var mysqlErr *mysqldriver.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1060 {
			return fmt.Errorf("add session revision column: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE visit_sessions ADD COLUMN owner_hash CHAR(64) NOT NULL DEFAULT '' AFTER revision"); err != nil {
		var mysqlErr *mysqldriver.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1060 {
			return fmt.Errorf("add session owner column: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, "CREATE INDEX idx_visit_sessions_owner_created ON visit_sessions (owner_hash, updated_at)"); err != nil {
		var mysqlErr *mysqldriver.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1061 {
			return fmt.Errorf("add session owner index: %w", err)
		}
	}
	return nil
}

func (s *MySQLStore) encryptLegacyRows(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id, owner_hash, payload FROM visit_sessions")
	if err != nil {
		return fmt.Errorf("list session payloads for encryption migration: %w", err)
	}
	type legacyRow struct {
		id        string
		ownerHash string
		payload   []byte
	}
	var legacyRows []legacyRow
	for rows.Next() {
		var row legacyRow
		if err := rows.Scan(&row.id, &row.ownerHash, &row.payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy session: %w", err)
		}
		legacyRows = append(legacyRows, row)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy session rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy session rows: %w", err)
	}
	for _, row := range legacyRows {
		item, legacy, err := s.codec.decode(row.payload)
		if err != nil {
			return fmt.Errorf("decode legacy session %s: %w", row.id, err)
		}
		if !legacy && row.ownerHash == item.OwnerHash {
			continue
		}
		if item.Revision == 0 {
			item.Revision = 1
		}
		payload, err := s.codec.encode(item)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE visit_sessions SET revision = ?, owner_hash = ?, payload = ? WHERE id = ?", item.Revision, item.OwnerHash, payload, row.id); err != nil {
			return fmt.Errorf("encrypt legacy session %s: %w", row.id, err)
		}
	}
	return nil
}

func encodeSession(item domain.Session) ([]byte, error) {
	payload := persistedSession{
		Session: item, OwnerHash: item.OwnerHash, RawInput: item.RawInput, Clarification: item.Clarification,
		ClarificationTurns: encodeClarificationTurns(item.ClarificationTurns),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode session payload: %w", err)
	}
	return data, nil
}

func decodeSession(data []byte) (domain.Session, error) {
	var payload persistedSession
	if err := json.Unmarshal(data, &payload); err != nil {
		return domain.Session{}, fmt.Errorf("decode session payload: %w", err)
	}
	payload.Session.RawInput = payload.RawInput
	payload.Session.OwnerHash = payload.OwnerHash
	payload.Session.Clarification = payload.Clarification
	payload.Session.ClarificationTurns = decodeClarificationTurns(payload.ClarificationTurns)
	return payload.Session, nil
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func encodeClarificationTurns(source []domain.ClarificationTurn) []persistedClarificationTurn {
	result := make([]persistedClarificationTurn, len(source))
	for index, turn := range source {
		result[index] = persistedClarificationTurn{
			Questions: append([]domain.Question(nil), turn.Questions...),
			Answer:    turn.Answer,
		}
	}
	return result
}

func decodeClarificationTurns(source []persistedClarificationTurn) []domain.ClarificationTurn {
	result := make([]domain.ClarificationTurn, len(source))
	for index, turn := range source {
		result[index] = domain.ClarificationTurn{
			Questions: append([]domain.Question(nil), turn.Questions...),
			Answer:    turn.Answer,
		}
	}
	return result
}
