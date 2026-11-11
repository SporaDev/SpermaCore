package main

import "testing"

// custom openai-compatible provider in [llm.providers.*] should build successfully
func TestBuildProviderCustomOpenAICompatible(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "reformboss"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"reformboss": {
			BaseURL:        "https://api.reformboss.com/v1",
			APIKey:         "sk-test",
			TimeoutSeconds: 10,
			MaxConcurrent:  3,
		},
	}

	p, endpoint, maxConc, err := buildProvider(cfg)
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	if p == nil {
		t.Fatal("buildProvider returned nil provider")
	}
	if endpoint != "https://api.reformboss.com/v1" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if maxConc != 3 {
		t.Fatalf("maxConc = %d", maxConc)
	}
}

func TestBuildProviderMissingBaseURL(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.Provider = "custom"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"custom": {APIKey: "sk-test"},
	}

	if _, _, _, err := buildProvider(cfg); err == nil {
		t.Fatal("expected error for missing base_url")
	}
}
