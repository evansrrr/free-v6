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

func TestRequireAuth(t *testing.T) {
	const token = "test-token-abc123"
	handler := withCORS(requireAuth(token, authTestMux()))

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
			request := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
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
	handler := withCORS(requireAuth("test-token-abc123", authTestMux()))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/ping", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", recorder.Code)
	}
}

// A helper started without FREEV6_API_TOKEN (empty server token) must reject
// every request, including clients that also send an empty bearer credential.
func TestRequireAuthEmptyServerTokenRejectsAll(t *testing.T) {
	handler := withCORS(requireAuth("", authTestMux()))
	headers := []string{"", "Bearer ", "Bearer anything"}
	for _, header := range headers {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: status = %d, want 401", header, recorder.Code)
		}
	}
}
