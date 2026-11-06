package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"spermacore/core/dashboard"
)

// log levels
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelTool
	LevelWarn
	LevelError
)

var levelNames = map[Level]string{
	LevelDebug: "debug",
	LevelInfo:  "info",
	LevelTool:  "tool",
	LevelWarn:  "warn",
	LevelError: "error",
}

func ParseLevel(s string) Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return LevelDebug
	case "INFO":
		return LevelInfo
	case "TOOL":
		return LevelTool
	case "WARN", "WARNING":
		return LevelWarn
	case "ERROR", "CRITICAL":
		return LevelError
	default:
		return LevelInfo
	}
}

// mapLevel maps internal Level to dashboard.Level
func mapLevel(l Level) dashboard.Level {
	switch l {
	case LevelDebug:
		return dashboard.LevelDebug
	case LevelInfo:
		return dashboard.LevelInfo
	case LevelTool:
		return dashboard.LevelTool
	case LevelWarn:
		return dashboard.LevelWarn
	case LevelError:
		return dashboard.LevelError
	}
	return dashboard.LevelInfo
}

// rotatingWriter rotates log files at maxBytes threshold
type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

func newRotatingWriter(path string, maxBytes int64, maxFiles int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &rotatingWriter{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.file = f
	w.size = st.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxBytes {
		_ = w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	_ = w.file.Close()
	oldest := fmt.Sprintf("%s.%d", w.path, w.maxFiles-1)
	_ = os.Remove(oldest)
	for i := w.maxFiles - 2; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		dst := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Rename(w.path, w.path+".1")
	return w.open()
}

// Logger provides named logger sharing state across components
type Logger struct {
	name     string
	shared   *sharedState
	toStdout bool
}

type sharedState struct {
	mu        sync.Mutex
	file      io.Writer
	lvl       Level
	stdoutLvl Level
	uiSink    func(Level, string, string)
}

var (
	shared *sharedState
)

var (
	sharedMu sync.Mutex
)

// InitLogger initializes global shared logger state
func InitLogger(logsDir string, level Level) error {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return fmt.Errorf("logger: mkdir %q: %w", logsDir, err)
	}
	fw, err := newRotatingWriter(filepath.Join(logsDir, "agent.log"), 10*1024*1024, 5)
	if err != nil {
		return fmt.Errorf("logger: open log file: %w", err)
	}
	shared = &sharedState{
		file:      fw,
		lvl:       level,
		stdoutLvl: LevelInfo,
	}
	return nil
}

// SetUISink attaches dashboard sink for log events
func SetUISink(sink func(Level, string, string)) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared != nil {
		shared.uiSink = sink
	}
}

// GetLogger returns named logger instance
func GetLogger(name string) *Logger {
	if shared == nil {
		shared = &sharedState{file: os.Stderr, lvl: LevelInfo, stdoutLvl: LevelInfo}
	}
	return &Logger{name: name, shared: shared, toStdout: true}
}

func (l *Logger) log(lvl Level, format string, args ...any) {
	if lvl < l.shared.lvl {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)

	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	fmt.Fprintf(l.shared.file, "%s [%-5s] [%-16s] %s\n", ts, levelNames[lvl], l.name, msg)

	if l.shared.uiSink != nil {
		l.shared.uiSink(lvl, l.name, msg)
	} else if l.toStdout && lvl >= l.shared.stdoutLvl {
		fmt.Printf("[%s] %s - %s\n", levelNames[lvl], l.name, msg)
	}
}

func (l *Logger) Debug(format string, args ...any) { l.log(LevelDebug, format, args...) }

func (l *Logger) Info(format string, args ...any) { l.log(LevelInfo, format, args...) }

func (l *Logger) Tool(format string, args ...any) { l.log(LevelTool, format, args...) }

func (l *Logger) Warn(format string, args ...any) { l.log(LevelWarn, format, args...) }

func (l *Logger) Error(format string, args ...any) { l.log(LevelError, format, args...) }
