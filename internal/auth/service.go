package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrEmailExists        = errors.New("email already exists")
	ErrAccountNotFound    = errors.New("account not found")
)

const SessionCookieName = "visitready_session"

type Account struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type accountRecord struct {
	Account
	PasswordHash []byte
}

type Store interface {
	Create(context.Context, accountRecord) error
	ByEmail(context.Context, string) (accountRecord, error)
	ByID(context.Context, string) (accountRecord, error)
	PutToken(context.Context, string, string, time.Time) error
	AccountByToken(context.Context, string, time.Time) (string, error)
	DeleteToken(context.Context, string) error
}

type Service struct {
	store      Store
	sessionTTL time.Duration
}

func NewService(store Store) *Service { return NewServiceWithTTL(store, 7*24*time.Hour) }

func NewServiceWithTTL(store Store, sessionTTL time.Duration) *Service {
	if sessionTTL <= 0 {
		sessionTTL = 7 * 24 * time.Hour
	}
	return &Service{store: store, sessionTTL: sessionTTL}
}

func (s *Service) Register(ctx context.Context, email, password string) (Account, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return Account{}, err
	}
	if len([]rune(password)) < 12 || len([]rune(password)) > 128 {
		return Account{}, errors.New("password must contain 12 to 128 characters")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return Account{}, fmt.Errorf("hash password: %w", err)
	}
	id, err := randomID(16)
	if err != nil {
		return Account{}, err
	}
	record := accountRecord{Account: Account{ID: id, Email: email}, PasswordHash: hash}
	if err := s.store.Create(ctx, record); err != nil {
		return Account{}, err
	}
	return record.Account, nil
}

func (s *Service) Login(ctx context.Context, email, password string) (string, Account, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return "", Account{}, ErrInvalidCredentials
	}
	record, err := s.store.ByEmail(ctx, email)
	if err != nil || !verifyPassword(record.PasswordHash, password) {
		return "", Account{}, ErrInvalidCredentials
	}
	token, err := randomID(32)
	if err != nil {
		return "", Account{}, err
	}
	if err := s.store.PutToken(ctx, tokenHash(token), record.ID, time.Now().Add(s.sessionTTL)); err != nil {
		return "", Account{}, err
	}
	return token, record.Account, nil
}

func hashPassword(password string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate password salt: %w", err)
	}
	derived := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	encoded := "argon2id$v=19$m=19456,t=2,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(derived)
	return []byte(encoded), nil
}

func verifyPassword(encoded []byte, password string) bool {
	parts := strings.Split(string(encoded), "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" || parts[2] != "m=19456,t=2,p=1" {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[3])
	expected, hashErr := base64.RawStdEncoding.DecodeString(parts[4])
	if saltErr != nil || hashErr != nil || len(salt) != 16 || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func (s *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	if strings.TrimSpace(token) == "" {
		return Account{}, ErrInvalidCredentials
	}
	id, err := s.store.AccountByToken(ctx, tokenHash(token), time.Now())
	if err != nil {
		return Account{}, ErrInvalidCredentials
	}
	record, err := s.store.ByID(ctx, id)
	if err != nil {
		return Account{}, ErrInvalidCredentials
	}
	return record.Account, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return s.store.DeleteToken(ctx, tokenHash(token))
}

func normalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 320 {
		return "", errors.New("a valid email address is required")
	}
	return value, nil
}

func randomID(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate authentication token: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func OwnerHash(accountID string) string {
	digest := sha256.Sum256([]byte(accountID))
	return hex.EncodeToString(digest[:])
}

type MemoryStore struct {
	mu       sync.RWMutex
	accounts map[string]accountRecord
	byEmail  map[string]string
	tokens   map[string]tokenRecord
}

type tokenRecord struct {
	accountID string
	expiresAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{accounts: make(map[string]accountRecord), byEmail: make(map[string]string), tokens: make(map[string]tokenRecord)}
}

func (s *MemoryStore) Create(ctx context.Context, record accountRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byEmail[record.Email]; ok {
		return ErrEmailExists
	}
	s.accounts[record.ID] = record
	s.byEmail[record.Email] = record.ID
	return nil
}

func (s *MemoryStore) ByEmail(ctx context.Context, email string) (accountRecord, error) {
	if err := ctx.Err(); err != nil {
		return accountRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byEmail[email]
	if !ok {
		return accountRecord{}, ErrAccountNotFound
	}
	return s.accounts[id], nil
}

func (s *MemoryStore) ByID(ctx context.Context, id string) (accountRecord, error) {
	if err := ctx.Err(); err != nil {
		return accountRecord{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.accounts[id]
	if !ok {
		return accountRecord{}, ErrAccountNotFound
	}
	return record, nil
}

func (s *MemoryStore) PutToken(ctx context.Context, token, accountID string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = tokenRecord{accountID: accountID, expiresAt: expiresAt}
	return nil
}

func (s *MemoryStore) AccountByToken(ctx context.Context, token string, now time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.tokens[token]
	if !ok || !record.expiresAt.After(now) {
		return "", ErrInvalidCredentials
	}
	return record.accountID, nil
}

func (s *MemoryStore) DeleteToken(ctx context.Context, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
	return nil
}
