package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"visitready/internal/httpapi"
	"visitready/internal/session"
)

const (
	authTestDeviceKey      = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	authTestOtherDeviceKey = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

func TestSessionAPIRequiresDeviceKey(t *testing.T) {
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: session.NewMemoryStore(), Now: time.Now, RequireDeviceAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"input":"这是一段长度足够且已经脱敏的健康情况描述。"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusUnauthorized, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "device_key_required")
}

func TestSessionAccessIsBoundToCreatingDevice(t *testing.T) {
	store := session.NewMemoryStore()
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: time.Now, RequireDeviceAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	created := authenticatedRequest(handler, authTestDeviceKey, http.MethodPost, "/api/v1/sessions", `{"input":"这是一段长度足够且已经脱敏的健康情况描述。"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body = %s", created.Code, created.Body.String())
	}
	var payload struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ID == "" {
		t.Fatal("created session has no ID")
	}

	wrongDevice := authenticatedRequest(handler, authTestOtherDeviceKey, http.MethodGet, "/api/v1/sessions/"+payload.Data.ID, "")
	if wrongDevice.Code != http.StatusNotFound {
		t.Fatalf("wrong-device status = %d, want %d; body = %s", wrongDevice.Code, http.StatusNotFound, wrongDevice.Body.String())
	}
	correctDevice := authenticatedRequest(handler, authTestDeviceKey, http.MethodGet, "/api/v1/sessions/"+payload.Data.ID, "")
	if correctDevice.Code != http.StatusOK {
		t.Fatalf("correct-device status = %d; body = %s", correctDevice.Code, correctDevice.Body.String())
	}
	if bytes.Contains(correctDevice.Body.Bytes(), []byte("owner")) || bytes.Contains(correctDevice.Body.Bytes(), []byte(authTestDeviceKey)) {
		t.Fatalf("response exposes ownership material: %s", correctDevice.Body.String())
	}
}

func TestSessionWriteRejectsCrossSiteOrigin(t *testing.T) {
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: session.NewMemoryStore(), Now: time.Now, RequireDeviceAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://visitready.example/api/v1/sessions", strings.NewReader(`{"input":"这是一段长度足够且已经脱敏的健康情况描述。"}`))
	request.Host = "visitready.example"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(httpapi.DeviceKeyHeader, authTestDeviceKey)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusForbidden, response.Body.String())
	}
	assertErrorCode(t, response.Body.Bytes(), "cross_site_request")
}

func TestSessionCollectionListsOnlyCurrentDeviceMetadata(t *testing.T) {
	store := session.NewMemoryStore()
	handler, err := httpapi.New(httpapi.Config{Runner: &fakeRunner{}, Store: store, Now: time.Now, RequireDeviceAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	created := authenticatedRequest(handler, authTestDeviceKey, http.MethodPost, "/api/v1/sessions", `{"input":"这是一段长度足够且已经脱敏的健康情况描述。"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body = %s", created.Code, created.Body.String())
	}
	owned := authenticatedRequest(handler, authTestDeviceKey, http.MethodGet, "/api/v1/sessions", "")
	if owned.Code != http.StatusOK {
		t.Fatalf("list status = %d; body = %s", owned.Code, owned.Body.String())
	}
	if !bytes.Contains(owned.Body.Bytes(), []byte(`"id":"session-1"`)) {
		t.Fatalf("owned list is missing session: %s", owned.Body.String())
	}
	for _, forbidden := range [][]byte{[]byte("facts"), []byte("咳嗽三天"), []byte("owner_hash")} {
		if bytes.Contains(owned.Body.Bytes(), forbidden) {
			t.Fatalf("history list exposes private field %q: %s", forbidden, owned.Body.String())
		}
	}
	other := authenticatedRequest(handler, authTestOtherDeviceKey, http.MethodGet, "/api/v1/sessions", "")
	if other.Code != http.StatusOK || bytes.Contains(other.Body.Bytes(), []byte("session-1")) {
		t.Fatalf("other-device list = %d %s", other.Code, other.Body.String())
	}
}

func authenticatedRequest(handler http.Handler, deviceKey, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set(httpapi.DeviceKeyHeader, deviceKey)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
