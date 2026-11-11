package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTDLibBinaryName(t *testing.T) {
	if got := TDLibBinaryName("windows"); got != "tdjson.dll" {
		t.Errorf("windows: want tdjson.dll, got %s", got)
	}
	if got := TDLibBinaryName("darwin"); got != "libtdjson.dylib" {
		t.Errorf("darwin: want libtdjson.dylib, got %s", got)
	}
	if got := TDLibBinaryName("linux"); got != "libtdjson.so" {
		t.Errorf("linux: want libtdjson.so, got %s", got)
	}
}

func TestTDLibDownloadURL(t *testing.T) {
	uLinux := TDLibDownloadURL("linux", "amd64")
	if uLinux == "" {
		t.Errorf("expected non-empty linux/amd64 download URL")
	}
	uArm := TDLibDownloadURL("linux", "arm64")
	if uArm == "" {
		t.Errorf("expected non-empty linux/arm64 download URL")
	}
	uMac := TDLibDownloadURL("darwin", "arm64")
	if uMac == "" {
		t.Errorf("expected non-empty darwin/arm64 download URL")
	}

	// Test custom override
	t.Setenv("SPERMACORE_TDJSON_URL", "https://example.com/custom.so")
	if got := TDLibDownloadURL("linux", "amd64"); got != "https://example.com/custom.so" {
		t.Errorf("expected custom URL override, got %s", got)
	}
}

func TestEnsureTDLibBinary_AlreadyPresent(t *testing.T) {
	tmpDir := t.TempDir()
	tgDir := filepath.Join(tmpDir, "telegram")
	if err := os.MkdirAll(tgDir, 0755); err != nil {
		t.Fatal(err)
	}

	binName := TDLibBinaryName("windows")
	target := filepath.Join(tgDir, binName)
	// Create mock dummy binary > 1MB
	dummyData := make([]byte, 1024*1024+10)
	if err := os.WriteFile(target, dummyData, 0755); err != nil {
		t.Fatal(err)
	}

	// Should return nil immediately without attempting network calls
	if err := EnsureTDLibBinary(tmpDir); err != nil {
		t.Errorf("expected nil when binary is present, got %v", err)
	}
}

func TestEnsureTDLibBinary_DownloadDirect(t *testing.T) {
	// Create mock HTTP server that serves a 1.2MB dummy file
	dummyData := bytes.Repeat([]byte("A"), 1024*1024+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(dummyData)
	}))
	defer srv.Close()

	t.Setenv("SPERMACORE_TDJSON_URL", srv.URL+"/mock-lib.so")

	tmpDir := t.TempDir()
	if err := EnsureTDLibBinary(tmpDir); err != nil {
		t.Fatalf("EnsureTDLibBinary failed: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, "telegram", TDLibBinaryName(runtime.GOOS))
	fi, err := os.Stat(expectedPath)
	if err != nil || fi.Size() != int64(len(dummyData)) {
		t.Errorf("binary not found or size mismatch at %s: %v", expectedPath, err)
	}
}

func TestEnsureTDLibBinary_DownloadZip(t *testing.T) {
	// Prepare a zip containing the current platform's TDLib binary
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	binName := TDLibBinaryName("windows")
	zf, err := zw.Create(binName)
	if err != nil {
		t.Fatal(err)
	}
	dummyData := bytes.Repeat([]byte("Z"), 1024*1024+200)
	if _, err := zf.Write(dummyData); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBuf.Bytes())
	}))
	defer srv.Close()

	t.Setenv("SPERMACORE_TDJSON_URL", srv.URL+"/package.zip")

	tmpDir := t.TempDir()
	if err := EnsureTDLibBinary(tmpDir); err != nil {
		t.Fatalf("EnsureTDLibBinary with zip failed: %v", err)
	}

	target := filepath.Join(tmpDir, "telegram", binName)
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("extracted binary not found: %v", err)
	}
	if fi.Size() != int64(len(dummyData)) {
		t.Errorf("extracted size mismatch: want %d, got %d", len(dummyData), fi.Size())
	}
}
