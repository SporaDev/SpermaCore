package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// test cleanup of stale search dump files
func TestCleanupStaleSearchDumps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	oldFile := filepath.Join(dir, "last_search_html_post.html")
	newFile := filepath.Join(dir, "last_search_lite_get.html")
	other := filepath.Join(dir, "keep.txt")

	for _, p := range []string{oldFile, newFile, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(oldFile, old, old); err != nil {
		t.Fatal(err)
	}

	if removed := cleanupStaleSearchDumps(dir, now); removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("old dump was not deleted")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatal("fresh dump was deleted")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated file was deleted")
	}
}

// test grouper_time precedence over window_ms
func TestGrouperConfigPrecedence(t *testing.T) {
	c := LoadGrouperConfig(map[string]any{
		"grouper_time": "1.5s",
		"window_ms":    int64(3000),
	})
	if c.Window != 1500*time.Millisecond {
		t.Fatalf("window=%v, want 1.5s", c.Window)
	}
	if c.MaxWait < c.Window {
		t.Fatalf("max_wait=%v < window=%v", c.MaxWait, c.Window)
	}

	// test fallback to window_ms
	c = LoadGrouperConfig(map[string]any{"window_ms": int64(2500)})
	if c.Window != 2500*time.Millisecond {
		t.Fatalf("window=%v, want 2.5s", c.Window)
	}
}
