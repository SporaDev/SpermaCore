package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// config holds root configuration structure
type Config struct {
	Core    CoreConfig    `toml:"core"`
	LLM     LLMConfig     `toml:"llm"`
	Memory  MemoryConfig  `toml:"memory"`
	Plugins PluginsConfig `toml:"plugins"`

	// custom plugin sections passed via config_get_*
	PluginSections map[string]map[string]any `toml:"-"`

	// additional top-level sections (e.g. humanify, grouper)
	RootSections map[string]map[string]any `toml:"-"`

	// absolute path to config.toml
	SourcePath string `toml:"-"`
}

type CoreConfig struct {
	DataDir           string        `toml:"data_dir"`
	LogLevel          string        `toml:"log_level"`
	MaxToolIterations int           `toml:"max_tool_iterations"`
	Workers           int           `toml:"workers"`
	ErrorDelivery     string        `toml:"error_delivery"`
	DBKey             string        `toml:"db_key"`
	Sandbox           SandboxConfig `toml:"sandbox"`
}

// SandboxConfig controls isolation of native modules.
// Native C plugins cannot be safely sandboxed inside the Go process: a DLL
// shares its address space with the host. Strict mode therefore fails closed
// until the out-of-process ABI bridge is enabled, rather than pretending that
// an in-process permission flag is a security boundary.
type SandboxConfig struct {
	Enabled       bool   `toml:"enabled"`
	Mode          string `toml:"mode"`
	AllowUnsigned bool   `toml:"allow_unsigned"`
}

type LLMConfig struct {
	Provider  string                    `toml:"provider"`
	Model     string                    `toml:"model"`
	Models    map[string]string         `toml:"models"`
	Routing   map[string]ModelRoute     `toml:"routing"`
	Providers map[string]ProviderConfig `toml:"providers"`
}

// modelRoute maps a role to a provider and model
type ModelRoute struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
}

