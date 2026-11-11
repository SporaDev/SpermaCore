package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArtifactStaleness(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "modules", "demo")
	src := filepath.Join(mod, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	header := filepath.Join(root, "include", "plugin_api.h")
	source := filepath.Join(src, "demo.c")
	artifact := filepath.Join(mod, "build", "demo"+artifactExt())
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-time.Hour)
	for _, p := range []string{header, source} {
		if err := os.WriteFile(p, []byte("// x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, base, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(artifact, []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	newer := base.Add(30 * time.Minute)
	if err := os.Chtimes(artifact, newer, newer); err != nil {
		t.Fatal(err)
	}
	if artifactIsStale(artifact, mod, root) {
		t.Fatal("artifact newer than sources must not be stale")
	}

	// touching source makes artifact stale
	touched := base.Add(time.Hour)
	if err := os.Chtimes(source, touched, touched); err != nil {
		t.Fatal(err)
	}
	if !artifactIsStale(artifact, mod, root) {
		t.Fatal("artifact older than touched source must be stale")
	}
}

func TestArtifactStaleWhenHeaderNewer(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "modules", "demo")
	src := filepath.Join(mod, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "include"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(mod, "build", "demo"+artifactExt())
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "demo.c"), []byte("// x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldT := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(artifact, oldT, oldT); err != nil {
		t.Fatal(err)
	}
	header := filepath.Join(root, "include", "plugin_api.h")
	if err := os.WriteFile(header, []byte("// abi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !artifactIsStale(artifact, mod, root) {
		t.Fatal("newer plugin_api.h must make artifact stale")
	}
}
