// Package sandbox defines the native-module isolation boundary.
//
// Plugins can be isolated out-of-process via the Sidecar IPC runner.
// In sidecar mode, native C/C++ modules execute in independent child processes
// communicating over local IPC (Named Pipes on Windows / Unix Domain Sockets on Linux).
// A crash in native code (SIGSEGV, TDLib fault) is caught by the supervisor
// and automatically restarted within 500ms without crashing the Go core.
package sandbox

import "fmt"

type Config struct {
	Enabled       bool
	Mode          string
	AllowUnsigned bool
}

func Validate(c Config) error {
	if !c.Enabled {
		return nil
	}
	if c.Mode == "" {
		c.Mode = "sidecar"
	}
	switch c.Mode {
	case "sidecar", "process", "ipc":
		return nil
	default:
		return fmt.Errorf("sandbox: unsupported mode %q (supported: sidecar, process, ipc)", c.Mode)
	}
}

func Status(c Config) string {
	if !c.Enabled {
		return "disabled"
	}
	return "sidecar-isolated"
}
