package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// authState manages auth token and session cookies
type authState struct {
	token    string
	sessions map[string]time.Time
	mu       sync.Mutex
	ttl      time.Duration
}

const sessionCookie = "sc_session"

func newAuthState(token string) *authState {
	return &authState{
		token:    strings.TrimSpace(token),
		sessions: make(map[string]time.Time),
		ttl:      24 * time.Hour,
	}
}

// generateToken creates 32 random bytes hex token
func generateToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// tokenMatches compares tokens in constant time
func (a *authState) tokenMatches(got string) bool {
	if a == nil || a.token == "" || got == "" {
		return false
	}
	want := sha256.Sum256([]byte(a.token))
	have := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(want[:], have[:]) == 1
}

func (a *authState) newSession() (string, bool) {
	if a == nil {
		return "", false
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false
	}
	id := hex.EncodeToString(b[:])
	a.mu.Lock()
	a.sessions[id] = time.Now().Add(a.ttl)
	a.mu.Unlock()
	return id, true
}

func (a *authState) validSession(id string) bool {
	if a == nil || id == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.sessions, id)
		return false
	}
	return true
}

// bearerToken extracts bearer token from header
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// authorized checks bearer token, session cookie, or query token
func (a *authState) authorized(r *http.Request) bool {
	if a == nil {
		return false
	}
	if a.token != "" && a.tokenMatches(bearerToken(r)) {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && a.validSession(c.Value) {
		return true
	}
	if a.token != "" && a.tokenMatches(r.URL.Query().Get("token")) {
		return true
	}
	if a.token == "" && isLoopbackAddr(r.RemoteAddr) {
		return true
	}
	return false
}

// requireAuth middleware for protected endpoints
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="spermacore"`)
			writeJSONError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

// checkOrigin validates request origin against allowed hostnames
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Host)
	if r.Host != "" && strings.EqualFold(host, strings.ToLower(r.Host)) {
		return true
	}
	allow := []string{
		net.JoinHostPort("127.0.0.1", strconv.Itoa(s.cfg.Port)),
		net.JoinHostPort("localhost", strconv.Itoa(s.cfg.Port)),
		net.JoinHostPort("::1", strconv.Itoa(s.cfg.Port)),
	}
	if s.cfg.Bind != "" && s.cfg.Bind != "0.0.0.0" {
		allow = append(allow, net.JoinHostPort(s.cfg.Bind, strconv.Itoa(s.cfg.Port)))
	}
	for _, a := range allow {
		if host == strings.ToLower(a) {
			return true
		}
	}
	return false
}

// originGuard enforces origin validation on mutations
func (s *Server) originGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.checkOrigin(r) {
			writeJSONError(w, http.StatusForbidden, "origin not allowed")
			return
		}
		next(w, r)
	}
}

func isLoopbackAddr(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func isLoopbackBind(bind string) bool {
	b := strings.TrimSpace(bind)
	if b == "" {
		return false
	}
	if strings.EqualFold(b, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(b, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.checkOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad json")
		return
	}
	if !s.auth.tokenMatches(req.Token) {
		writeJSONError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	id, ok := s.auth.newSession()
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "session unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.auth.ttl / time.Second),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

// decodeJSON reads bounded request body
func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst)
}
