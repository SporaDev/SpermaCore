package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProtocolFraming(t *testing.T) {
	buf := &bytes.Buffer{}
	orig := &IPCMessage{
		ID:     42,
		Type:   MsgCallTool,
		Module: "telegram",
		Method: "send_text",
		Payload: json.RawMessage(`{"chat_id": 12345, "text": "hello"}`),
	}

	if err := WriteMessage(buf, orig); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	read, err := ReadMessage(buf)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	if read.ID != orig.ID || read.Type != orig.Type || read.Module != orig.Module || read.Method != orig.Method {
		t.Fatalf("frame mismatch: got %+v, want %+v", read, orig)
	}
	cleanRead := strings.ReplaceAll(string(read.Payload), " ", "")
	cleanOrig := strings.ReplaceAll(string(orig.Payload), " ", "")
	if cleanRead != cleanOrig {
		t.Fatalf("payload mismatch: %s != %s", cleanRead, cleanOrig)
	}
}

func TestSupervisorEchoAndCrashRecovery(t *testing.T) {
	if os.Getenv("SIDECAR_TEST_CHILD") == "1" {
		runTestChild()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	sup := NewSupervisor(SupervisorConfig{
		ModuleName:   "mock-plugin",
		Command:      exe,
		Args:         []string{"-test.run=TestSupervisorEchoAndCrashRecovery"},
		Env:          []string{"SIDECAR_TEST_CHILD=1"},
		AutoRestart:  true,
		RestartDelay: 100 * time.Millisecond,
	})

	var eventCount atomic.Int32
	sup.SetEventHandler(func(p string) {
		eventCount.Add(1)
	})

	if err := sup.Start(); err != nil {
		t.Fatalf("supervisor start: %v", err)
	}
	defer sup.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 1. send call_tool
	resp, err := sup.Call(ctx, &IPCMessage{
		Type:    MsgCallTool,
		Method:  "echo",
		Payload: json.RawMessage(`{"msg":"ping"}`),
	})
	if err != nil {
		t.Fatalf("call error: %v", err)
	}
	if !strings.Contains(string(resp.Payload), "echo: ping") {
		t.Fatalf("unexpected echo response: %s", string(resp.Payload))
	}

	// 2. test crash and 500ms auto-restart: trigger child crash via "crash" method
	_, _ = sup.Call(ctx, &IPCMessage{
		Type:   MsgCallTool,
		Method: "crash",
	})

	// Wait for supervisor to detect exit, wait RestartDelay, and restart
	time.Sleep(300 * time.Millisecond)

	// 3. verify module is resurrected and responds to calls again
	resp2, err := sup.Call(ctx, &IPCMessage{
		Type:    MsgCallTool,
		Method:  "echo",
		Payload: json.RawMessage(`{"msg":"after_resurrection"}`),
	})
	if err != nil {
		t.Fatalf("call after restart error: %v", err)
	}
	if !strings.Contains(string(resp2.Payload), "echo: after_resurrection") {
		t.Fatalf("unexpected echo after restart: %s", string(resp2.Payload))
	}

	// 4. test hot-reload
	if err := sup.Reload(); err != nil {
		t.Fatalf("hot-reload error: %v", err)
	}

	resp3, err := sup.Call(ctx, &IPCMessage{
		Type:    MsgCallTool,
		Method:  "echo",
		Payload: json.RawMessage(`{"msg":"after_reload"}`),
	})
	if err != nil {
		t.Fatalf("call after reload error: %v", err)
	}
	if !strings.Contains(string(resp3.Payload), "echo: after_reload") {
		t.Fatalf("unexpected echo after reload: %s", string(resp3.Payload))
	}
}

// runTestChild simulates an external plugin process communicating over stdio.
func runTestChild() {
	for {
		msg, err := ReadMessage(os.Stdin)
		if err != nil {
			return
		}

		switch msg.Method {
		case "crash":
			// simulate segmentation fault / unhandled crash
			os.Exit(42)
		case "echo":
			var payload struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(msg.Payload, &payload)
			_ = WriteMessage(os.Stdout, &IPCMessage{
				ID:      msg.ID,
				Type:    MsgToolResult,
				Payload: json.RawMessage(`{"result":"echo: ` + payload.Msg + `"}`),
			})
		default:
			_ = WriteMessage(os.Stdout, &IPCMessage{
				ID:      msg.ID,
				Type:    MsgToolResult,
				Payload: json.RawMessage(`{"result":"ok"}`),
			})
		}
	}
}

func init() {
	// prevent recursive test runner exec issues
	_ = exec.Command
}
