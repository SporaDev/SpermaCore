package main

import (
	"os"
	"strings"
)

// default fallback endpoint
const defaultEmbedBase = "http://localhost:11434"

// embeddingSettings holds resolved embedsc parameters
type embeddingSettings struct {
	Provider string
	Base     string
	Model    string
	Key      string
	// disabled if required key is missing
	Disabled bool
}

// resolveEmbeddingSettings deterministically selects provider, endpoint, and key
func resolveEmbeddingSettings(cfg *Config) embeddingSettings {
	s := embeddingSettings{}
	if cfg == nil || !cfg.Memory.IsEnabled() {
		s.Disabled = true
		return s
	}

	s.Model = cfg.Memory.EmbeddingModel
	if sec, ok := cfg.PluginSections["embedsc"]; ok {
		if m, ok := sec["model"].(string); ok && m != "" {
			s.Model = m
		}
	}
	if cfg.HasRoute("embedding") {
		if cfg.LLM.Models["embedding"] != "" || cfg.LLM.Routing["embedding"].Model != "" {
			if m := cfg.ModelNameFor("embedding"); m != "" {
				s.Model = m
			}
		}
	}

	provider := cfg.ProviderFor("embedding")
	p, ok := cfg.LLM.Providers[provider]
	if ok {
		s.Base = p.BaseURL
		s.Key = strings.TrimSpace(p.APIKey)
	}

	// fallback for unrouted embedding role
	if s.Key == "" && !cfg.HasRoute("embedding") {
		for _, name := range []string{"openrouter", "openai"} {
			q, ok := cfg.LLM.Providers[name]
			if !ok || strings.TrimSpace(q.APIKey) == "" {
				continue
			}
			if s.Base != "" && q.BaseURL != "" && s.Base != q.BaseURL {
				continue
			}
			provider = name
			s.Base = q.BaseURL
			s.Key = strings.TrimSpace(q.APIKey)
			break
		}
	}
	s.Provider = provider

	// environment variable fallback for provider
	if s.Key == "" {
		switch provider {
		case "openrouter":
			s.Key = strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
		case "openai":
			s.Key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		}
	}

	if s.Base == "" {
		if q, ok := cfg.LLM.Providers["ollama"]; ok && q.BaseURL != "" {
			s.Base = q.BaseURL
		} else {
			s.Base = defaultEmbedBase
		}
	}

	if s.Key == "" {
		s.Disabled = true
	}
	return s
}
