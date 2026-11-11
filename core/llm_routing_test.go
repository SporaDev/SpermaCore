package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// verify role-based model and provider routing
func TestModelRoutingPerRole(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "reformboss"
	cfg.LLM.Model = "glm-5.3"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"reformboss": {BaseURL: "https://api.reformboss.com/v1", APIKey: "k1"},
		"openrouter": {BaseURL: "https://openrouter.ai/api/v1", APIKey: "k2"},
	}
	cfg.LLM.Models = map[string]string{}
	cfg.LLM.Routing = map[string]ModelRoute{
		"vision": {Provider: "openrouter", Model: "google/gemini-2.0-flash"},
	}
	cfg.applyDefaults()

	if got, want := cfg.ModelFor("chat"), "glm-5.3"; got != want {
		t.Fatalf("chat model = %q, want %q", got, want)
	}
	if got, want := cfg.ModelFor("vision"), "openrouter/google/gemini-2.0-flash"; got != want {
		t.Fatalf("vision model = %q, want %q", got, want)
	}
	if got, want := cfg.ProviderFor("vision"), "openrouter"; got != want {
		t.Fatalf("vision provider = %q, want %q", got, want)
	}
	if got, want := cfg.ProviderFor("chat"), "reformboss"; got != want {
		t.Fatalf("chat provider = %q, want %q", got, want)
	}
	if got, want := cfg.ModelNameFor("vision"), "google/gemini-2.0-flash"; got != want {
		t.Fatalf("vision model name = %q, want %q", got, want)
	}
}

// legacy [llm.models] syntax support
func TestModelRoutingLegacyModelsTable(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "reformboss"
	cfg.LLM.Model = "glm-5.3"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"reformboss": {BaseURL: "https://api.reformboss.com/v1", APIKey: "k1"},
		"openrouter": {BaseURL: "https://openrouter.ai/api/v1", APIKey: "k2"},
	}
	cfg.LLM.Models = map[string]string{"vision": "openrouter/google/gemini-2.0-flash"}
	cfg.applyDefaults()

	if got, want := cfg.ModelFor("vision"), "openrouter/google/gemini-2.0-flash"; got != want {
		t.Fatalf("vision model = %q, want %q", got, want)
	}
	if got, want := cfg.ProviderFor("vision"), "openrouter"; got != want {
		t.Fatalf("vision provider = %q, want %q", got, want)
	}
}

// slash in model name without provider prefix is treated as model name
func TestModelForUnknownPrefixIsModelName(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "reformboss"
	cfg.LLM.Model = "glm-5.3"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"reformboss": {BaseURL: "https://api.reformboss.com/v1", APIKey: "k1"},
	}
	cfg.LLM.Models = map[string]string{"vision": "google/gemini-3.8-flash"}
	cfg.applyDefaults()

	if got, want := cfg.ModelNameFor("vision"), "google/gemini-3.8-flash"; got != want {
		t.Fatalf("vision model name = %q, want %q", got, want)
	}
	if got, want := cfg.ProviderFor("vision"), "reformboss"; got != want {
		t.Fatalf("vision provider = %q, want %q", got, want)
	}
}

// legacy ollama/ prefix support
func TestModelForLegacyOllamaPrefix(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "openrouter"
	cfg.LLM.Model = "some/model"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openrouter": {BaseURL: "https://openrouter.ai/api/v1", APIKey: "k"},
	}
	cfg.LLM.Models = map[string]string{"vision": "ollama/llava"}
	cfg.applyDefaults()

	if got, want := cfg.ModelNameFor("vision"), "llava"; got != want {
		t.Fatalf("vision model name = %q, want %q", got, want)
	}
	if got, want := cfg.ProviderFor("vision"), "openrouter"; got != want {
		t.Fatalf("vision provider = %q, want %q", got, want)
	}
}

func TestValidateRejectsUnknownRouteProvider(t *testing.T) {
	cfg := &Config{}
	cfg.Core.LogLevel = "INFO"
	cfg.LLM.Provider = "reformboss"
	cfg.LLM.Model = "glm-5.3"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"reformboss": {BaseURL: "https://api.reformboss.com/v1", APIKey: "k1"},
	}
	cfg.LLM.Routing = map[string]ModelRoute{
		"vision": {Provider: "nope", Model: "m"},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for unknown route provider")
	}
}

