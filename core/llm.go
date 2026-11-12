package main

import (
	"fmt"
	"strings"
	"time"
)

// buildProvider creates default provider from [llm]
func buildProvider(cfg *Config) (LLMProvider, string, int, error) {
	return buildProviderNamed(cfg, cfg.LLM.Provider)
}

// buildProviderNamed creates named provider
func buildProviderNamed(cfg *Config, rawName string) (LLMProvider, string, int, error) {
	name := strings.ToLower(strings.TrimSpace(rawName))
	pc, ok := cfg.LLM.Providers[name]
	if !ok {
		return nil, "", 0, fmt.Errorf("provider %q not configured in [llm.providers.%s]", name, name)
	}

	timeout := time.Duration(pc.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	maxConc := pc.MaxConcurrent
	if maxConc <= 0 {
		maxConc = 2
	}

	typ := strings.ToLower(strings.TrimSpace(pc.Type))
	if typ == "" {
		if name == "ollama" {
			typ = "ollama"
		} else {
			typ = "openai"
		}
	}

	switch typ {
	case "ollama":
		base := pc.BaseURL
		if base == "" {
			base = "http://localhost:11434"
		}
		return NewOllamaProvider(base, timeout), base, maxConc, nil
	case "openai", "openai-compatible", "custom":
		if pc.APIKey == "" && len(pc.Headers) == 0 {
			return nil, "", 0, fmt.Errorf("provider %q requires api_key (set in .env and use ${VAR} in config.toml, or configure [llm.providers.%s.headers])", name, name)
		}
		if pc.BaseURL == "" {
			return nil, "", 0, fmt.Errorf("provider %q requires base_url", name)
		}
		return NewOpenAIProviderExt(name, pc.BaseURL, pc.APIKey, timeout, pc.Headers, pc.Extra), pc.BaseURL, maxConc, nil
	default:
		return nil, "", 0, fmt.Errorf("provider %q has unsupported type %q (use openai or ollama)", name, pc.Type)
	}
}

// buildProviders registers all configured providers and constructs the router
func buildProviders(cfg *Config) (*ProviderRouter, string, int, error) {
	r := &ProviderRouter{
		providers: map[string]LLMProvider{},
		defModel:  cfg.LLM.Model,
	}
	var (
		defEndpoint string
		defMaxConc  int
		lastErr     error
	)
	// register configured providers
	defaultName := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))

	required := map[string]bool{defaultName: true}
	for _, role := range []string{"chat", "vision"} {
		if p := cfg.ProviderFor(role); p != "" {
			required[strings.ToLower(p)] = true
		}
	}

	for name := range cfg.LLM.Providers {
		p, endpoint, maxConc, err := buildProviderNamed(cfg, name)
		if err != nil {
			if required[name] {
				lastErr = err
				GetLogger("llm").Warn("provider %q: %v", name, err)
			}
			continue
		}
		r.providers[name] = p
		if name == defaultName {
			defEndpoint = endpoint
			defMaxConc = maxConc
		}
	}

	if def := r.providers[defaultName]; def != nil {
		r.def = def
		r.defName = defaultName
	}
	if r.def == nil {
		reason := "not configured"
		if lastErr != nil {
			reason = lastErr.Error()
		}
		GetLogger("llm").Warn("warning: default llm provider %q is not configured (%s). running in waiting mode.", defaultName, reason)
		fallback := &unconfiguredProvider{name: defaultName, reason: reason}
		r.providers[defaultName] = fallback
		r.def = fallback
		r.defName = defaultName
		defEndpoint = "not configured (waiting for api key)"
		defMaxConc = 2
	}
	return r, defEndpoint, defMaxConc, nil
}
