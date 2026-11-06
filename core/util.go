package main

import (
	"encoding/base64"
	"os"
	"runtime"
)

// truncate shortens string to n runes.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos] + "..."
		}
		i++
	}
	return s
}

func readFileBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func detectRedStarOS() string {
	arch := runtime.GOARCH
	switch runtime.GOOS {
	case "windows":
		return "Windows (" + arch + ")"
	case "linux":
		return "Linux (" + arch + ")"
	case "darwin":
		return "macOS (" + arch + ")"
	default:
		return runtime.GOOS + " (" + arch + ")"
	}
}

// levelName returns short level name for webui.
func levelName(l Level) string {
	switch l {
	case LevelDebug:
		return "DBG"
	case LevelInfo:
		return "INF"
	case LevelTool:
		return "TUL"
	case LevelWarn:
		return "WRN"
	case LevelError:
		return "ERR"
	}
	return "INF"
}
