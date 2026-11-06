package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type Logger interface {
	Info(format string, args ...any)
	Warn(format string, args ...any)
	Error(format string, args ...any)
	Debug(format string, args ...any)
}

type defaultLogger struct{}

func (defaultLogger) Info(format string, args ...any)  { fmt.Printf("[INFO] "+format+"\n", args...) }
func (defaultLogger) Warn(format string, args ...any)  { fmt.Printf("[WARN] "+format+"\n", args...) }
func (defaultLogger) Error(format string, args ...any) { fmt.Printf("[ERROR] "+format+"\n", args...) }
func (defaultLogger) Debug(format string, args ...any) {}

type SupervisorConfig struct {
	ModuleName   string
	Command      string
	Args         []string
	Env          []string
	AutoRestart  bool
	RestartDelay time.Duration
	Logger       Logger
}

type processRun struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	doneCh chan struct{}
}

type Supervisor struct {
	cfg       SupervisorConfig
	log       Logger
	mu        sync.RWMutex
	current   *processRun
	seq       atomic.Uint64
	pending   map[uint64]chan *IPCMessage
	onEvent   func(payload string)
	onLog     func(level int, tag, msg string)
	closed    bool
	reloading bool
	stopCh    chan struct{}
}

func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = 500 * time.Millisecond
	}
	log := cfg.Logger
	if log == nil {
		log = defaultLogger{}
	}
	return &Supervisor{
		cfg:     cfg,
		log:     log,
		pending: make(map[uint64]chan *IPCMessage),
		stopCh:  make(chan struct{}),
	}
}

func (s *Supervisor) SetEventHandler(fn func(payload string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEvent = fn
}

func (s *Supervisor) SetLogHandler(fn func(level int, tag, msg string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onLog = fn
}

// Start launches the sidecar child process and begins supervision.
func (s *Supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("supervisor already closed")
	}
	return s.startChildLocked()
}

func (s *Supervisor) startChildLocked() error {
	if s.cfg.Command == "" {
		return fmt.Errorf("empty command for sidecar %s", s.cfg.ModuleName)
	}

	cmd := exec.Command(s.cfg.Command, s.cfg.Args...)
	if len(s.cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), s.cfg.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("create stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("start sidecar process %s: %w", s.cfg.ModuleName, err)
	}

	proc := &processRun{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		doneCh: make(chan struct{}),
	}
	s.current = proc

	s.log.Info("sidecar supervisor: child process %s started (pid=%d)", s.cfg.ModuleName, cmd.Process.Pid)

	go s.readLoop(stdout)
	go s.monitorProcess(proc)

	return nil
}

func (s *Supervisor) monitorProcess(proc *processRun) {
	err := proc.cmd.Wait()
	close(proc.doneCh)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	reloading := s.reloading
	s.reloading = false

	// fail pending requests for this crashed run
	for id, ch := range s.pending {
		delete(s.pending, id)
		close(ch)
	}
	s.mu.Unlock()

	if reloading {
		s.log.Info("sidecar supervisor: module %s stopped for reload", s.cfg.ModuleName)
		return
	}

	if s.cfg.AutoRestart {
		s.log.Warn("sidecar supervisor: warning: module %s process crashed (%v). auto-restart in %v...", s.cfg.ModuleName, err, s.cfg.RestartDelay)
		time.Sleep(s.cfg.RestartDelay)

		s.mu.Lock()
		if !s.closed {
			if restartErr := s.startChildLocked(); restartErr != nil {
				s.log.Error("sidecar supervisor: error auto-restarting %s: %v", s.cfg.ModuleName, restartErr)
			} else {
				s.log.Info("sidecar supervisor: module %s restarted after %v", s.cfg.ModuleName, s.cfg.RestartDelay)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Supervisor) readLoop(r io.Reader) {
	for {
		msg, err := ReadMessage(r)
		if err != nil {
			return
		}

		if msg.ID > 0 {
			s.mu.Lock()
			ch, ok := s.pending[msg.ID]
			if ok {
				delete(s.pending, msg.ID)
			}
			s.mu.Unlock()

			if ok && ch != nil {
				ch <- msg
				continue
			}
		}

		// handle async notifications
		switch msg.Type {
		case MsgEmitEvent:
			s.mu.RLock()
			handler := s.onEvent
			s.mu.RUnlock()
			if handler != nil {
				handler(string(msg.Payload))
			}
		case MsgLog:
			s.mu.RLock()
			handler := s.onLog
			s.mu.RUnlock()
			if handler != nil {
				var logPayload struct {
					Level int    `json:"level"`
					Tag   string `json:"tag"`
					Msg   string `json:"msg"`
				}
				if json.Unmarshal(msg.Payload, &logPayload) == nil {
					handler(logPayload.Level, logPayload.Tag, logPayload.Msg)
				}
			}
		case MsgPing:
			_ = s.Send(&IPCMessage{Type: MsgPong})
		}
	}
}

// Send writes an asynchronous IPC message to the sidecar.
func (s *Supervisor) Send(msg *IPCMessage) error {
	s.mu.RLock()
	proc := s.current
	s.mu.RUnlock()
	if proc == nil || proc.stdin == nil {
		return fmt.Errorf("sidecar %s not running", s.cfg.ModuleName)
	}
	return WriteMessage(proc.stdin, msg)
}

// Call sends a synchronous request to the sidecar and waits for a reply.
func (s *Supervisor) Call(ctx context.Context, msg *IPCMessage) (*IPCMessage, error) {
	id := s.seq.Add(1)
	msg.ID = id

	respCh := make(chan *IPCMessage, 1)

	s.mu.Lock()
	proc := s.current
	if proc == nil || proc.stdin == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("sidecar %s not running", s.cfg.ModuleName)
	}
	s.pending[id] = respCh
	s.mu.Unlock()

	if err := WriteMessage(proc.stdin, msg); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("write sidecar request: %w", err)
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-respCh:
		if !ok || resp == nil {
			return nil, fmt.Errorf("sidecar connection dropped")
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("sidecar error: %s", resp.Error)
		}
		return resp, nil
	}
}

// Reload stops the existing child process, waits for it to exit, and starts a fresh one (hot-reload).
func (s *Supervisor) Reload() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("supervisor closed")
	}

	s.reloading = true
	proc := s.current
	s.mu.Unlock()

	if proc != nil && proc.cmd != nil && proc.cmd.Process != nil {
		_ = proc.cmd.Process.Kill()
		<-proc.doneCh
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloading = false
	if s.closed {
		return nil
	}
	return s.startChildLocked()
}

// Stop shuts down the supervisor and child process cleanly.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	proc := s.current
	s.mu.Unlock()

	_ = s.Send(&IPCMessage{Type: MsgShutdown})

	if proc != nil {
		if proc.stdin != nil {
			_ = proc.stdin.Close()
		}
		if proc.cmd != nil && proc.cmd.Process != nil {
			_ = proc.cmd.Process.Kill()
			<-proc.doneCh
		}
	}
}
