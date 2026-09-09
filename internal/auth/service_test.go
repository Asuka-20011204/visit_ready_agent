package auth

import (
	"context"
	"strings"
	"testing"
)

func TestServiceRegisterLoginAndRevoke(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	account, err := service.Register(context.Background(), "User@Example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if account.Email != "user@example.com" || account.ID == "" {
		t.Fatalf("account = %#v", account)
	}
	token, loggedIn, err := service.Login(context.Background(), "user@example.com", "correct horse battery staple")
	if err != nil || token == "" || loggedIn.ID != account.ID {
		t.Fatalf("login token=%q account=%#v err=%v", token, loggedIn, err)
	}
	resolved, err := service.Authenticate(context.Background(), token)
	if err != nil || resolved.ID != account.ID {
		t.Fatalf("authenticate account=%#v err=%v", resolved, err)
	}
	if err := service.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(context.Background(), token); err == nil {
		t.Fatal("revoked token was accepted")
	}
}

func TestServiceRejectsDuplicateAndWrongPassword(t *testing.T) {
	service := NewService(NewMemoryStore())
	if _, err := service.Register(context.Background(), "person@example.com", "long enough password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(context.Background(), "PERSON@example.com", "another long password"); err == nil {
		t.Fatal("duplicate email accepted")
	}
	if _, _, err := service.Login(context.Background(), "person@example.com", "wrong password"); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestServiceValidatesRegistration(t *testing.T) {
	service := NewService(NewMemoryStore())
	for _, tc := range []struct{ email, password string }{
		{"", "long enough password"},
		{"not-an-email", "long enough password"},
		{"a@example.com", "short"},
		{"a@example.com", strings.Repeat("x", 129)},
	} {
		if _, err := service.Register(context.Background(), tc.email, tc.password); err == nil {
			t.Fatalf("invalid registration accepted: %#v", tc)
		}
	}
}
