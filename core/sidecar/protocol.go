package sidecar

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// MessageType indicates the kind of IPC message.
type MessageType string

const (
	MsgHandshake       MessageType = "handshake"
	MsgHandshakeAck    MessageType = "handshake_ack"
	MsgPing            MessageType = "ping"
	MsgPong            MessageType = "pong"
	MsgCallTool        MessageType = "call_tool"
	MsgToolResult      MessageType = "tool_result"
	MsgGetTools        MessageType = "get_tools"
	MsgToolsList       MessageType = "tools_list"
	MsgTransportSend   MessageType = "transport_send"
	MsgTransportResult MessageType = "transport_result"
	MsgEmitEvent       MessageType = "emit_event"
	MsgLog             MessageType = "log"
	MsgShutdown        MessageType = "shutdown"
	MsgReload          MessageType = "reload"
)

// IPCMessage represents a frame sent across the sidecar boundary.
type IPCMessage struct {
	ID      uint64          `json:"id,omitempty"`
	Type    MessageType     `json:"type"`
	Module  string          `json:"module,omitempty"`
	Method  string          `json:"method,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Framing: 4 bytes big-endian length prefix followed by JSON payload.
const maxFrameSize = 64 * 1024 * 1024 // 64 MB

var (
	writeMu sync.Mutex
)

// ReadMessage reads a length-prefixed IPC message from reader.
func ReadMessage(r io.Reader) (*IPCMessage, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	if length > maxFrameSize {
		return nil, fmt.Errorf("sidecar frame exceeds maximum size: %d > %d", length, maxFrameSize)
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("read sidecar frame payload: %w", err)
	}

	var msg IPCMessage
	if err := json.Unmarshal(buf, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal sidecar frame: %w", err)
	}
	return &msg, nil
}

// WriteMessage writes a length-prefixed IPC message to writer.
func WriteMessage(w io.Writer, msg *IPCMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal sidecar frame: %w", err)
	}

	length := uint32(len(data))
	if length > maxFrameSize {
		return fmt.Errorf("sidecar frame exceeds maximum size: %d > %d", length, maxFrameSize)
	}

	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, length)

	writeMu.Lock()
	defer writeMu.Unlock()

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write sidecar frame header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write sidecar frame payload: %w", err)
	}
	return nil
}
