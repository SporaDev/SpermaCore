package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// TDLibPrebuiltURLs maps OS/Arch combinations to precompiled standalone TDLib binaries.
// Linux and macOS binaries are self-contained and statically linked with OpenSSL/zlib.
var TDLibPrebuiltURLs = map[string]string{
	"linux/amd64":   "https://github.com/alexander-akhmetov/tdlib-compiled/releases/download/v1.8.66/libtdjson-linux-x86_64.so",
	"linux/arm64":   "https://github.com/alexander-akhmetov/tdlib-compiled/releases/download/v1.8.66/libtdjson-linux-aarch64.so",
	"darwin/arm64":  "https://github.com/alexander-akhmetov/tdlib-compiled/releases/download/v1.8.66/libtdjson-macos-arm64.dylib",
	"darwin/amd64":  "https://github.com/alexander-akhmetov/tdlib-compiled/releases/download/v1.8.66/libtdjson-macos-x86_64.dylib",
	"windows/amd64": "https://github.com/ForNeVeR/tdlib.native/releases/download/v1.8.66/tdlib.native.windows.x86-64.zip",
}

// TDLibBinaryName returns the platform-specific library filename.
func TDLibBinaryName(goos string) string {
	switch goos {
	case "windows":
		return "tdjson.dll"
	case "darwin":
		return "libtdjson.dylib"
	default:
		return "libtdjson.so"
	}
}

// TDLibDownloadURL returns the asset download URL for the target OS and arch.
func TDLibDownloadURL(goos, goarch string) string {
	if custom := os.Getenv("SPERMACORE_TDJSON_URL"); strings.TrimSpace(custom) != "" {
		return strings.TrimSpace(custom)
	}
	key := fmt.Sprintf("%s/%s", goos, goarch)
	return TDLibPrebuiltURLs[key]
}

// EnsureTDLibBinary checks whether the platform TDLib library exists in dataDir.
// If absent, it automatically downloads it from verified prebuilt releases.
func EnsureTDLibBinary(dataDir string) error {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	binName := TDLibBinaryName(goos)

	cleanDir := filepath.Clean(dataDir)
	var targetDir string
	if strings.HasSuffix(cleanDir, "telegram") {
		targetDir = cleanDir
	} else {
		targetDir = filepath.Join(cleanDir, "telegram")
	}

	targetPath := filepath.Join(targetDir, binName)
	if fi, err := os.Stat(targetPath); err == nil && fi.Size() > 1024*1024 {
		return nil
	}

	// Also check directly inside dataDir or root directory as fallback
	if fi, err := os.Stat(filepath.Join(dataDir, binName)); err == nil && fi.Size() > 1024*1024 {
		return nil
	}
	if fi, err := os.Stat(binName); err == nil && fi.Size() > 1024*1024 {
		return nil
	}

	downloadURL := TDLibDownloadURL(goos, goarch)
	if downloadURL == "" {
		return fmt.Errorf("no prebuilt TDLib binary URL available for %s/%s", goos, goarch)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("cannot create telegram data dir %s: %w", targetDir, err)
	}

	log := GetLogger("tdlib")
	log.Info("TDLib library (%s) not found in %s", binName, targetDir)
	log.Info("downloading prebuilt %s (%s/%s) from %s ...", binName, goos, goarch, downloadURL)

	client := &http.Client{
		Timeout: 10 * time.Minute,
	}

	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}
	req.Header.Set("User-Agent", "spermacore-auto-updater")

	resp, err := client.Do(req)
	if err != nil {
		log.Warn("failed to connect to download mirror: %v", err)
		return fmt.Errorf("tdlib download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d for %s", resp.StatusCode, downloadURL)
	}

	tmpFile := targetPath + ".tmp"
	_ = os.Remove(tmpFile)

	if strings.HasSuffix(strings.ToLower(downloadURL), ".zip") {
		// Download archive into memory/file and extract binary
		zipData, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read zip payload: %w", err)
		}
		zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
		if err != nil {
			return fmt.Errorf("failed to parse zip archive: %w", err)
		}
		var found bool
		for _, f := range zr.File {
			if strings.EqualFold(filepath.Base(f.Name), binName) {
				rc, err := f.Open()
				if err != nil {
					return fmt.Errorf("failed to open file inside zip: %w", err)
				}
				outF, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
				if err != nil {
					rc.Close()
					return fmt.Errorf("failed to create temporary binary %s: %w", tmpFile, err)
				}
				_, copyErr := io.Copy(outF, rc)
				rc.Close()
				outF.Close()
				if copyErr != nil {
					_ = os.Remove(tmpFile)
					return fmt.Errorf("failed to extract binary: %w", copyErr)
				}
				found = true
				break
			}
		}
		if !found {
			_ = os.Remove(tmpFile)
			return fmt.Errorf("binary %s not found in zip archive", binName)
		}
	} else {
		// Direct binary download (.so / .dylib)
		outF, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("failed to create temporary file %s: %w", tmpFile, err)
		}
		written, err := io.Copy(outF, resp.Body)
		outF.Close()
		if err != nil {
			_ = os.Remove(tmpFile)
			return fmt.Errorf("failed to download binary data: %w", err)
		}
		if written < 1024*1024 {
			_ = os.Remove(tmpFile)
			return fmt.Errorf("downloaded file is suspiciously small (%d bytes)", written)
		}
	}

	if err := os.Rename(tmpFile, targetPath); err != nil {
		_ = os.Remove(targetPath)
		if err := os.Rename(tmpFile, targetPath); err != nil {
			_ = os.Remove(tmpFile)
			return fmt.Errorf("failed to move downloaded binary to %s: %w", targetPath, err)
		}
	}

	_ = os.Chmod(targetPath, 0755)
	if fi, err := os.Stat(targetPath); err == nil {
		log.Info("TDLib binary saved to %s (size: %.2f MB)", targetPath, float64(fi.Size())/(1024*1024))
	}
	return nil
}