type ProviderConfig struct {
	// provider type ("openai" or "ollama")
	Type           string `toml:"type"`
	BaseURL        string `toml:"base_url"`
	APIKey         string `toml:"api_key"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
	MaxConcurrent  int    `toml:"max_concurrent"`
	// custom http headers
	Headers map[string]string `toml:"headers"`
	// extra fields passed to request body
	Extra map[string]any `toml:"extra"`
}

type MemoryConfig struct {
	Enabled             *bool   `toml:"enabled"`
	EmbeddingModel      string  `toml:"embedding_model"`
	MaxRAGResults       int     `toml:"max_rag_results"`
	SimilarityThreshold float64 `toml:"similarity_threshold"`
	IndexType           string  `toml:"index_type"`
	HNSWM               int     `toml:"hnsw_m"`
	HNSWEfConstruction  int     `toml:"hnsw_ef_construction"`
	HNSWEfSearch        int     `toml:"hnsw_ef_search"`
}

// isEnabled reports whether semantic memory is enabled
func (m MemoryConfig) IsEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

type PluginsConfig struct {
	Load []string `toml:"load"`
	// allow stale binary if rebuild fails
	AllowStale bool `toml:"allow_stale"`
}

// loadConfigRaw reads and validates config.toml.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\\\"")
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func LoadConfigRaw(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve path %q: %w", path, err)
	}
	if err := loadDotEnv(filepath.Join(filepath.Dir(abs), ".env")); err != nil {
		return nil, fmt.Errorf("config: load .env: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", abs, err)
	}
	raw = []byte(os.ExpandEnv(string(raw)))

	var cfg Config
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", abs, err)
	}
	cfg.SourcePath = abs

	var root map[string]any
	if err := toml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("config: parse (tree) %q: %w", abs, err)
	}
	cfg.PluginSections = extractPluginSections(root)
	cfg.RootSections = extractRootSections(root)

	cfg.applyDefaults()
	if strings.TrimSpace(cfg.Core.Sandbox.Mode) == "" {
		cfg.Core.Sandbox.Mode = "process"
	}
	if cfg.Core.Sandbox.Enabled && cfg.Core.Sandbox.Mode != "process" {
		return nil, fmt.Errorf("config: unsupported sandbox.mode %q (use process)", cfg.Core.Sandbox.Mode)
	}
	cfg.normalizePaths()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// extractPluginSections extracts [plugins.<name>] sections
func extractPluginSections(root map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any)
	plugins, ok := root["plugins"].(map[string]any)
	if !ok {
		return out
	}
	for name, v := range plugins {
		if name == "load" {
			continue
		}
		sub, ok := v.(map[string]any)
		if !ok {
			continue
		}
		out[name] = sub
	}
	return out
}

// extractRootSections extracts top-level sections outside reserved keys
func extractRootSections(root map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any)
	reserved := map[string]bool{
		"core": true, "llm": true, "memory": true, "plugins": true,
	}
	for name, v := range root {
		if reserved[name] {
			continue
		}
		sub, ok := v.(map[string]any)
		if !ok {
			continue
		}
		out[name] = sub
	}
	return out
}

func (c *Config) applyDefaults() {
	if c.Core.DataDir == "" {
		c.Core.DataDir = "./data"
	}
	if c.Core.LogLevel == "" {
		c.Core.LogLevel = "INFO"
	}
	if c.Core.MaxToolIterations <= 0 {
		c.Core.MaxToolIterations = 10
	}
	if c.Core.Workers <= 0 {
		c.Core.Workers = defaultWorkerCount
	}
	if c.Core.ErrorDelivery != "webui" && c.Core.ErrorDelivery != "user" {
		c.Core.ErrorDelivery = "webui"
	}

	if c.Memory.EmbeddingModel == "" {
		c.Memory.EmbeddingModel = "qwen3-embedding:latest"
	}
	if c.Memory.MaxRAGResults <= 0 {
		c.Memory.MaxRAGResults = 5
	}
	if c.Memory.SimilarityThreshold <= 0 {
		c.Memory.SimilarityThreshold = 0.72
	}
	if c.Memory.IndexType == "" {
		c.Memory.IndexType = "hnsw"
	}
	if c.Memory.HNSWM <= 0 {
		c.Memory.HNSWM = 16
	}
	if c.Memory.HNSWEfConstruction <= 0 {
		c.Memory.HNSWEfConstruction = 64
	}
	if c.Memory.HNSWEfSearch <= 0 {
		c.Memory.HNSWEfSearch = 32
	}
	if c.LLM.Models == nil {
		c.LLM.Models = map[string]string{}
	}
	if c.LLM.Routing == nil {
		c.LLM.Routing = map[string]ModelRoute{}
	}
	// normalize provider and role names to lowercase
	if len(c.LLM.Providers) > 0 {
		norm := make(map[string]ProviderConfig, len(c.LLM.Providers))
		for name, pc := range c.LLM.Providers {
			norm[strings.ToLower(strings.TrimSpace(name))] = pc
		}
		c.LLM.Providers = norm
	}
	if len(c.LLM.Routing) > 0 {
		norm := make(map[string]ModelRoute, len(c.LLM.Routing))
		for role, r := range c.LLM.Routing {
			r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
			norm[strings.ToLower(strings.TrimSpace(role))] = r
		}
		c.LLM.Routing = norm
	}
	if c.PluginSections == nil {
		c.PluginSections = map[string]map[string]any{}
	}
	if c.RootSections == nil {
		c.RootSections = map[string]map[string]any{}
	}
}

func (c *Config) normalizePaths() {
	base := filepath.Dir(c.SourcePath)
	if !filepath.IsAbs(c.Core.DataDir) {
		c.Core.DataDir = filepath.Clean(filepath.Join(base, c.Core.DataDir))
	}
}

func (c *Config) validate() error {
	switch strings.ToUpper(c.Core.LogLevel) {
	case "DEBUG", "INFO", "WARN", "ERROR", "CRITICAL", "TOOL":
	default:
		return fmt.Errorf("config: invalid log_level %q", c.Core.LogLevel)
	}
	if c.LLM.Provider == "" {
		return fmt.Errorf("config: [llm] provider is required")
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("config: [llm] model is required")
	}
	for role, r := range c.LLM.Routing {
		if r.Provider == "" {
			continue
		}
		if _, ok := c.LLM.Providers[r.Provider]; !ok {
			return fmt.Errorf("config: [llm.routing.%s] references unknown provider %q", role, r.Provider)
		}
	}
	if _, ok := c.LLM.Providers[strings.ToLower(c.LLM.Provider)]; !ok {
		return fmt.Errorf("config: [llm] provider %q is not defined in [llm.providers.%s]", c.LLM.Provider, c.LLM.Provider)
	}
	for name, pc := range c.LLM.Providers {
		typ := strings.ToLower(strings.TrimSpace(pc.Type))
		switch typ {
		case "", "openai", "openai-compatible", "custom", "ollama":
		default:
			return fmt.Errorf("config: [llm.providers.%s] has unsupported type %q (use openai or ollama)", name, pc.Type)
		}
	}
	return nil
}

// reports whether role has explicit routing
func (c *Config) HasRoute(role string) bool {
	role = strings.ToLower(role)
	if r, ok := c.LLM.Routing[role]; ok && (r.Provider != "" || r.Model != "") {
		return true
	}
	return c.LLM.Models[role] != ""
}

// splitModelRoute extracts provider prefix and model name
func (c *Config) splitModelRoute(raw string) (provider, model string) {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, '/'); i > 0 {
		name := strings.ToLower(raw[:i])
		if _, ok := c.LLM.Providers[name]; ok {
			return name, strings.TrimLeft(raw[i+1:], "/")
		}
		// legacy compatibility for ollama/ prefix
		if name == "ollama" {
			return "", strings.TrimLeft(raw[i+1:], "/")
		}
	}
	return "", raw
}

// returns model routing string for given role
func (c *Config) ModelFor(role string) string {
	role = strings.ToLower(role)
	if r, ok := c.LLM.Routing[role]; ok {
		m := r.Model
		if m == "" {
			m = c.LLM.Model
		}
		if r.Provider != "" {
			return r.Provider + "/" + m
		}
		if m != "" {
			return m
		}
	}
	if m := c.LLM.Models[role]; m != "" {
		return m
	}
	if role != "chat" {
		return c.ModelFor("chat")
	}
	return c.LLM.Model
}

// returns provider name for role
func (c *Config) ProviderFor(role string) string {
	role = strings.ToLower(role)
	if r, ok := c.LLM.Routing[role]; ok && r.Provider != "" {
		return r.Provider
	}
	if p, _ := c.splitModelRoute(c.ModelFor(role)); p != "" {
		return p
	}
	return strings.ToLower(c.LLM.Provider)
}

// returns raw model name without provider prefix
func (c *Config) ModelNameFor(role string) string {
	_, m := c.splitModelRoute(c.ModelFor(role))
	return m
}

func (c *Config) LogsDir() string {
	return filepath.Join(c.Core.DataDir, "logs")
}

func (c *Config) pluginConfigJSON(name string) string {
	section, ok := c.PluginSections[name]
	if !ok || len(section) == 0 {
		return "{}"
	}
	b, err := json.Marshal(section)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (c *Config) isPluginEnabled(name string) bool {
	section, ok := c.PluginSections[name]
	if !ok {
		return true
	}
	v, ok := section["enabled"]
	if !ok {
		return true
	}
	b, ok := v.(bool)
	if !ok {
		return true
	}
	return b
}

// EnsureSection writes default section or missing keys to config.toml without overwriting existing entries
func (c *Config) EnsureSection(name string, defaults map[string]any) (created bool, added bool, err error) {
	raw, err := os.ReadFile(c.SourcePath)
	if err != nil {
		return false, false, err
	}
	text := string(raw)
	lines := strings.Split(text, "\n")

	// look for top-level [name] section
	sectionStart := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isSectionHeader(trimmed) {
			secName := sectionName(trimmed)
			if secName == name {
				sectionStart = i
				break
			}
		}
	}

	// sort keys deterministically
	keys := sortedKeys(defaults)

	// section missing: append full section
	if sectionStart < 0 {
		var b strings.Builder
		if len(text) > 0 && !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n# ==== auto-added by " + name + " ====\n")
		b.WriteString("[" + name + "]\n")
		for _, k := range keys {
			b.WriteString(k + " = " + formatTOMLValue(defaults[k]) + "\n")
		}

		changed, err := atomicWriteFile(c.SourcePath, []byte(text+b.String()))
		if err != nil {
			return false, false, err
		}
		if changed {
			bumpConfigVersion()
		}
		return true, false, nil
	}

	// section exists: find missing keys
	sectionEnd := len(lines)
	for i := sectionStart + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if isSectionHeader(trimmed) {
			sectionEnd = i
			break
		}
	}

	existing := map[string]bool{}
	for i := sectionStart + 1; i < sectionEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			existing[key] = true
		}
	}

	// check keys to add
	var toAdd []string
	for _, k := range keys {
		if existing[k] {
			continue
		}
		toAdd = append(toAdd, k+" = "+formatTOMLValue(defaults[k]))
	}
	if len(toAdd) == 0 {
		return false, false, nil
	}

	// insert missing keys at end of section
	insertAt := sectionEnd
	for i := sectionEnd - 1; i > sectionStart; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			insertAt = i + 1
			break
		}
	}

	newLines := make([]string, 0, len(lines)+len(toAdd))
	newLines = append(newLines, lines[:insertAt]...)
	newLines = append(newLines, toAdd...)
	newLines = append(newLines, lines[insertAt:]...)

	out := strings.Join(newLines, "\n")
	changed, err := atomicWriteFile(c.SourcePath, []byte(out))
	if err != nil {
		return false, false, err
	}
	if !changed {
		// content unchanged
		return false, false, nil
	}
	bumpConfigVersion()
	return false, true, nil
}

// isSectionHeader checks for [section] pattern
func isSectionHeader(s string) bool {
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return false
	}
	// ignore array of tables [[...]]
	return s[1] != '['
}

// sectionName returns trimmed section name
func sectionName(s string) string {
	return strings.TrimSpace(s[1 : len(s)-1])
}

// sortedKeys returns keys in sorted order
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// formatTOMLValue formats go value as toml literal
func formatTOMLValue(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return quoteTOMLString(x)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = quoteTOMLString(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		// fallback string representation
		return fmt.Sprintf("%q", fmt.Sprint(v))
	}
}

func quoteTOMLString(s string) string {
	esc := strings.ReplaceAll(s, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	esc = strings.ReplaceAll(esc, "\n", `\n`)
	esc = strings.ReplaceAll(esc, "\r", `\r`)
	esc = strings.ReplaceAll(esc, "\t", `\t`)
	return `"` + esc + `"`
}
