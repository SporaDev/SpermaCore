package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spermacore/core/humanify"
)

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func readFileStr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// test fsync failure survives without corrupting original file
func TestAtomicWriteFileSurvivesSyncFailure(t *testing.T) {
	path := writeTestConfig(t, "original = true\n")

	prev := configSyncFile
	configSyncFile = func(*os.File) error { return errors.New("injected fsync failure") }
	defer func() { configSyncFile = prev }()

	if _, err := atomicWriteFile(path, []byte("broken = true\n")); err == nil {
		t.Fatal("expected fsync error")
	}
	if got := readFileStr(t, path); got != "original = true\n" {
		t.Fatalf("original file corrupted: %q", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

// test rename failure survives without corrupting original file
func TestAtomicWriteFileSurvivesRenameFailure(t *testing.T) {
	path := writeTestConfig(t, "original = true\n")

	prev := configRenameFile
	configRenameFile = func(string, string) error { return errors.New("injected rename failure") }
	defer func() { configRenameFile = prev }()

	if _, err := atomicWriteFile(path, []byte("broken = true\n")); err == nil {
		t.Fatal("expected rename error")
	}
	if got := readFileStr(t, path); got != "original = true\n" {
		t.Fatalf("original file corrupted: %q", got)
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file remained %q", e.Name())
		}
	}
}

// test unchanged content is not rewritten
func TestAtomicWriteFileSkipsUnchanged(t *testing.T) {
	path := writeTestConfig(t, "same = 1\n")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := atomicWriteFile(path, []byte("same = 1\n"))
	if err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	if changed {
		t.Fatal("file rewritten without changes")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("backup created when there were no changes")
	}
}

// ensure section preserves comments, ordering, existing values, and backups
func TestEnsureSectionPreservesCommentsAndOrder(t *testing.T) {
	original := `# top comment
[core]
# keep me
workers = 6
log_level = "DEBUG"

# humanify block
[humanify]
# custom value stays
delay_min_ms = 111
`
	path := writeTestConfig(t, original)
	cfg := &Config{SourcePath: path}

	created, added, err := cfg.EnsureSection("humanify", map[string]any{
		"delay_min_ms": 300,
		"enabled":      true,
	})
	if err != nil {
		t.Fatalf("EnsureSection: %v", err)
	}
	if created {
		t.Fatal("section already existed")
	}
	if !added {
		t.Fatal("expected enabled key to be added")
	}

	got := readFileStr(t, path)
	if !strings.Contains(got, "# top comment") || !strings.Contains(got, "# keep me") ||
		!strings.Contains(got, "# custom value stays") || !strings.Contains(got, "# humanify block") {
		t.Fatalf("comments lost:\n%s", got)
	}
	if !strings.Contains(got, "delay_min_ms = 111") {
		t.Fatalf("existing value overwritten:\n%s", got)
	}
	if !strings.Contains(got, "enabled = true") {
		t.Fatalf("missing key not added:\n%s", got)
	}
	// verify key ordering is preserved
	if strings.Index(got, "workers") > strings.Index(got, "log_level") {
		t.Fatalf("key order corrupted:\n%s", got)
	}
	// verify key is appended to correct section
	if strings.Index(got, "enabled") < strings.Index(got, "[humanify]") {
		t.Fatalf("key added to wrong section:\n%s", got)
	}
	if bak := readFileStr(t, path+".bak"); bak != original {
		t.Fatalf("backup does not match original:\n%s", bak)
	}
}

// ensure section is idempotent
func TestEnsureSectionIdempotent(t *testing.T) {
	path := writeTestConfig(t, "[humanify]\nenabled = true\n")
	cfg := &Config{SourcePath: path}
	defs := map[string]any{"enabled": true}

	if _, _, err := cfg.EnsureSection("humanify", defs); err != nil {
		t.Fatalf("first call: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	created, added, err := cfg.EnsureSection("humanify", defs)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created || added {
		t.Fatalf("expected no-op, got created=%v added=%v", created, added)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("file rewritten without changes")
	}
}

// test runtime reload of humanify settings
func TestApplyRuntimeConfigHotReloadsHumanify(t *testing.T) {
	cfg := &Config{
		SourcePath: filepath.Join(t.TempDir(), "config.toml"),
		RootSections: map[string]map[string]any{
			"humanify": {
				"enabled":            true,
				"delay_min_ms":       int64(1200),
				"delay_max_ms":       int64(1300),
				"distraction_chance": float64(0),
			},
		},
	}

	warnings := applyRuntimeConfig(nil, cfg, nil)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	h := humanify.Global()
	if h == nil {
		t.Fatal("humanify not initialized")
	}
	for i := 0; i < 20; i++ {
		d := h.DelayBefore("ok")
		if d < 1200*time.Millisecond || d > 1300*time.Millisecond {
			t.Fatalf("delay %v outside expected range 1.2s..1.3s", d)
		}
	}
}

// test detection of fields requiring restart
func TestRestartRequiredReportsFields(t *testing.T) {
	oldCfg := &Config{Core: CoreConfig{Workers: 4}, LLM: LLMConfig{Provider: "a", Model: "m"}}
	newCfg := &Config{Core: CoreConfig{Workers: 8}, LLM: LLMConfig{Provider: "a", Model: "m"}}

	got := restartRequired(oldCfg, newCfg)
	if len(got) != 1 || got[0] != "core.workers" {
		t.Fatalf("expected only core.workers, got %v", got)
	}
	if len(restartRequired(oldCfg, oldCfg)) != 0 {
		t.Fatal("identical configs should not require restart")
	}
}
