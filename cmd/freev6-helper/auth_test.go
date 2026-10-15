package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func authTestMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	})
	return mux
}

// fullChain mirrors main(): CORS outermost (answers OPTIONS preflight),
// then the Host/Origin guard, then the bearer check.
func fullChain(token string) http.Handler {
	return withCORS(guardLocalOnly(requireAuth(token, authTestMux())))
}

// newLoopbackRequest builds a request the way the real client addresses the
// helper (Host carries the loopback port). httptest's default Host would be
// rejected by guardLocalOnly on purpose.
func newLoopbackRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.Host = "127.0.0.1:13335"
	return request
}

func TestRequireAuth(t *testing.T) {
	const token = "test-token-abc123"
	handler := fullChain(token)

	cases := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong-token", http.StatusUnauthorized},
		{"prefix only", "Bearer ", http.StatusUnauthorized},
		{"missing scheme", token, http.StatusUnauthorized},
		{"wrong scheme", "Basic " + token, http.StatusUnauthorized},
		{"correct token", "Bearer " + token, http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := newLoopbackRequest(http.MethodGet, "/api/v1/ping")
			if testCase.header != "" {
				request.Header.Set("Authorization", testCase.header)
			}
			handler.ServeHTTP(recorder, request)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
		})
	}
}

// CORS preflight must succeed without a token: browsers never attach
// Authorization to OPTIONS, and the token header only reaches the server
// after the preflight has passed.
func TestRequireAuthAllowsPreflightWithoutToken(t *testing.T) {
	handler := fullChain("test-token-abc123")
	recorder := httptest.NewRecorder()
	request := newLoopbackRequest(http.MethodOptions, "/api/v1/ping")
	// Even a hostile Origin gets the preflight answered — the actual request
	// that follows is then stopped by guardLocalOnly/requireAuth.
	request.Header.Set("Origin", "https://evil.example")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", recorder.Code)
	}
}

// A helper started without FREEV6_API_TOKEN (empty server token) must reject
// every request, including clients that also send an empty bearer credential.
func TestRequireAuthEmptyServerTokenRejectsAll(t *testing.T) {
	handler := fullChain("")
	headers := []string{"", "Bearer ", "Bearer anything"}
	for _, header := range headers {
		recorder := httptest.NewRecorder()
		request := newLoopbackRequest(http.MethodGet, "/api/v1/ping")
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: status = %d, want 401", header, recorder.Code)
		}
	}
}

func TestGuardLocalOnly(t *testing.T) {
	const token = "test-token-abc123"
	handler := fullChain(token)

	cases := []struct {
		name       string
		host       string
		origin     string
		wantStatus int
	}{
		// Host: DNS rebinding sends a foreign authority — stop before auth.
		{"rebound host", "evil.example:13335", "", http.StatusForbidden},
		{"rebound host without port", "evil.example", "", http.StatusForbidden},
		// Origin: a real website cross-origin call reaches the loopback host
		// but carries its own Origin — also stopped before auth.
		{"foreign https origin", "127.0.0.1:13335", "https://evil.example", http.StatusForbidden},
		{"foreign http origin", "127.0.0.1:13335", "http://attacker.example:8080", http.StatusForbidden},
		// Legit shapes: loopback/Tauri origins, non-http schemes, absent.
		{"loopback origin", "127.0.0.1:13335", "http://127.0.0.1:13335", http.StatusOK},
		{"tauri origin", "127.0.0.1:13335", "http://tauri.localhost", http.StatusOK},
		{"tauri scheme origin", "127.0.0.1:13335", "tauri://localhost", http.StatusOK},
		{"opaque origin", "127.0.0.1:13335", "null", http.StatusOK},
		{"localhost host", "localhost:13335", "", http.StatusOK},
		{"no origin", "127.0.0.1:13335", "", http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := newLoopbackRequest(http.MethodGet, "/api/v1/ping")
			request.Host = testCase.host
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			request.Header.Set("Authorization", "Bearer "+token)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
		})
	}
}

// The guard runs BEFORE the bearer check: a rejected host/origin must yield
// 403 even when no token is presented (no oracle, clear semantics).
func TestGuardRejectsBeforeAuth(t *testing.T) {
	handler := fullChain("test-token-abc123")
	recorder := httptest.NewRecorder()
	request := newLoopbackRequest(http.MethodGet, "/api/v1/ping")
	request.Host = "evil.example:13335"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}
