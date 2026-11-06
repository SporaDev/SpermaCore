package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// prune stale search dump html files older than 24 hours
const searchDumpMaxAge = 24 * time.Hour

func cleanupStaleSearchDumps(dataDir string, now time.Time) int {
	if dataDir == "" {
		return 0
	}
	dirs := []string{dataDir, filepath.Join(dataDir, "websearch")}
	removed := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, "last_search_") || !strings.HasSuffix(name, ".html") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) <= searchDumpMaxAge {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err == nil {
				removed++
			}
		}
	}
	return removed
}
