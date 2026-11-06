//go:build !cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"spermacore/core/dashboard"
	"spermacore/core/sidecar"
)

var (
	errNativeAddonsUnavailable = errors.New("native addon unavailable: sidecar runner not found, or build with CGO_ENABLED=1")
	gCfg                       *Config
	busMu                      sync.RWMutex
	busAddons                  []*LoadedAddon
)

func SetGlobalConfig(c *Config) {
	gCfg = c
}

func SetGlobalProvider(p LLMProvider) {}

func pluginSyscall(method, args string) (string, int) {
	return "", -1
}

func emitMediaExt(mediaType uint32, format, payload string, outID *uint32) (uint32, int) {
	return 0, 0
}

type LoadedAddon struct {
	Name             string
	Version          string
	Desc             string
	LLMDescription   string
	HumanDescription string
	Path             string
	TransportName    string
	supervisor       *sidecar.Supervisor
}

type WidgetInfo struct {
	ID       string
	Title    string
	Icon     string
	DefaultW int
	DefaultH int
	owner    *LoadedAddon
}

func (a *LoadedAddon) CallTool(name, args string) (string, error) {
	if a.supervisor != nil {
		payload, _ := json.Marshal(map[string]string{"args": args})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgCallTool,
			Method:  name,
			Payload: payload,
		})
		if err != nil {
			return "", err
		}
		var out struct {
			Result string `json:"result"`
		}
		_ = json.Unmarshal(resp.Payload, &out)
		if out.Result != "" {
			return out.Result, nil
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) GetTools() ([]map[string]any, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type: sidecar.MsgGetTools,
		})
		if err != nil {
			return nil, err
		}
		var tools []map[string]any
		_ = json.Unmarshal(resp.Payload, &tools)
		return tools, nil
	}
	return nil, errNativeAddonsUnavailable
}

func (a *LoadedAddon) OnStartup() int                      { return 0 }
func (a *LoadedAddon) OnConfigReload(string) int           { return 0 }
func (a *LoadedAddon) NotifyConfigVersion(uint32)          {}
func (a *LoadedAddon) GetSystemPrompt() (string, error)    { return "", nil }
func (a *LoadedAddon) deinit() int                         { return 0 }
func (a *LoadedAddon) Unload()                             {}
func (a *LoadedAddon) onShutdown() int {
	if a.supervisor != nil {
		a.supervisor.Stop()
	}
	return 0
}
func (a *LoadedAddon) HealthCheckEnhanced() string         { return "" }
func (a *LoadedAddon) HookNames() []string                 { return nil }
func (a *LoadedAddon) Widgets() []*WidgetInfo              { return nil }
func (w *WidgetInfo) Render() string                       { return "" }
func (w *WidgetInfo) Update() string                       { return "" }
func (w *WidgetInfo) Click(payload string) string          { return "" }

func (a *LoadedAddon) UpdateWidget(id string) string         { return "" }
func (a *LoadedAddon) ClickWidget(id, payload string) string { return "" }

// transport methods for sidecar runners
func (a *LoadedAddon) TransportNameResolved() string    { return a.TransportName }
func (a *LoadedAddon) TransportDisplayName() string     { return a.Name }
func (a *LoadedAddon) TransportDescription() string     { return a.Desc }
func (a *LoadedAddon) TransportCapabilities() uint32    { return 0xFFFFFFFF }
func (a *LoadedAddon) TransportMaxTextLen() int         { return 4096 }
func (a *LoadedAddon) HasNativeTransport() bool {
	return a.supervisor != nil && a.TransportName != ""
}
func (a *LoadedAddon) TransportSupports(which int) bool {
	return a.supervisor != nil
}

func (a *LoadedAddon) TransportSendText(payload string) (string, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgTransportSend,
			Method:  "send_text",
			Payload: json.RawMessage(payload),
		})
		if err != nil {
			return "", err
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) TransportSendMedia(payload string) (string, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgTransportSend,
			Method:  "send_media",
			Payload: json.RawMessage(payload),
		})
		if err != nil {
			return "", err
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) TransportReact(payload string) (string, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgTransportSend,
			Method:  "react",
			Payload: json.RawMessage(payload),
		})
		if err != nil {
			return "", err
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) TransportMarkRead(payload string) (string, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgTransportSend,
			Method:  "mark_read",
			Payload: json.RawMessage(payload),
		})
		if err != nil {
			return "", err
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) TransportTyping(payload string) (string, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgTransportSend,
			Method:  "typing",
			Payload: json.RawMessage(payload),
		})
		if err != nil {
			return "", err
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) TransportEdit(payload string) (string, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgTransportSend,
			Method:  "edit",
			Payload: json.RawMessage(payload),
		})
		if err != nil {
			return "", err
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) TransportDelete(payload string) (string, error) {
	if a.supervisor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := a.supervisor.Call(ctx, &sidecar.IPCMessage{
			Type:    sidecar.MsgTransportSend,
			Method:  "delete",
			Payload: json.RawMessage(payload),
		})
		if err != nil {
			return "", err
		}
		return string(resp.Payload), nil
	}
	return "", errNativeAddonsUnavailable
}

