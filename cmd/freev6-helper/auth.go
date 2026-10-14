package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireAuth gates every helper API request on the per-launch bearer token
// the desktop shell passes through FREEV6_API_TOKEN. An empty server token
// rejects everything (fail closed): a helper started without the env var —
// a manual `go run`, a misconfigured spawn — must never serve an unauthenticated
// API. OPTIONS preflights never carry Authorization; withCORS answers them
// before this middleware runs.
func requireAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		presented := bearerToken(request.Header.Get("Authorization"))
		if token == "" || presented == "" ||
			subtle.ConstantTimeCompare([]byte(token), []byte(presented)) != 1 {
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// bearerToken extracts the credential from an "Authorization: Bearer <token>"
// header. Only the exact scheme spelling our client sends is accepted — no
// folding, no alternatives.
func bearerToken(header string) string {
	rest, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(rest)
}
