package main

/*
#cgo CFLAGS: -I${SRCDIR}/../include
#include <stdlib.h>
#include "plugin_api.h"

static const sc_hook_reg_t* sc_hook_at(const sc_hook_reg_t* arr, size_t i) {
    return &arr[i];
}
static int sc_hook_call(const sc_hook_reg_t* reg, const char* name,
                        const char* payload, char** out) {
    if (!reg || !reg->fn) return SC_ERR_INVALID_ARG;
    return reg->fn(reg->userdata, name, payload, out);
}
static int c_invoke_callback(void* cb, void* ud, const char* name,
                             const char* payload, char** out) {
    sc_hook_fn fn = (sc_hook_fn)cb;
    if (!fn) return SC_ERR_INVALID_ARG;
    return fn(ud, name, payload, out);
}
static const sc_syscall_reg_t* sc_syscall_at(const sc_syscall_reg_t* arr, size_t i) {
    return &arr[i];
}
static int sc_syscall_call(const sc_syscall_reg_t* reg, const char* method,
                           const char* args, char** out) {
    if (!reg || !reg->fn) return SC_ERR_INVALID_ARG;
    return reg->fn(method, args, out);
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	scHookOnMedia        = "on_media"
	scHookOnCustomEvent  = "on_custom_event"
	scHookAsyncCompleted = "async_tool_completed"

	scHookBeforeEvent      = "before_event"
	scHookBeforeLLMCall    = "before_llm_call"
	scHookAfterLLMResponse = "after_llm_response"
	scHookOnToolCall       = "on_tool_call"
	scHookOnToolResult     = "on_tool_result"
	scHookBeforeSend       = "before_send"
	scHookAfterSend        = "after_send"
	scHookOnMessageSent    = "on_message_sent"

	scHookOnTransportSend = "on_transport_send"
)

var mediaTypeMimes = map[uint32]string{
	1: "text/plain", 2: "image/jpeg", 3: "video/mp4",
	4: "audio/ogg", 5: "audio/mpeg", 6: "image/webp", 7: "video/webm",
	8: "application/octet-stream", 9: "text/html", 10: "application/json",
}

var mediaStringToEnum = map[string]uint32{
	"":          0,
	"text":      1,
	"photo":     2,
	"video_msg": 3,
	"voice":     4,
	"audio":     5,
	"sticker":   6,
	"sticker_a": 7,
	"document":  8,
	"link":      9,
	"location":  10,
}

type mediaFormatRec struct {
	mime string
	desc string
}

var (
	busMu     sync.RWMutex
	busAddons []*LoadedAddon

	mediaFormats = map[string]mediaFormatRec{}
	busOwners    map[string]*LoadedAddon
	transports   = map[string]*LoadedAddon{}

	eventSeq       atomic.Uint32
	configVersionN atomic.Uint32
)

func setEventAddons(addons []*LoadedAddon, log *Logger) {
	busMu.Lock()
	defer busMu.Unlock()
	busAddons = addons
	_, busOwners = collectTools(addons, log)
	transports = map[string]*LoadedAddon{}
	for _, a := range addons {
		if a != nil && a.TransportName != "" {
			transports[a.TransportName] = a
			if a.HasNativeTransport() {
				GetLogger("core.transport").Info("transport registered: %s (via %s, caps=0x%x, max_text=%d)",
					a.TransportName, a.Name, a.TransportCapabilities(), a.TransportMaxTextLen())
				transportRegister(a.TransportName)
			} else {
				GetLogger("core.transport").Info("transport registered (legacy hook): %s (via %s)",
					a.TransportName, a.Name)
			}
		}
	}
}

func transportOwner(name string) *LoadedAddon {
	busMu.RLock()
	defer busMu.RUnlock()
	owner, _ := transports[name]
	return owner
}

// transport dashboard status
var (
	transportSeenMu sync.Mutex
	transportSeen   = map[string]bool{}
)

// transportRegister adds transport to dashboard
func transportRegister(name string) {
	name = strings.TrimSpace(name)
	if name == "" || gDash == nil {
		return
	}
	transportSeenMu.Lock()
	seen := transportSeen[name]
	transportSeen[name] = true
	transportSeenMu.Unlock()
	if !seen {
		gDash.AddTransport(name, "plugin", "registered")
	}
}

// transportStatus updates transport connectivity in dashboard
func transportStatus(name string, connected bool, detailJSON string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	status := "disconnected"
	if connected {
		status = "connected"
	}
	if detailJSON != "" {
		GetLogger("core.transport").Debug("transport %s: %s %s", name, status, detailJSON)
	} else {
		GetLogger("core.transport").Debug("transport %s: %s", name, status)
	}
	if gDash == nil {
		return
	}
	transportSeenMu.Lock()
	seen := transportSeen[name]
	transportSeen[name] = true
	transportSeenMu.Unlock()
	if seen {
		gDash.UpdateTransport(name, status)
		return
	}
	gDash.AddTransport(name, "plugin", status)
}

func dispatchHookTo(a *LoadedAddon, name, payload string) bool {
	if a == nil || a.hooksPtr == nil || a.hooksLen == 0 {
		return false
	}
	for i := C.size_t(0); i < a.hooksLen; i++ {
		reg := C.sc_hook_at(a.hooksPtr, i)
		if reg == nil || reg.name == nil {
			continue
		}
		if C.GoString(reg.name) != name {
			continue
		}
		if sup, _ := dispatchOne(a, reg, name, payload); sup {
			return true
		}
	}
	return false
}

func dispatchHook(name, payload string) bool {
	return dispatchHookFull(name, payload, nil)
}

func dispatchHookFull(name, payload string, collect func(string)) bool {
	busMu.RLock()
	addons := busAddons
	busMu.RUnlock()

	var callbacks []dispatchItem
	for _, a := range addons {
		if a == nil || a.hooksPtr == nil || a.hooksLen == 0 {
			continue
		}
		for i := C.size_t(0); i < a.hooksLen; i++ {
			reg := C.sc_hook_at(a.hooksPtr, i)
			if reg == nil || reg.name == nil {
				continue
			}
			if C.GoString(reg.name) != name {
				continue
			}
			callbacks = append(callbacks, dispatchItem{
				a: a, reg: reg, pri: C.uint(reg.priority),
			})
		}
	}

	for i := 0; i < len(callbacks); i++ {
		for j := 0; j+1 < len(callbacks)-i; j++ {
			if callbacks[j].pri < callbacks[j+1].pri {
				tmp := callbacks[j]
				callbacks[j] = callbacks[j+1]
				callbacks[j+1] = tmp
			}
		}
	}

	for _, cb := range callbacks {
		sup, out := dispatchOne(cb.a, cb.reg, name, payload)
		if out != "" && collect != nil {
			collect(out)
		}
		if sup {
			return true
		}
	}
	return false
}

type dispatchItem struct {
	a   *LoadedAddon
	reg *C.sc_hook_reg_t
	pri C.uint
}

// dispatchOne executes single hook and parses suppression
func dispatchOne(a *LoadedAddon, reg *C.sc_hook_reg_t, name, payload string) (bool, string) {
	// execute hook inside plugin scope
	defer a.enterCall()()
	defer a.beginScope()()

	cName := C.CString(name)
	cPayload := C.CString(payload)
	defer C.free(unsafe.Pointer(cName))
	defer C.free(unsafe.Pointer(cPayload))

	var out *C.char
	rc := C.sc_hook_call(reg, cName, cPayload, &out)
	suppressed := false
	outJSON := ""
	if out != nil {
		oj := C.GoString(out)
		var parsed map[string]any
		if json.Unmarshal([]byte(oj), &parsed) == nil {
			if s, _ := parsed["suppress"].(bool); s {
				suppressed = true
			}
		}
		outJSON = oj
		a.freePluginOut(out)
	}
	if suppressed {
		GetLogger("core.events").Debug("event %q suppressed by %s (rc=%d)",
			name, a.Name, int(rc))
	}
	return suppressed, outJSON
}

func emitMedia(mediaType uint32, payload string) int {
	_, rc := emitMediaExt(mediaType, "", payload, nil)
	return rc
}

func emitMediaExt(mediaType uint32, format, payload string, outID *uint32) (uint32, int) {
	// format parameter retained for abi compatibility
	_ = format
	id := eventSeq.Add(1)
	if outID != nil {
		*outID = id
	}
	dispatchHook(scHookOnMedia, payload)
	return id, 0
}

func emitCustomEvent(name, payload string) int {
	if name == "" {
		return -2
	}
	dispatchHook(scHookOnCustomEvent, payload)
	return 0
}

func registerMediaFormat(format, mime, desc string) int {
	if format == "" {
		return -2
	}
	busMu.Lock()
	mediaFormats[format] = mediaFormatRec{mime: mime, desc: desc}
	busMu.Unlock()
	return 0
}

func mediaMimeType(mediaType uint32) string {
	if m, ok := mediaTypeMimes[mediaType]; ok {
		return m
	}
	return ""
}

func getConfigVersion() uint32 { return configVersionN.Load() }
func bumpConfigVersion()       { configVersionN.Add(1) }

func isShutdownRequested() int {
	select {
	case <-shutdownCh:
		return 1
	default:
		return 0
	}
}

func healthCheck(severity uint32, message string) {
	GetLogger("plugin.health").Info("severity=%d msg=%s", severity, message)
	if gDash != nil {
		gDash.Log(mapLevel(LevelInfo), "plugin", mapSeverity(severity)+" "+message)
	}
}

func mapSeverity(s uint32) string {
	switch s {
	case 0:
		return "OK"
	case 1:
		return "DEGRADED"
	case 2:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

func callToolAsync(tool, args string, cb unsafe.Pointer, ud unsafe.Pointer) uint64 {
	handle := uint64(eventSeq.Add(1))
	go func() {
		busMu.RLock()
		owners := busOwners
		busMu.RUnlock()

		var result string
		if owner, ok := owners[tool]; ok {
			r, err := owner.CallTool(tool, args)
			if err != nil {
				result = `{"status":"error","reason":` + jsonQuote(err.Error()) + `}`
			} else if r == "" {
				result = "null"
			} else {
				result = r
			}
		} else {
			result = `{"status":"error","reason":"tool not found"}`
		}

		if cb != nil {
			cName := C.CString("async_tool_completed")
			cPayload := C.CString(result)
			var out *C.char
			C.c_invoke_callback(cb, ud, cName, cPayload, &out)
			if out != nil {
				// callback out_json owned by plugin
			}
			C.free(unsafe.Pointer(cName))
			C.free(unsafe.Pointer(cPayload))
		}
	}()
	return handle
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func emitMediaToPlugins(ev transportEvent) {
	enum, ok := mediaStringToEnum[ev.MediaType]
	if !ok {
		enum = 8
	}
	payload := struct {
		Event      string `json:"event"`
		MediaType  uint32 `json:"media_type"`
		ChatID     int64  `json:"chat_id"`
		SenderName string `json:"sender_name,omitempty"`
		SenderID   int64  `json:"sender_id"`
		Text       string `json:"text,omitempty"`
	}{
		Event: "message", MediaType: enum,
		ChatID: ev.ChatID, SenderName: ev.SenderName,
		SenderID: ev.SenderID, Text: ev.Text,
	}
	b, _ := json.Marshal(payload)
	dispatchHook(scHookOnMedia, string(b))
}

func emitEvent(eventJSON string) int {
	if eventJSON == "" {
		return -2
	}
	dispatchHook(scHookOnCustomEvent, eventJSON)
	return 0
}

func callTool(name, args string) (string, error) {
	busMu.RLock()
	owners := busOwners
	busMu.RUnlock()
	if owner, ok := owners[name]; ok {
		return owner.CallTool(name, args)
	}
	return "", fmt.Errorf("tool %q not found", name)
}

func pluginSyscall(method, args string) (string, int) {
	busMu.RLock()
	addons := busAddons
	busMu.RUnlock()

	var cands []syscallItem
	for _, a := range addons {
		if a == nil || a.syscallsPtr == nil || a.syscallsLen == 0 {
			continue
		}
		for i := C.size_t(0); i < a.syscallsLen; i++ {
			reg := C.sc_syscall_at(a.syscallsPtr, i)
			if reg == nil || reg.prefix == nil {
				continue
			}
			pref := C.GoString(reg.prefix)
			if pref == "" {
				cands = append(cands, syscallItem{a: a, reg: reg, pl: 0})
				continue
			}
			if strings.HasPrefix(method, pref) {
				cands = append(cands, syscallItem{a: a, reg: reg, pl: len(pref)})
			}
		}
	}
	if len(cands) == 0 {
		return "", 0
	}

	for i := 0; i < len(cands); i++ {
		for j := 0; j+1 < len(cands)-i; j++ {
			if cands[j].pl < cands[j+1].pl {
				tmp := cands[j]
				cands[j] = cands[j+1]
				cands[j+1] = tmp
			}
		}
	}

	cMethod := C.CString(method)
	cArgs := C.CString(args)
	defer C.free(unsafe.Pointer(cMethod))
	defer C.free(unsafe.Pointer(cArgs))

	for _, c := range cands {
		releaseCall := c.a.enterCall()
		release := c.a.beginScope()
		var out *C.char
		rc := C.sc_syscall_call(c.reg, cMethod, cArgs, &out)
		result := ""
		if rc == 0 && out != nil {
			result = C.GoString(out)
			c.a.freePluginOut(out)
		}
		release()
		releaseCall()
		if rc != 0 {
			continue
		}
		return result, 1
	}
	return "", 0
}

type syscallItem struct {
	a   *LoadedAddon
	reg *C.sc_syscall_reg_t
	pl  int
}

var (
	alarmMu     sync.Mutex
	alarmCancel = map[uint64]*atomic.Bool{}
)

func setAlarm(afterSeconds uint32, payload string) uint64 {
	handle := uint64(eventSeq.Add(1))
	c := &atomic.Bool{}
	alarmMu.Lock()
	alarmCancel[handle] = c
	alarmMu.Unlock()
	go func() {
		time.Sleep(time.Duration(int(afterSeconds)) * time.Second)
		if c.Load() {
			return
		}
		alarmMu.Lock()
		delete(alarmCancel, handle)
		alarmMu.Unlock()
		dispatchHook(scHookOnCustomEvent, payload)
	}()
	return handle
}

func cancelAlarm(handle uint64) int {
	alarmMu.Lock()
	c, ok := alarmCancel[handle]
	if ok {
		c.Store(true)
		delete(alarmCancel, handle)
	}
	alarmMu.Unlock()
	return 0
}

func isPluginLoaded(name string) int {
	busMu.RLock()
	defer busMu.RUnlock()
	for _, a := range busAddons {
		if a != nil && a.Name == name {
			return 1
		}
	}
	return 0
}

var procStart = time.Now()

func nowMs() uint64 { return uint64(time.Since(procStart).Milliseconds()) }

func hookBeforeLLM(model string, messages []ChatMessage) (skip bool, cached string) {
	payload := struct {
		Model    string        `json:"model"`
		Messages []ChatMessage `json:"messages"`
	}{Model: model, Messages: messages}
	b, _ := json.Marshal(payload)
	cached = ""
	suppressed := dispatchHookFull(scHookBeforeLLMCall, string(b), func(oj string) {
		if cached != "" {
			return
		}
		var o map[string]any
		if json.Unmarshal([]byte(oj), &o) != nil {
			return
		}
		if r, ok := o["response"].(map[string]any); ok {
			if c, ok := r["content"].(string); ok {
				cached = c
			}
		}
	})
	return suppressed || cached != "", cached
}

func hookAfterLLM(model, content string) string {
	payload := struct {
		Model   string `json:"model"`
		Content string `json:"content"`
	}{Model: model, Content: content}
	b, _ := json.Marshal(payload)
	rewritten := ""
	dispatchHookFull(scHookAfterLLMResponse, string(b), func(oj string) {
		if rewritten != "" {
			return
		}
		var o map[string]any
		if json.Unmarshal([]byte(oj), &o) != nil {
			return
		}
		if c, ok := o["content"].(string); ok {
			rewritten = c
		}
	})
	return rewritten
}

func hookToolCall(tool, args string) (override string, blocked bool) {
	payload := struct {
		Tool string `json:"tool"`
		Args string `json:"args"`
	}{Tool: tool, Args: args}
	b, _ := json.Marshal(payload)
	override = ""
	blocked = dispatchHookFull(scHookOnToolCall, string(b), func(oj string) {
		if override != "" {
			return
		}
		var o map[string]any
		if json.Unmarshal([]byte(oj), &o) != nil {
			return
		}
		if r, ok := o["result"].(string); ok {
			override = r
		}
	})
	return override, blocked
}

func hookToolResult(tool, result string) string {
	payload := struct {
		Tool   string `json:"tool"`
		Result string `json:"result"`
	}{Tool: tool, Result: result}
	b, _ := json.Marshal(payload)
	rewritten := ""
	dispatchHookFull(scHookOnToolResult, string(b), func(oj string) {
		if rewritten != "" {
			return
		}
		var o map[string]any
		if json.Unmarshal([]byte(oj), &o) != nil {
			return
		}
		if r, ok := o["result"].(string); ok {
			rewritten = r
		}
	})
	return rewritten
}

func sendHookPayload(transport string, chatID int64, text string) string {
	b, _ := json.Marshal(struct {
		Transport string `json:"transport"`
		ChatID    int64  `json:"chat_id"`
		Text      string `json:"text"`
	}{Transport: transport, ChatID: chatID, Text: text})
	return string(b)
}
