package main

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
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
		// CORS preflights never carry Authorization; the Host/Origin guard has
		// already rejected foreign origins, so answering here (before the token
		// check) is safe.
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
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

// guardLocalOnly rejects requests not addressed to the loopback helper from
// a local/Tauri origin — DNS rebinding (Host: evil.example) and hostile web
// pages stop here, before the bearer check. Deliberately a blocklist of
// foreign http(s) Origins only: any other Origin shape (tauri://, null,
// absent) falls through to requireAuth, so an unexpected webview origin
// form can never brick the app while the token remains the real gate.
func guardLocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !allowedHost(request.Host) || !allowedOrigin(request.Header.Get("Origin")) {
			writeJSON(writer, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// allowedHost strips the port (RFC 7230 Host may carry one) and accepts
// only the loopback names the helper is reachable under.
func allowedHost(hostPort string) bool {
	host := hostPort
	if parsed, _, err := net.SplitHostPort(hostPort); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost":
		return true
	}
	return false
}

// allowedOrigin permits: no Origin at all (curl/CLI — the bearer token is the
// gate), the Tauri webview origins, and local dev origins. The opaque "null"
// origin (sandboxed iframes) is rejected: combined with the CORS headers it
// would let a hostile page read the token-authenticated API responses.
func allowedOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch {
	case parsed.Scheme == "http" || parsed.Scheme == "https":
		switch strings.ToLower(parsed.Hostname()) {
		case "127.0.0.1", "localhost", "tauri.localhost":
			return true
		}
		return false
	case parsed.Scheme == "tauri" && parsed.Host == "localhost":
		return true
	}
	return false
}
