package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeState struct{}

func (fakeState) Snapshot() Snapshot { return Snapshot{Version: "test"} }

func newTestServer(t *testing.T, cfg Config) (*Server, *httptest.Server) {
	t.Helper()
	if cfg.Port == 0 {
		cfg.Port = 8080
	}
	s := NewServer(cfg, NewHub(), fakeState{})
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func doReq(t *testing.T, method, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthRequired(t *testing.T) {
	_, ts := newTestServer(t, Config{AuthToken: "secret"})

	for _, path := range []string{"/api/state", "/ws"} {
		resp := doReq(t, http.MethodGet, ts.URL+path, "", nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token: status %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestBearerToken(t *testing.T) {
	_, ts := newTestServer(t, Config{AuthToken: "secret"})

	resp := doReq(t, http.MethodGet, ts.URL+"/api/state", "", map[string]string{
		"Authorization": "Bearer secret",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid bearer: status %d, want 200", resp.StatusCode)
	}

	resp = doReq(t, http.MethodGet, ts.URL+"/api/state", "", map[string]string{
		"Authorization": "Bearer wrong",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: status %d, want 401", resp.StatusCode)
	}
}

func TestLoginSetsCookie(t *testing.T) {
	_, ts := newTestServer(t, Config{AuthToken: "secret"})

	resp := doReq(t, http.MethodPost, ts.URL+"/api/login", `{"token":"secret"}`, map[string]string{
		"Content-Type": "application/json",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d, want 200", resp.StatusCode)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login did not set sc_session cookie")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req.AddCookie(session)
	stateResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	stateResp.Body.Close()
	if stateResp.StatusCode != http.StatusOK {
		t.Fatalf("state with cookie: status %d, want 200", stateResp.StatusCode)
	}
}

func TestLoginRejectsBadToken(t *testing.T) {
	_, ts := newTestServer(t, Config{AuthToken: "secret"})
	resp := doReq(t, http.MethodPost, ts.URL+"/api/login", `{"token":"nope"}`, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: status %d, want 401", resp.StatusCode)
	}
}

func TestForeignOriginRejected(t *testing.T) {
	_, ts := newTestServer(t, Config{AuthToken: "secret"})

	resp := doReq(t, http.MethodPost, ts.URL+"/api/login", `{"token":"secret"}`, map[string]string{
		"Origin": "http://evil.example",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin login: status %d, want 403", resp.StatusCode)
	}

	resp = doReq(t, http.MethodGet, ts.URL+"/ws?token=secret", "", map[string]string{
		"Origin": "http://evil.example",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin ws: status %d, want 403", resp.StatusCode)
	}
}

func TestStartRefusesNonLoopbackWithoutToken(t *testing.T) {
	s := NewServer(Config{Bind: "0.0.0.0", Port: 18099}, NewHub(), fakeState{})
	if err := s.Start(); err == nil {
		s.Stop()
		t.Fatal("Start on non-loopback bind without token must fail")
	}
}

func TestLoadAuthFields(t *testing.T) {
	cfg := Load(map[string]any{
		"enabled":    true,
		"auth_token": " tok ",
	})
	if cfg.AuthToken != "tok" {
		t.Errorf("AuthToken = %q, want tok", cfg.AuthToken)
	}
}

func TestDefaultsWithAuthToken(t *testing.T) {
	a := DefaultsWithAuthToken()
	b := DefaultsWithAuthToken()
	if a["auth_token"] == "" || a["auth_token"] == b["auth_token"] {
		t.Fatalf("auth tokens should be non-empty and unique")
	}
	if _, ok := Defaults["auth_token"]; !ok {
		t.Error("Defaults must declare auth_token")
	}
}