func (a *LoadedAddon) TransportPoll(payload string) (string, error) {
	return "", nil
}

func checkPluginLoad(path string) error {
	return nil
}

func loadAddons(rootDir string, cfg *Config, log *Logger) []*LoadedAddon {
	if cfg == nil || len(cfg.Plugins.Load) == 0 {
		return nil
	}

	var out []*LoadedAddon
	loaded := make([]dashboard.PluginInfo, 0, len(cfg.Plugins.Load))

	for _, entry := range cfg.Plugins.Load {
		name := filepath.Base(strings.TrimRight(entry, `/\`))
		if !cfg.isPluginEnabled(name) {
			log.Info("plugin %s: disabled in config", name)
			loaded = append(loaded, dashboard.PluginInfo{
				Name: name, Version: "-", Enabled: false, Loaded: false,
			})
			continue
		}

		// check for sidecar binary
		cmdPath := ""
		candidates := []string{
			filepath.Join(rootDir, "modules", name, "sidecar.exe"),
			filepath.Join(rootDir, "modules", name, "sidecar"),
			filepath.Join(rootDir, "bin", fmt.Sprintf("sidecar-%s.exe", name)),
			filepath.Join(rootDir, "bin", fmt.Sprintf("sidecar-%s", name)),
		}
		for _, cand := range candidates {
			if _, err := os.Stat(cand); err == nil {
				cmdPath = cand
				break
			}
		}

		if cmdPath == "" {
			log.Warn("plugin %s: compiled without cgo (cgo_enabled=0). run sidecar runner (%s) or recompile with cgo_enabled=1", name, candidates[0])
			loaded = append(loaded, dashboard.PluginInfo{
				Name: name, Version: "sidecar-pending", Enabled: true, Loaded: false,
			})
			continue
		}

		sup := sidecar.NewSupervisor(sidecar.SupervisorConfig{
			ModuleName:   name,
			Command:      cmdPath,
			AutoRestart:  true,
			RestartDelay: 500 * time.Millisecond,
		})

		sup.SetEventHandler(func(evJSON string) {
			_, _ = emitMediaExt(0, "json", evJSON, nil)
		})
		sup.SetLogHandler(func(level int, tag, msg string) {
			GetLogger(tag).Info("%s", msg)
		})

		if err := sup.Start(); err != nil {
			log.Error("plugin %s: sidecar startup error: %v", name, err)
			continue
		}

		addon := &LoadedAddon{
			Name:          name,
			Version:       "sidecar-1.0",
			Path:          cmdPath,
			TransportName: name,
			supervisor:    sup,
		}
		out = append(out, addon)
		loaded = append(loaded, dashboard.PluginInfo{
			Name: name, Version: "sidecar", Desc: "out-of-process sidecar", Enabled: true, Loaded: true,
		})
		log.Info("plugin %s started in isolated sidecar process", name)
	}

	if gDash != nil {
		gDash.SetPlugins(loaded)
	}
	return out
}

const (
	scHookBeforeEvent     = "before_event"
	scHookBeforeSend      = "before_send"
	scHookAfterSend       = "after_send"
	scHookOnMessageSent   = "on_message_sent"
	scHookOnTransportSend = "on_transport_send"
)

func setEventAddons(addons []*LoadedAddon, log *Logger)                  {}
func transportRegister(name string)                                      {}
func transportOwner(name string) *LoadedAddon                            { return nil }
func dispatchHookTo(a *LoadedAddon, name, payload string) bool           { return false }
func dispatchHook(name, payload string) bool                             { return false }
func sendHookPayload(transport string, chatID int64, text string) string { return "" }
func emitMediaToPlugins(ev transportEvent)                               {}
func hookBeforeLLM(model string, messages []ChatMessage) (bool, string)  { return false, "" }
func hookAfterLLM(model, content string) string                          { return "" }
func hookToolCall(tool, args string) (string, bool)                      { return "", false }
func hookToolResult(tool, result string) string                          { return "" }
func bumpConfigVersion()                                                 {}
func startConfigWatch(log *Logger)                                       {}
