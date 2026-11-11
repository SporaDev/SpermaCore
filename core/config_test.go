package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SC_TEST_ENV_NEW", "")
	os.Unsetenv("SC_TEST_ENV_NEW") //nolint:errcheck
	t.Setenv("SC_TEST_ENV_EXISTING", "keep")

	path := filepath.Join(dir, ".env")
	body := strings.Join([]string{
		"# comment",
		"",
		"SC_TEST_ENV_NEW=\"value with spaces\"",
		"export SC_TEST_ENV_EXPORTED=plain",
		"SC_TEST_ENV_EXISTING=changed",
		"NO_EQUALS_LINE",
		"=no-key",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(path); err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}
	if got := os.Getenv("SC_TEST_ENV_NEW"); got != "value with spaces" {
		t.Fatalf("new var = %q", got)
	}
	if got := os.Getenv("SC_TEST_ENV_EXPORTED"); got != "plain" {
		t.Fatalf("exported var = %q", got)
	}
	if got := os.Getenv("SC_TEST_ENV_EXISTING"); got != "keep" {
		t.Fatalf("existing var overwritten: %q", got)
	}

	if err := loadDotEnv(filepath.Join(dir, "missing.env")); err != nil {
		t.Fatalf("missing .env must be ignored: %v", err)
	}
	// directory instead of file should fail
	if err := loadDotEnv(dir); err == nil {
		t.Fatal("directory as .env must fail")
	}
}

