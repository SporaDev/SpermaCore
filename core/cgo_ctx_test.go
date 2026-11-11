package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// test scope isolation across concurrent calls
func TestPluginScopeIsolation(t *testing.T) {
	prev := gCfg
	gCfg = &Config{Core: CoreConfig{DataDir: t.TempDir()}}
	defer func() { gCfg = prev }()

	plugins := []string{"alpha", "beta"}
	const iterations = 100

	var wg sync.WaitGroup
	errs := make(chan error, iterations)
	for i := 0; i < iterations; i++ {
		name := plugins[i%len(plugins)]
		wantKey := "key-" + name
		wantNum := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := beginPluginScope(name, map[string]any{
				"token": wantKey,
				"num":   wantNum,
			})
			defer release()

			if got := cStringProbe("token"); got != wantKey {
				errs <- fmt.Errorf("plugin %s: token=%q want %q", name, got, wantKey)
				return
			}
			if got := cIntProbe("num", -1); got != wantNum {
				errs <- fmt.Errorf("plugin %s: num=%d want %d", name, got, wantNum)
				return
			}
			wantDir := filepath.Join(gCfg.Core.DataDir, name)
			if got := cDataDirProbe(); got != wantDir {
				errs <- fmt.Errorf("plugin %s: data_dir=%q want %q", name, got, wantDir)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// test nested plugin scopes
func TestPluginScopeNested(t *testing.T) {
	prev := gCfg
	gCfg = &Config{Core: CoreConfig{DataDir: t.TempDir()}}
	defer func() { gCfg = prev }()

	outer := beginPluginScope("outer", map[string]any{"who": "outer"})
	if got := cStringProbe("who"); got != "outer" {
		t.Fatalf("outer scope: got %q", got)
	}
	inner := beginPluginScope("inner", map[string]any{"who": "inner"})
	if got := cStringProbe("who"); got != "inner" {
		t.Fatalf("inner scope: got %q", got)
	}
	inner()
	if got := cStringProbe("who"); got != "outer" {
		t.Fatalf("restored outer scope: got %q", got)
	}
	outer()
	if got := cStringProbe("who"); got != "" {
		t.Fatalf("no scope: got %q", got)
	}
}
