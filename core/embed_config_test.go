package main

import "testing"

func embedTestConfig() *Config {
	return &Config{
		LLM: LLMConfig{
			Provider: "openrouter",
			Providers: map[string]ProviderConfig{
				"openrouter": {BaseURL: "https://openrouter.ai/api/v1", APIKey: "or-key"},
				"openai":     {BaseURL: "https://api.openai.com/v1", APIKey: "oa-key"},
				"groq":       {BaseURL: "https://api.groq.com/openai/v1", APIKey: "groq-key"},
			},
		},
		PluginSections: map[string]map[string]any{},
	}
}

func TestEmbeddingKeyIsDeterministic(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := embedTestConfig()

	first := resolveEmbeddingSettings(cfg)
	if first.Provider != "openrouter" || first.Key != "or-key" {
		t.Fatalf("default provider: got %s/%s, want openrouter/or-key", first.Provider, first.Key)
	}
	for i := 0; i < 200; i++ {
		got := resolveEmbeddingSettings(cfg)
		if got != first {
			t.Fatalf("iteration %d: settings changed: %+v vs %+v", i, got, first)
		}
	}
}

func TestEmbeddingExplicitRouteWins(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := embedTestConfig()
	cfg.LLM.Routing = map[string]ModelRoute{
		"embedding": {Provider: "openai", Model: "text-embedding-3-small"},
	}

	got := resolveEmbeddingSettings(cfg)
	if got.Provider != "openai" || got.Key != "oa-key" {
		t.Fatalf("explicit route: got %s/%s, want openai/oa-key", got.Provider, got.Key)
	}
	if got.Model != "text-embedding-3-small" {
		t.Fatalf("model = %q", got.Model)
	}
}

func TestEmbeddingDoesNotBorrowForeignKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := &Config{
		LLM: LLMConfig{
			Provider: "groq",
			Providers: map[string]ProviderConfig{
				"groq":       {BaseURL: "https://api.groq.com/openai/v1"},
				"openrouter": {BaseURL: "https://openrouter.ai/api/v1", APIKey: "or-key"},
			},
		},
	}
	got := resolveEmbeddingSettings(cfg)
	if !got.Disabled {
		t.Fatalf("embedding must be disabled without its own key, got %+v", got)
	}
	if got.Key != "" {
		t.Fatalf("foreign key leaked: %q", got.Key)
	}
}

func TestEmbeddingEnvKeyOnlyForSelectedProvider(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "env-openai")
	cfg := &Config{
		LLM: LLMConfig{
			Provider:  "openrouter",
			Providers: map[string]ProviderConfig{"openrouter": {BaseURL: "https://openrouter.ai/api/v1"}},
		},
	}
	if got := resolveEmbeddingSettings(cfg); !got.Disabled || got.Key != "" {
		t.Fatalf("OPENAI env key must not be used for openrouter: %+v", got)
	}

	cfg.LLM.Provider = "openai"
	if got := resolveEmbeddingSettings(cfg); got.Key != "env-openai" {
		t.Fatalf("openai env key not picked: %+v", got)
	}
}

func TestEmbeddingFallbackRequiresMatchingBase(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := &Config{
		LLM: LLMConfig{
			Provider: "custom",
			Providers: map[string]ProviderConfig{
				"custom":     {BaseURL: "https://custom.example/v1"},
				"openrouter": {BaseURL: "https://openrouter.ai/api/v1", APIKey: "or-key"},
			},
		},
	}
	got := resolveEmbeddingSettings(cfg)
	if !got.Disabled {
		t.Fatalf("fallback must not move openrouter key to another endpoint: %+v", got)
	}
}

func TestEmbeddingCanBeDisabledViaConfig(t *testing.T) {
	cfg := embedTestConfig()
	disabled := false
	cfg.Memory.Enabled = &disabled
	got := resolveEmbeddingSettings(cfg)
	if !got.Disabled {
		t.Fatalf("expected embedding to be disabled when memory.enabled = false")
	}
}