func TestLoadConfigRaw(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SC_TEST_MODEL", "llama-test")
	path := filepath.Join(dir, "config.toml")
	body := `
# comment preserved by EnsureSection, not by parser
[core]
data_dir = "./runtime"
log_level = "debug"

[llm]
provider = "Ollama"
model = "${SC_TEST_MODEL}"

[llm.providers.ollama]
type = "ollama"

[llm.routing.vision]
provider = "ollama"
model = "llava"

[plugins]
load = ["time"]

[plugins.time]
enabled = true

[humanify]
enabled = false
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfigRaw(path)
	if err != nil {
		t.Fatalf("LoadConfigRaw: %v", err)
	}
	if !filepath.IsAbs(cfg.SourcePath) {
		t.Fatalf("SourcePath is not absolute: %q", cfg.SourcePath)
	}
	wantData := filepath.Join(dir, "runtime")
	if cfg.Core.DataDir != wantData {
		t.Fatalf("DataDir = %q want %q", cfg.Core.DataDir, wantData)
	}
	if cfg.LLM.Model != "llama-test" {
		t.Fatalf("env expansion failed: %q", cfg.LLM.Model)
	}
	if _, ok := cfg.LLM.Providers["ollama"]; !ok {
		t.Fatalf("provider keys not normalized: %#v", cfg.LLM.Providers)
	}
	if cfg.PluginSections["time"]["enabled"] != true {
		t.Fatalf("plugin section = %#v", cfg.PluginSections["time"])
	}
	if cfg.RootSections["humanify"]["enabled"] != false {
		t.Fatalf("root section = %#v", cfg.RootSections["humanify"])
	}
	if _, ok := cfg.RootSections["core"]; ok {
		t.Fatal("reserved [core] must not be in RootSections")
	}
	if cfg.Core.MaxToolIterations != 10 || cfg.Core.Workers <= 0 {
		t.Fatalf("defaults not applied: %+v", cfg.Core)
	}
	if cfg.Core.Sandbox.Mode != "process" {
		t.Fatalf("sandbox mode default = %q", cfg.Core.Sandbox.Mode)
	}
	if cfg.LogsDir() != filepath.Join(wantData, "logs") {
		t.Fatalf("LogsDir = %q", cfg.LogsDir())
	}
	if cfg.ModelFor("vision") != "ollama/llava" {
		t.Fatalf("ModelFor(vision) = %q", cfg.ModelFor("vision"))
	}
}

func TestLoadConfigRawErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, err := LoadConfigRaw(filepath.Join(dir, "absent.toml")); err == nil {
		t.Fatal("missing file must fail")
	}
	if _, err := LoadConfigRaw(write("bad-syntax.toml", "[core\n")); err == nil {
		t.Fatal("invalid TOML must fail")
	}
	if _, err := LoadConfigRaw(write("bad-log.toml", "[core]\nlog_level=\"nope\"\n[llm]\nprovider=\"p\"\nmodel=\"m\"\n[llm.providers.p]\n")); err == nil {
		t.Fatal("invalid log level must fail")
	}
	if _, err := LoadConfigRaw(write("no-provider.toml", "[llm]\nmodel=\"m\"\n")); err == nil {
		t.Fatal("missing provider must fail")
	}
	if _, err := LoadConfigRaw(write("no-model.toml", "[llm]\nprovider=\"p\"\n[llm.providers.p]\n")); err == nil {
		t.Fatal("missing model must fail")
	}
	if _, err := LoadConfigRaw(write("unknown-route.toml", "[llm]\nprovider=\"p\"\nmodel=\"m\"\n[llm.providers.p]\n[llm.routing.vision]\nprovider=\"q\"\n")); err == nil {
		t.Fatal("routing to unknown provider must fail")
	}
	if _, err := LoadConfigRaw(write("undefined-provider.toml", "[llm]\nprovider=\"q\"\nmodel=\"m\"\n")); err == nil {
		t.Fatal("undefined default provider must fail")
	}
	if _, err := LoadConfigRaw(write("bad-type.toml", "[llm]\nprovider=\"p\"\nmodel=\"m\"\n[llm.providers.p]\ntype=\"magic\"\n")); err == nil {
		t.Fatal("unsupported provider type must fail")
	}
	if _, err := LoadConfigRaw(write("bad-sandbox.toml", "[core.sandbox]\nenabled=true\nmode=\"chroot\"\n[llm]\nprovider=\"p\"\nmodel=\"m\"\n[llm.providers.p]\n")); err == nil {
		t.Fatal("unsupported sandbox mode must fail")
	}
	if _, err := LoadConfigRaw(write("ok-sandbox.toml", "[core.sandbox]\nenabled=true\nmode=\"process\"\n[llm]\nprovider=\"p\"\nmodel=\"m\"\n[llm.providers.p]\n")); err != nil {
		t.Fatalf("process sandbox must be accepted: %v", err)
	}
}

func TestExtractSections(t *testing.T) {
	if got := extractPluginSections(nil); len(got) != 0 {
		t.Fatalf("nil root = %#v", got)
	}
	root := map[string]any{
		"plugins": map[string]any{
			"load": []any{"a"},
			"a":    map[string]any{"enabled": true},
			"bad":  "not-a-table",
		},
		"humanify": map[string]any{"enabled": true},
		"scalar":   42,
	}
	plugins := extractPluginSections(root)
	if len(plugins) != 1 || plugins["a"]["enabled"] != true {
		t.Fatalf("plugins = %#v", plugins)
	}
	if _, ok := plugins["load"]; ok {
		t.Fatal("plugins.load must be excluded")
	}
	roots := extractRootSections(root)
	if _, ok := roots["humanify"]; !ok {
		t.Fatalf("roots = %#v", roots)
	}
	if _, ok := roots["plugins"]; ok {
		t.Fatal("[plugins] must be reserved")
	}
	if _, ok := roots["scalar"]; ok {
		t.Fatal("scalar must be skipped")
	}
}

func TestNormalizePathsAbsolute(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "data")
	c := &Config{SourcePath: filepath.Join(t.TempDir(), "config.toml")}
	c.Core.DataDir = abs
	c.normalizePaths()
	if c.Core.DataDir != abs {
		t.Fatalf("absolute path changed: %q", c.Core.DataDir)
	}
}

func TestRouteHelpers(t *testing.T) {
	c := &Config{LLM: LLMConfig{
		Provider: "Default",
		Model:    "base-model",
		Models:   map[string]string{"chat": "models-chat", "fast": "fast-model"},
		Routing: map[string]ModelRoute{
			"vision": {Provider: "openrouter", Model: "google/gemini"},
			"embed":  {Provider: "openrouter"},
			"local":  {Model: "only-model"},
			"empty":  {},
		},
		Providers: map[string]ProviderConfig{"openrouter": {}, "default": {}},
	}}

	cases := []struct {
		role, model, provider, name string
		has                         bool
	}{
		{"vision", "openrouter/google/gemini", "openrouter", "google/gemini", true},
		{"embed", "openrouter/base-model", "openrouter", "base-model", true},
		{"local", "only-model", "default", "only-model", true},
		{"empty", "base-model", "default", "base-model", false},
		{"fast", "fast-model", "default", "fast-model", true},
		{"unknown", "models-chat", "default", "models-chat", false},
		{"CHAT", "models-chat", "default", "models-chat", true},
	}
	for _, tc := range cases {
		if got := c.ModelFor(tc.role); got != tc.model {
			t.Errorf("ModelFor(%q) = %q want %q", tc.role, got, tc.model)
		}
		if got := c.ProviderFor(tc.role); got != tc.provider {
			t.Errorf("ProviderFor(%q) = %q want %q", tc.role, got, tc.provider)
		}
		if got := c.ModelNameFor(tc.role); got != tc.name {
			t.Errorf("ModelNameFor(%q) = %q want %q", tc.role, got, tc.name)
		}
		if got := c.HasRoute(tc.role); got != tc.has {
			t.Errorf("HasRoute(%q) = %v want %v", tc.role, got, tc.has)
		}
	}

	// fallback to [llm].model when routing is absent
	fallback := &Config{LLM: LLMConfig{Provider: "p", Model: "fallback"}}
	if got := fallback.ModelFor("chat"); got != "fallback" {
		t.Fatalf("fallback ModelFor = %q", got)
	}
	if got := fallback.ProviderFor("chat"); got != "p" {
		t.Fatalf("fallback ProviderFor = %q", got)
	}
}

func TestSplitModelRoute(t *testing.T) {
	c := &Config{LLM: LLMConfig{Providers: map[string]ProviderConfig{"openrouter": {}, "local": {}}}}
	cases := []struct{ raw, provider, model string }{
		{"openrouter/google/gemini", "openrouter", "google/gemini"},
		{"OPENROUTER//double", "openrouter", "double"},
		{"ollama/llava", "", "llava"},
		{"unknown/prefix", "", "unknown/prefix"},
		{"plain-model", "", "plain-model"},
		{"/leading", "", "/leading"},
		{"  spaced/model  ", "", "spaced/model"},
	}
	for _, tc := range cases {
		p, m := c.splitModelRoute(tc.raw)
		if p != tc.provider || m != tc.model {
			t.Errorf("splitModelRoute(%q) = (%q,%q) want (%q,%q)", tc.raw, p, m, tc.provider, tc.model)
		}
	}
}

func TestPluginHelpers(t *testing.T) {
	c := &Config{PluginSections: map[string]map[string]any{
		"missing-enabled": {"x": 1},
		"bad-enabled":     {"enabled": "yes"},
		"off":             {"enabled": false},
		"on":              {"enabled": true},
	}}

	if got := c.pluginConfigJSON("absent"); got != "{}" {
		t.Fatalf("absent json = %q", got)
	}
	if got := c.pluginConfigJSON("on"); got != `{"enabled":true}` {
		t.Fatalf("on json = %q", got)
	}
	c.PluginSections["bad-json"] = map[string]any{"fn": func() {}}
	if got := c.pluginConfigJSON("bad-json"); got != "{}" {
		t.Fatalf("bad json = %q", got)
	}

	for name, want := range map[string]bool{
		"unknown": true, "missing-enabled": true, "bad-enabled": true, "off": false, "on": true,
	} {
		if got := c.isPluginEnabled(name); got != want {
			t.Errorf("isPluginEnabled(%q) = %v want %v", name, got, want)
		}
	}
}

func TestFormatTOMLValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{true, "true"},
		{false, "false"},
		{7, "7"},
		{int64(9000000000), "9000000000"},
		{1.5, "1.5"},
		{`a"b\c`, `"a\"b\\c"`},
		{[]string{"a", `b"c`}, `["a", "b\"c"]`},
		{struct{ X int }{1}, `"{1}"`},
	}
	for _, tc := range cases {
		if got := formatTOMLValue(tc.in); got != tc.want {
			t.Errorf("formatTOMLValue(%#v) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSectionHelpers(t *testing.T) {
	for _, s := range []string{"[core]", "[a.b.c]", "[ ]"} {
		if !isSectionHeader(s) {
			t.Errorf("%q must be a section header", s)
		}
	}
	for _, s := range []string{"", "[", "[[array]]", "core]", "x"} {
		if isSectionHeader(s) {
			t.Errorf("%q must not be a section header", s)
		}
	}
	if got := sectionName("[ a.b ]"); got != "a.b" {
		t.Fatalf("sectionName = %q", got)
	}
	keys := sortedKeys(map[string]any{"b": 1, "a": 2})
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("sortedKeys = %#v", keys)
	}
}

func TestEnsureSectionCreatesMissing(t *testing.T) {
	path := writeTestConfig(t, "top = 1")
	cfg := &Config{SourcePath: path}

	created, added, err := cfg.EnsureSection("webui", map[string]any{
		"enabled":    true,
		"port":       8080,
		"auth_token": `tok"en`,
		"bind":       "127.0.0.1",
		"hosts":      []string{"a", "b"},
		"ratio":      0.5,
	})
	if err != nil {
		t.Fatalf("EnsureSection: %v", err)
	}
	if !created || added {
		t.Fatalf("created=%v added=%v", created, added)
	}
	got := readFileStr(t, path)
	for _, want := range []string{
		"[webui]", "enabled = true", "port = 8080",
		`auth_token = "tok\"en"`, `bind = "127.0.0.1"`,
		`hosts = ["a", "b"]`, "ratio = 0.5", "auto-added by webui",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if created, added, err := cfg.EnsureSection("webui", map[string]any{"port": 1}); err != nil || created || added {
		t.Fatalf("second call: created=%v added=%v err=%v", created, added, err)
	}

	if _, _, err := (&Config{SourcePath: filepath.Join(t.TempDir(), "nope.toml")}).EnsureSection("x", nil); err == nil {
		t.Fatal("missing config file must fail")
	}
}

