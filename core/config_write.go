package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// atomic file writer with fsync and rename

// test hooks for error injection
var (
	configSyncFile   = func(f *os.File) error { return f.Sync() }
	configRenameFile = os.Rename
)

// rename retry count for windows file locks
const configRenameRetries = 10

// atomicWriteFile safely writes data to path via temp file and rename
func atomicWriteFile(path string, data []byte) (changed bool, err error) {
	if old, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(old, data) {
		return false, nil
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return false, fmt.Errorf("config: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return false, fmt.Errorf("config: write temp: %w", err)
	}
	if err := configSyncFile(tmp); err != nil {
		return false, fmt.Errorf("config: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("config: close temp: %w", err)
	}

	// backup previous version before replace
	if old, rerr := os.ReadFile(path); rerr == nil {
		if berr := os.WriteFile(path+".bak", old, 0o644); berr != nil {
			GetLogger("core.config").Warn("backup %s.bak: %v", path, berr)
		}
	}

	if err := renameWithRetry(tmpName, path); err != nil {
		return false, fmt.Errorf("config: rename temp: %w", err)
	}
	committed = true
	syncDir(dir)
	return true, nil
}

// renameWithRetry retries rename on brief file lock
func renameWithRetry(from, to string) error {
	delay := 10 * time.Millisecond
	var err error
	for attempt := 0; attempt < configRenameRetries; attempt++ {
		if err = configRenameFile(from, to); err == nil {
			return nil
		}
		time.Sleep(delay)
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
	return err
}

// syncDir flushes directory metadata on posix systems
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	_ = f.Sync()
}