// router strips provider prefix before sending model name
func TestProviderRouterDispatchesByPrefix(t *testing.T) {
	type call struct {
		model string
		auth  string
	}
	makeServer := func(name string, got *call) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &req)
			got.model = req.Model
			got.auth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"" + name + "\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}))
	}

	var defCall, altCall call
	defSrv := makeServer("default", &defCall)
	defer defSrv.Close()
	altSrv := makeServer("alt", &altCall)
	defer altSrv.Close()

	cfg := &Config{}
	cfg.LLM.Provider = "main"
	cfg.LLM.Model = "main-model"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"main": {BaseURL: defSrv.URL, APIKey: "key-main"},
		"alt":  {BaseURL: altSrv.URL, APIKey: "key-alt"},
	}

	router, _, _, err := buildProviders(cfg)
	if err != nil {
		t.Fatalf("buildProviders: %v", err)
	}

	ctx := context.Background()
	msgs := []ChatMessage{{Role: "user", Content: "hi"}}

	// without prefix: use default provider
	ch := router.ChatStream(ctx, "main-model", msgs, nil)
	var out strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("default stream: %v", chunk.Err)
		}
		out.WriteString(chunk.Delta)
	}
	if out.String() != "default" {
		t.Fatalf("default response = %q", out.String())
	}
	if defCall.model != "main-model" || defCall.auth != "Bearer key-main" {
		t.Fatalf("default call = %+v", defCall)
	}

	// with prefix: route to specified provider
	out.Reset()
	ch = router.ChatStream(ctx, "alt/vision-model", msgs, nil)
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("alt stream: %v", chunk.Err)
		}
		out.WriteString(chunk.Delta)
	}
	if out.String() != "alt" {
		t.Fatalf("alt response = %q", out.String())
	}
	if altCall.model != "vision-model" || altCall.auth != "Bearer key-alt" {
		t.Fatalf("alt call = %+v", altCall)
	}
}

// custom headers and extra fields passed without overriding core fields
func TestProviderCustomHeadersAndExtra(t *testing.T) {
	var gotAPIKey, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("api-key")
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		gotModel, _ = req["model"].(string)
		if req["provider"] != nil {
			t.Errorf("extra must not override model-related fields, got %v", req["provider"])
		}
		if req["reasoning"] != "high" {
			t.Errorf("extra reasoning missing: %v", req["reasoning"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := &Config{}
	cfg.LLM.Provider = "azure"
	cfg.LLM.Model = "gpt-4o"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"azure": {
			BaseURL: srv.URL,
			APIKey:  "",
			Headers: map[string]string{"api-key": "secret"},
			Extra:   map[string]any{"reasoning": "high"},
		},
	}
	router, _, _, err := buildProviders(cfg)
	if err != nil {
		t.Fatalf("buildProviders: %v", err)
	}
	for chunk := range router.ChatStream(context.Background(), "gpt-4o",
		[]ChatMessage{{Role: "user", Content: "hi"}}, nil) {
		if chunk.Err != nil {
			t.Fatalf("stream: %v", chunk.Err)
		}
	}
	if gotAPIKey != "secret" {
		t.Fatalf("api-key header = %q", gotAPIKey)
	}
	if gotModel != "gpt-4o" {
		t.Fatalf("model = %q", gotModel)
	}
}

// A slash inside a model id that is not a configured provider must be forwarded
// verbatim to the default provider instead of silently falling back to the
// default model. Regression: vision routing to "google/gemini-..." broke.
func TestResolveForwardsUnknownSlashPrefix(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := &Config{}
	cfg.LLM.Provider = "main"
	cfg.LLM.Model = "main-model"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"main": {BaseURL: srv.URL, APIKey: "key-main"},
	}
	router, _, _, err := buildProviders(cfg)
	if err != nil {
		t.Fatalf("buildProviders: %v", err)
	}

	// Unqualified slash model: leading segment is part of the name, not a
	// provider, so the default provider receives the full model id.
	for chunk := range router.ChatStream(context.Background(), "google/gemini-2.0-flash",
		[]ChatMessage{{Role: "user", Content: "hi"}}, nil) {
		if chunk.Err != nil {
			t.Fatalf("stream: %v", chunk.Err)
		}
	}
	if gotModel != "google/gemini-2.0-flash" {
		t.Fatalf("model forwarded = %q, want %q", gotModel, "google/gemini-2.0-flash")
	}

	// Explicit provider prefix is still stripped before dispatch.
	gotModel = ""
	cfgWithRoute := &Config{}
	cfgWithRoute.LLM.Provider = "main"
	cfgWithRoute.LLM.Model = "main-model"
	cfgWithRoute.LLM.Providers = map[string]ProviderConfig{
		"main": {BaseURL: srv.URL, APIKey: "key-main"},
	}
	router2, _, _, err := buildProviders(cfgWithRoute)
	if err != nil {
		t.Fatalf("buildProviders: %v", err)
	}
	for chunk := range router2.ChatStream(context.Background(), "main/google/gemini-2.0-flash",
		[]ChatMessage{{Role: "user", Content: "hi"}}, nil) {
		if chunk.Err != nil {
			t.Fatalf("stream: %v", chunk.Err)
		}
	}
	if gotModel != "google/gemini-2.0-flash" {
		t.Fatalf("prefixed model dispatched = %q, want %q", gotModel, "google/gemini-2.0-flash")
	}
}