func TestEnsureSectionInsertsBeforeNextSection(t *testing.T) {
	original := "[a]\nx = 1\n\n[b]\ny = 2\n"
	path := writeTestConfig(t, original)
	cfg := &Config{SourcePath: path}

	created, added, err := cfg.EnsureSection("a", map[string]any{"z": "last"})
	if err != nil || created || !added {
		t.Fatalf("created=%v added=%v err=%v", created, added, err)
	}
	got := readFileStr(t, path)
	ai := strings.Index(got, "[a]")
	zi := strings.Index(got, `z = "last"`)
	bi := strings.Index(got, "[b]")
	if !(ai < zi && zi < bi) {
		t.Fatalf("key inserted outside [a]:\n%s", got)
	}
}

func TestFormatTOMLValueEscaping(t *testing.T) {
	cases := []struct {
		val  any
		want string
	}{
		{"hello\nworld", `"hello\nworld"`},
		{"tab\tseparated\r\n", `"tab\tseparated\r\n"`},
		{`quote "and" backslash \`, `"quote \"and\" backslash \\"`},
		{[]string{"line1\nline2", "quote\""}, `["line1\nline2", "quote\""]`},
	}
	for _, tc := range cases {
		got := formatTOMLValue(tc.val)
		if got != tc.want {
			t.Errorf("formatTOMLValue(%v) = %q, want %q", tc.val, got, tc.want)
		}
	}
}
