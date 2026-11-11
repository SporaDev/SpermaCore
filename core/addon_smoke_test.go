package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSmokeLoadModules verifies dynamic loading of compiled addons without network.
func TestSmokeLoadModules(t *testing.T) {
	modulesRoot := filepath.Join("..", "modules")
	entries, err := os.ReadDir(modulesRoot)
	if err != nil {
		t.Skipf("modules directory unavailable: %v", err)
	}

	loaded := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(modulesRoot, name)
		artifact := findBuiltArtifact(dir, name)
		if artifact == "" {
			continue
		}
		if err := checkPluginLoad(artifact); err != nil {
			t.Errorf("module %s: %v", name, err)
			continue
		}
		loaded++
		t.Logf("module %s: loaded %s", name, artifact)
	}
	if loaded == 0 {
		t.Skip("no built module artifacts (run cmake build first)")
	}
}
