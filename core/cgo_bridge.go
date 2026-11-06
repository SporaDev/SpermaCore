package main

/*
#cgo CFLAGS: -I${SRCDIR}/../include
#cgo windows LDFLAGS: -lws2_32

#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include "plugin_api.h"
// exported go symbols
extern int    goPromptYesNo(char* question, int defaultValue);
extern int    goPromptText(char* question, char* defaultValue, char* out, size_t outsz);
extern int    goPromptSelect(char* question, char** options, int count, int defaultIndex);
extern void   goLog(int level, char* name, char* msg);
extern char*  goConfigGetString(char* key, char* defaultValue);
extern int    goConfigGetInt(char* key, int defaultValue);
extern int    goConfigGetBool(char* key, int defaultValue);
extern double goConfigGetDouble(char* key, double defaultValue);
extern char*  goSyscall(char* method, char* args_json);
extern void   goFreeCString(char* p);

// ---- ABI 1.5 ----
extern int             goEmitMedia(unsigned int media_type, char* payload_json);
extern int             goEmitMediaExt(unsigned int media_type, char* format, char* payload_json, unsigned int* out_event_id);
extern int             goEmitCustomEvent(char* event_name, char* payload_json);
extern int             goRegisterMediaFormat(char* format, char* mime, char* description);
extern char*           goConfigSectionJSON(void);
extern unsigned int    goConfigVersion(void);
extern int             goIsShutdownRequested(void);
extern void            goHealthCheck(unsigned int severity, char* message);
extern char*           goMediaMimeType(unsigned int media_type);
extern unsigned long long goCallToolAsync(char* tool, char* args, void* cb, void* cb_userdata);

// ---- ABI 1.6 ----
extern int             goEmitEvent(char* event_json);
extern char*           goCallTool(char* tool, char* args);
extern unsigned long long goSetAlarm(unsigned int after_seconds, char* payload_json);
extern int             goCancelAlarm(unsigned long long handle);
extern int             goConfigEnsure(char* name, char* defaults_json);
extern int             goIsPluginLoaded(char* name);
extern unsigned long long goNowMs(void);
extern void            goLogJSON(int level, char* name, char* msg_json);
extern int             goIngestEvent(char* event_json);
extern unsigned int    goConfigReload(void);
extern char*           goDataDir(void);

// ---- ABI 1.6.1: WebUI widgets ----
extern int    goWidgetEmit(char* id, char* data_json);
extern int    goWidgetNotify(char* id, int level, char* msg, unsigned int duration_ms);
extern int    goWidgetStateSet(char* id, char* state_json);
extern char*  goWidgetStateGet(char* id);

// ---- ABI 1.7: transport ----
extern int    goTransportStatus(char* name, int connected, char* detail_json);
// dynamic library loader
#if defined(_WIN32)
#include <windows.h>
static void* sc_load(const char* path) { return (void*)LoadLibraryA(path); }
static void  sc_unload(void* h) { FreeLibrary((HMODULE)h); }
static void* sc_sym(void* h, const char* name) { return (void*)GetProcAddress((HMODULE)h, name); }
#else
#include <dlfcn.h>
static void* sc_load(const char* path) { return dlopen(path, RTLD_NOW); }
static void  sc_unload(void* h) { dlclose(h); }
static void* sc_sym(void* h, const char* name) { return dlsym(h, name); }
#endif

typedef plugin_api_t* (*sc_get_api_fn)(void);

// per-call plugin context
// thread-local stack for nested plugin calls
typedef struct sc_call_ctx {
    unsigned long long     id;
    struct sc_call_ctx*    prev;
} sc_call_ctx_t;

#ifndef SC_THREAD_LOCAL
#if defined(_MSC_VER)
#define SC_THREAD_LOCAL __declspec(thread)
#else
#define SC_THREAD_LOCAL _Thread_local
#endif
#endif

static SC_THREAD_LOCAL sc_call_ctx_t* sc_ctx_tls = NULL;

static void* sc_ctx_enter(unsigned long long id) {
    sc_call_ctx_t* c = (sc_call_ctx_t*)malloc(sizeof(sc_call_ctx_t));
    if (!c) return NULL;
    c->id = id;
    c->prev = sc_ctx_tls;
    sc_ctx_tls = c;
    return (void*)c;
}

static void sc_ctx_leave(void* tok) {
    sc_call_ctx_t* c = (sc_call_ctx_t*)tok;
    if (!c) return;
    if (sc_ctx_tls == c) {
        sc_ctx_tls = c->prev;
    } else {
        // unstack node on mismatch
        sc_call_ctx_t* it = sc_ctx_tls;
        while (it != NULL) {
            if (it->prev == c) { it->prev = c->prev; break; }
            it = it->prev;
        }
    }
    free(c);
}

static unsigned long long sc_ctx_current(void) {
    return sc_ctx_tls ? sc_ctx_tls->id : 0;
}

#define SC_API_SZ_11   72u
#define SC_API_SZ_12   80u
#define SC_API_SZ_14   96u
#define SC_API_SZ_15   120u
#define SC_API_SZ_16   128u
#define SC_API_SZ_16_1 136u
#define SC_API_SZ_17   144u

static plugin_api_t* sc_get_api(void* h) {
    sc_get_api_fn f = (sc_get_api_fn)sc_sym(h, "plugin_get_api");
    if (!f) return NULL;
    return f();
}
// plugin api wrappers
static int sc_api_init(plugin_api_t* api, const core_api_t* core, const char* cfg) {
    return api->init(core, cfg);
}
static int sc_api_deinit(plugin_api_t* api) {
    if (api == NULL) return SC_ERR_INVALID_ARG;
    if (api->deinit == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->deinit();
}
static int sc_api_call_tool(plugin_api_t* api, const char* name, const char* args, char** out) {
    return api->call_tool(name, args, out);
}
static int sc_api_get_tools(plugin_api_t* api, char** out) {
    return api->get_tools(out);
}
static const plugin_info_t* sc_api_get_info(plugin_api_t* api) {
    return api->get_info();
}
static int sc_api_on_startup(plugin_api_t* api) {
    if (!api) return SC_ERR_INVALID_ARG;
    if (api->struct_size < SC_API_SZ_11) return SC_ERR_NOT_IMPLEMENTED;
    if (api->on_startup == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->on_startup();
}
static int sc_api_has_system_prompt(plugin_api_t* api) {
    if (!api) return 0;
    if (api->struct_size < SC_API_SZ_12) return 0;
    if (api->get_system_prompt == NULL) return 0;
    return 1;
}
static int sc_api_get_system_prompt(plugin_api_t* api, char** out) {
    if (!sc_api_has_system_prompt(api)) return SC_ERR_NOT_IMPLEMENTED;
    return api->get_system_prompt(out);
}
static int sc_api_get_hooks(plugin_api_t* api, const sc_hook_reg_t** out, size_t* count) {
    if (!api) return SC_ERR_INVALID_ARG;
    if (api->get_hooks == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->get_hooks(out, count);
}
static int sc_api_get_syscalls(plugin_api_t* api, const sc_syscall_reg_t** out, size_t* count) {
    if (!api) return SC_ERR_INVALID_ARG;
    if (api->struct_size < SC_API_SZ_16) return SC_ERR_NOT_IMPLEMENTED;
    if (api->get_syscalls == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->get_syscalls(out, count);
}
static const sc_syscall_reg_t* sc_syscall_at(const sc_syscall_reg_t* arr, size_t i) {
    return &arr[i];
}
static int sc_syscall_call(const sc_syscall_reg_t* reg, const char* method,
                           const char* args, char** out) {
    if (!reg || !reg->fn) return SC_ERR_INVALID_ARG;
    return reg->fn(method, args, out);
}
static int sc_hook_call(const sc_hook_reg_t* reg, const char* name, const char* payload, char** out) {
    if (!reg || !reg->fn) return SC_ERR_INVALID_ARG;
    return reg->fn(reg->userdata, name, payload, out);
}
static const sc_hook_reg_t* sc_hook_at(const sc_hook_reg_t* arr, size_t i) {
    return &arr[i];
}
static int c_invoke_callback(void* cb, void* ud, const char* name, const char* payload, char** out) {
    sc_hook_fn fn = (sc_hook_fn)cb;
    if (!fn) return SC_ERR_INVALID_ARG;
    return fn(ud, name, payload, out);
}
static int sc_api_on_shutdown(plugin_api_t* api) {
    if (!api) return SC_ERR_INVALID_ARG;
    if (api->struct_size < SC_API_SZ_15) return SC_ERR_NOT_IMPLEMENTED;
    if (api->on_shutdown == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->on_shutdown();
}
static int sc_api_health_enhanced(plugin_api_t* api, char** out) {
    if (!api) return SC_ERR_INVALID_ARG;
    if (api->struct_size < SC_API_SZ_15) return SC_ERR_NOT_IMPLEMENTED;
    if (api->health_check_enhanced == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->health_check_enhanced(out);
}

// ---- ABI 1.6.1: WebUI widgets ----

static int sc_api_get_widgets(plugin_api_t* api, const sc_widget_info_t** out, size_t* count) {
    if (!api) return SC_ERR_INVALID_ARG;
    if (api->struct_size < SC_API_SZ_16_1) return SC_ERR_NOT_IMPLEMENTED;
    if (api->get_widgets == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->get_widgets(out, count);
}
static const sc_widget_info_t* sc_widget_at(const sc_widget_info_t* arr, size_t i) {
    return &arr[i];
}
static int sc_widget_render(const sc_widget_info_t* w, char** out) {
    if (!w || !w->render_fn) return SC_ERR_NOT_IMPLEMENTED;
    return w->render_fn(out);
}
static int sc_widget_update(const sc_widget_info_t* w, char** out) {
    if (!w || !w->update_fn) return SC_ERR_NOT_IMPLEMENTED;
    return w->update_fn(out);
}
static int sc_widget_click(const sc_widget_info_t* w, const char* payload, char** out) {
    if (!w || !w->click_handler) return SC_ERR_NOT_IMPLEMENTED;
    return w->click_handler(payload, out);
}

// transport api
static const sc_transport_info_t* sc_api_get_transport(plugin_api_t* api) {
    if (!api) return NULL;
    if (api->struct_size < SC_API_SZ_17) return NULL;
    if (api->get_transport == NULL) return NULL;
    return api->get_transport();
}
static const char* sc_transport_name(const sc_transport_info_t* t) {
    return t ? t->name : NULL;
}
static const char* sc_transport_display_name(const sc_transport_info_t* t) {
    return t ? t->display_name : NULL;
}
static const char* sc_transport_description(const sc_transport_info_t* t) {
    return t ? t->description : NULL;
}
static uint32_t sc_transport_capabilities(const sc_transport_info_t* t) {
    return t ? t->capabilities : 0;
}
static uint32_t sc_transport_max_text(const sc_transport_info_t* t) {
    return t ? t->max_text_len : 0;
}
static int sc_transport_has(const sc_transport_info_t* t, int which) {
    if (!t) return 0;
    switch (which) {
        case 0: return t->send_text != NULL;
        case 1: return t->send_media != NULL;
        case 2: return t->react != NULL;
        case 3: return t->mark_read != NULL;
        case 4: return t->typing != NULL;
        case 5: return t->edit_text != NULL;
        case 6: return t->delete_message != NULL;
        case 7: return t->poll != NULL;
    }
    return 0;
}
static int sc_transport_call(const sc_transport_info_t* t, int which,
                             const char* payload, char** out) {
    if (!t) return SC_ERR_INVALID_ARG;
    switch (which) {
        case 0: if (!t->send_text) return SC_ERR_NOT_IMPLEMENTED; return t->send_text(payload, out);
        case 1: if (!t->send_media) return SC_ERR_NOT_IMPLEMENTED; return t->send_media(payload, out);
        case 2: if (!t->react) return SC_ERR_NOT_IMPLEMENTED; return t->react(payload, out);
        case 3: if (!t->mark_read) return SC_ERR_NOT_IMPLEMENTED; return t->mark_read(payload, out);
        case 4: if (!t->typing) return SC_ERR_NOT_IMPLEMENTED; return t->typing(payload, out);
        case 5: if (!t->edit_text) return SC_ERR_NOT_IMPLEMENTED; return t->edit_text(payload, out);
        case 6: if (!t->delete_message) return SC_ERR_NOT_IMPLEMENTED; return t->delete_message(payload, out);
        case 7: if (!t->poll) return SC_ERR_NOT_IMPLEMENTED; return t->poll(payload, out);
    }
    return SC_ERR_INVALID_ARG;
}
// core api structure
static void* c_alloc(size_t n) { return malloc(n); }
static void  c_free(void* p)   { free(p); }

static void c_log(int level, const char* name, const char* msg) {
    goLog(level, (char*)name, (char*)msg);
}
static int c_prompt_yes_no(const char* q, int d) {
    return goPromptYesNo((char*)q, d);
}
static int c_prompt_text(const char* q, const char* d, char* out, size_t n) {
    return goPromptText((char*)q, (char*)d, out, n);
}
static int c_prompt_select(const char* q, const char* const* opts, int n, int di) {
    return goPromptSelect((char*)q, (char**)opts, n, di);
}
static const char* c_config_get_string(const char* k, const char* d) {
    return goConfigGetString((char*)k, (char*)d);
}
static int c_config_get_int(const char* k, int d) {
    return goConfigGetInt((char*)k, d);
}
static int c_config_get_bool(const char* k, int d) {
    return goConfigGetBool((char*)k, d);
}
static double c_config_get_double(const char* k, double d) {
    return goConfigGetDouble((char*)k, d);
}
static sc_status_t c_syscall(const char* method, const char* args_json, char** out_json) {
    char* result = goSyscall((char*)method, (char*)args_json);
    if (result == NULL) {
        if (out_json) *out_json = NULL;
        return SC_ERR_INTERNAL;
    }
    int is_err = (strncmp(result, "{\"error\":", 9) == 0) ||
                 (strstr(result, "\"status\":\"error\"") != NULL);
    if (out_json) {
        size_t n = strlen(result);
        char* copy = (char*)malloc(n + 1);
        if (copy) {
            memcpy(copy, result, n + 1);
            *out_json = copy;
        } else {
            *out_json = NULL;
        }
    }
    goFreeCString(result);
    return is_err ? SC_ERR_INTERNAL : SC_OK;
}

// ---- ABI 1.5 ----
static sc_status_t c_emit_media(uint32_t media_type, const char* payload_json) {
    return goEmitMedia(media_type, (char*)payload_json);
}
static sc_status_t c_emit_media_ext(uint32_t media_type, const char* format,
                                    const char* payload_json, uint32_t* out_event_id) {
    return goEmitMediaExt(media_type, (char*)format, (char*)payload_json, out_event_id);
}
static sc_status_t c_emit_custom_event(const char* event_name, const char* payload_json) {
    return goEmitCustomEvent((char*)event_name, (char*)payload_json);
}
static sc_status_t c_register_media_format(const char* format, const char* mime,
                                           const char* description) {
    return goRegisterMediaFormat((char*)format, (char*)mime, (char*)description);
}
static const char* c_config_section_json(void) {
    return goConfigSectionJSON();
}
static uint32_t c_config_version(void) {
    return goConfigVersion();
}
static int c_is_shutdown_requested(void) {
    return goIsShutdownRequested();
}
static void c_health_check(uint32_t severity, const char* message) {
    goHealthCheck(severity, (char*)message);
}
static const char* c_media_mime_type(uint32_t media_type) {
    return goMediaMimeType(media_type);
}
static uint64_t c_call_tool_async(const char* tool, const char* args,
                                  sc_hook_fn cb, void* ud) {
    return (uint64_t)goCallToolAsync((char*)tool, (char*)args, (void*)cb, ud);
}

// ---- ABI 1.6 ----
static sc_status_t c_emit_event(const char* event_json) {
    return goEmitEvent((char*)event_json);
}
static sc_status_t c_call_tool(const char* tool, const char* args, char** out_json) {
    char* result = goCallTool((char*)tool, (char*)args);
    if (result == NULL) {
        if (out_json) *out_json = NULL;
        return SC_ERR_NOT_FOUND;
    }
    if (out_json) {
        size_t n = strlen(result);
        char* copy = (char*)malloc(n + 1);
        if (copy) {
            memcpy(copy, result, n + 1);
            *out_json = copy;
        } else {
            *out_json = NULL;
        }
    }
    goFreeCString(result);
    return SC_OK;
}
static uint64_t c_set_alarm(uint32_t after_seconds, const char* payload_json) {
    return (uint64_t)goSetAlarm(after_seconds, (char*)payload_json);
}
static sc_status_t c_cancel_alarm(uint64_t handle) {
    return goCancelAlarm((unsigned long long)handle);
}
static int c_config_ensure(const char* name, const char* defaults_json) {
    if (!name || !defaults_json) return SC_CONFIG_ERR;
    return goConfigEnsure((char*)name, (char*)defaults_json);
}
static int c_is_plugin_loaded(const char* name) {
    return goIsPluginLoaded((char*)name);
}
static uint64_t c_now_ms(void) {
    return (uint64_t)goNowMs();
}
static void c_log_json(int level, const char* name, const char* msg_json) {
    goLogJSON(level, (char*)name, (char*)msg_json);
}
static sc_status_t c_ingest_event(const char* event_json) {
    return goIngestEvent((char*)event_json);
}
static uint32_t c_config_reload(void) {
    return goConfigReload();
}
static const char* c_data_dir(void) {
    return goDataDir();
}

static const char* sc_api_transport_name(plugin_api_t* api) {
    if (!api) return NULL;
    if (api->struct_size < SC_API_SZ_16) return NULL;
    const plugin_info_t* info = api->get_info();
    if (!info) return NULL;
    return info->transport_name;
}
static int sc_api_on_config_reload(plugin_api_t* api, const char* cfg) {
    if (!api) return SC_ERR_INVALID_ARG;
    if (api->struct_size < SC_API_SZ_14) return SC_ERR_NOT_IMPLEMENTED;
    if (api->on_config_reload == NULL) return SC_ERR_NOT_IMPLEMENTED;
    return api->on_config_reload(cfg);
}
static void* sc_api_config_callback(plugin_api_t* api) {
    if (!api) return NULL;
    if (api->struct_size < SC_API_SZ_15) return NULL;
    const plugin_info_t* info = api->get_info();
    if (!info) return NULL;
    return (void*)info->on_config_version_change;
}
static void* sc_api_config_userdata(plugin_api_t* api) {
    if (!api) return NULL;
    if (api->struct_size < SC_API_SZ_16) return NULL;
    const plugin_info_t* info = api->get_info();
    if (!info) return NULL;
    return info->on_config_version_change_ud;
}
static void sc_api_config_notify(void* fn, void* ud, uint32_t ver) {
    if (!fn) return;
    ((void (*)(void*, uint32_t))fn)(ud, ver);
}

// widget api wrappers
static sc_status_t c_widget_emit(const char* id, const char* data_json) {
    return goWidgetEmit((char*)id, (char*)data_json);
}
static sc_status_t c_widget_notify(const char* id, int level, const char* msg, uint32_t duration_ms) {
    return goWidgetNotify((char*)id, level, (char*)msg, duration_ms);
}
static sc_status_t c_widget_state_set(const char* id, const char* state_json) {
    return goWidgetStateSet((char*)id, (char*)state_json);
}
static const char* c_widget_state_get(const char* id) {
    return goWidgetStateGet((char*)id);
}

// transport api wrappers
static sc_status_t c_transport_status(const char* name, int connected, const char* detail_json) {
    if (!name) return SC_ERR_INVALID_ARG;
    return goTransportStatus((char*)name, connected, (char*)detail_json);
}

static const core_api_t g_core_api = {
    SC_ABI_MAJOR,
    SC_ABI_MINOR,
    sizeof(core_api_t),
    c_alloc,
    c_free,
    c_log,
    c_emit_event,
    c_set_alarm,
    c_cancel_alarm,
    c_call_tool,
    c_syscall,
    c_prompt_yes_no,
    c_prompt_text,
    c_prompt_select,
    c_config_get_string,
    c_config_get_int,
    c_config_get_bool,
    c_config_get_double,
    c_config_ensure,
    c_is_plugin_loaded,
    c_now_ms,
    c_log_json,
    c_emit_media,
    c_config_version,
    c_is_shutdown_requested,
    c_call_tool_async,
    c_health_check,
    c_media_mime_type,
    c_emit_media_ext,
    c_emit_custom_event,
    c_register_media_format,
    c_config_section_json,
    c_ingest_event,
    c_config_reload,
    c_data_dir,
    c_widget_emit,
    c_widget_notify,
    c_widget_state_set,
    c_widget_state_get,
    {0, 0, 0, 0, 0, 0},
    c_transport_status,
    {0, 0, 0, 0, 0, 0, 0},
};

static const core_api_t* sc_core_api(void) { return &g_core_api; }
*/
import "C"

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/pelletier/go-toml/v2"

	"spermacore/core/dashboard"
	scEmbedsc "spermacore/core/embedsc"
	"spermacore/core/webui"
)

// pluginCallCtx manages per-call scoped state and allocations
type pluginCallCtx struct {
	name   string
	cfg    map[string]any
	parent *pluginCallCtx
	mu     sync.Mutex
	allocs []unsafe.Pointer
}

var (
	pluginCtxRegistry sync.Map // uint64 -> *pluginCallCtx
	pluginCtxSeq      atomic.Uint64
)

// pluginFlagThreadSafe flag
const pluginFlagThreadSafe uint32 = C.SC_PLUGIN_FLAG_THREAD_SAFE

// flag indicating plugin malloc'd out_json
const pluginFlagOwnsToolResult uint32 = C.SC_PLUGIN_FLAG_OWNS_TOOL_RESULT

var gCfg *Config

// beginPluginScope enters thread-local scope for plugin execution
func beginPluginScope(name string, section map[string]any) func() {
	ctx := &pluginCallCtx{name: name, cfg: section, parent: currentPluginCtx()}
	id := pluginCtxSeq.Add(1)
	pluginCtxRegistry.Store(id, ctx)

	runtime.LockOSThread()
	tok := C.sc_ctx_enter(C.ulonglong(id))
	return func() {
		C.sc_ctx_leave(tok)
		pluginCtxRegistry.Delete(id)
		ctx.mu.Lock()
		for _, p := range ctx.allocs {
			C.free(p)
		}
		ctx.allocs = nil
		ctx.cfg = nil
		ctx.mu.Unlock()
		runtime.UnlockOSThread()
	}
}

// currentPluginCtx retrieves context for current thread
func currentPluginCtx() *pluginCallCtx {
	id := uint64(C.sc_ctx_current())
	if id == 0 {
		return nil
	}
	v, ok := pluginCtxRegistry.Load(id)
	if !ok {
		return nil
	}
	return v.(*pluginCallCtx)
}

// trackAlloc tracks allocated string for deferred release
func (c *pluginCallCtx) trackAlloc(p *C.char) *C.char {
	if c == nil || p == nil {
		return p
	}
	c.mu.Lock()
	c.allocs = append(c.allocs, unsafe.Pointer(p))
	c.mu.Unlock()
	return p
}

// cgo //export
//
// recoverBoundary catches panics across cgo boundary
func recoverBoundary(where string) {
	if r := recover(); r != nil {
		GetLogger("core.cgo").Error("panic at %s: %v", where, r)
	}
}

//export goPromptYesNo
func goPromptYesNo(question *C.char, defaultValue C.int) C.int {
	defer recoverBoundary("goPromptYesNo")
	q := C.GoString(question)
	fmt.Printf("\033[?25h\n? %s [y/n] (Enter = %s): ", q, boolWord(int(defaultValue) != 0))
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if gDash != nil {
		fmt.Print("\033[?25l")
	}
	if line == "" {
		return defaultValue
	}
	if line == "y" || line == "yes" {
		return 1
	}
	if line == "n" || line == "no" {
		return 0
	}
	return defaultValue
}

//export goPromptText
func goPromptText(question *C.char, defaultValue *C.char, out *C.char, outsz C.size_t) C.int {
	defer recoverBoundary("goPromptText")
	q := C.GoString(question)
	def := ""
	if defaultValue != nil {
		def = C.GoString(defaultValue)
	}

	line := ""
	answered := false
	// prompt via webui if available, fallback to stdin
	if w := webui.Global(); w != nil && w.Prompter() != nil {
		if v, err := w.Prompter().Text(q, def); err == nil {
			line = v
			answered = true
		}
	}
	if !answered {
		fmt.Printf("\033[?25h\n? %s", q)
		if def != "" {
			fmt.Printf(" [%s]", def)
		}
		fmt.Print(": ")

		reader := bufio.NewReader(os.Stdin)
		line, _ = reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			line = def
		}
	}
	if out == nil || outsz == 0 {
		return -1
	}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(out)), int(outsz))
	n := copy(buf, line)
	if n >= len(buf) {
		n = len(buf) - 1
	}
	buf[n] = 0
	if gDash != nil {
		fmt.Print("\033[?25l")
	}
	return 0
}

//export goPromptSelect
func goPromptSelect(question *C.char, options **C.char, count C.int, defaultIndex C.int) C.int {
	defer recoverBoundary("goPromptSelect")
	if question == nil || count <= 0 || options == nil {
		return -1
	}
	items := unsafe.Slice(options, int(count))
	def := int(defaultIndex)
	if def < 0 || def >= int(count) {
		def = 0
	}

	fmt.Printf("\033[?25h\n? %s\n", C.GoString(question))
	for i, option := range items {
		label := ""
		if option != nil {
			label = C.GoString(option)
		}
		fmt.Printf("  %d) %s%s\n", i+1, label, map[bool]string{true: " (default)", false: ""}[i == def])
	}
	fmt.Printf("Select [default %d]: ", def+1)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if gDash != nil {
		fmt.Print("\033[?25l")
	}
	if err != nil && len(line) == 0 {
		return -1
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return C.int(def)
	}
	choice, err := strconv.Atoi(line)
	if err != nil || choice < 1 || choice > int(count) {
		return -1
	}
	return C.int(choice - 1)
}

//export goLog
func goLog(level C.int, name *C.char, msg *C.char) {
	defer recoverBoundary("goLog")
	n := "addon"
	if name != nil {
		n = C.GoString(name)
	}
	m := ""
	if msg != nil {
		m = C.GoString(msg)
	}
	GetLogger(n).log(Level(int(level)), "%s", m)
}

//export goConfigGetString
func goConfigGetString(key *C.char, defaultValue *C.char) *C.char {
	defer recoverBoundary("goConfigGetString")

	def := ""
	if defaultValue != nil {
		def = C.GoString(defaultValue)
	}
	ctx := currentPluginCtx()
	if ctx == nil {
		// outside plugin scope return default value without allocation
		if defaultValue != nil {
			return defaultValue
		}
		return C.CString("")
	}
	ctx.mu.Lock()
	cfg := ctx.cfg
	ctx.mu.Unlock()
	if cfg != nil {
		k := C.GoString(key)
		if v, ok := cfg[k]; ok {
			if s, ok := v.(string); ok {
				return ctx.trackAlloc(C.CString(s))
			}
		}
	}
	return ctx.trackAlloc(C.CString(def))
}

//export goConfigGetInt
func goConfigGetInt(key *C.char, defaultValue C.int) C.int {
	defer recoverBoundary("goConfigGetInt")
	ctx := currentPluginCtx()
	if ctx == nil {
		return defaultValue
	}
	ctx.mu.Lock()
	cfg := ctx.cfg
	ctx.mu.Unlock()
	if cfg == nil {
		return defaultValue
	}
	k := C.GoString(key)
	v, ok := cfg[k]
	if !ok {
		return defaultValue
	}
	switch n := v.(type) {
	case int64:
		return C.int(n)
	case int:
		return C.int(n)
	case float64:
		return C.int(n)
	}
	return defaultValue
}

//export goConfigGetBool
func goConfigGetBool(key *C.char, defaultValue C.int) C.int {
	defer recoverBoundary("goConfigGetBool")
	ctx := currentPluginCtx()
	if ctx == nil {
		return defaultValue
	}
	ctx.mu.Lock()
	cfg := ctx.cfg
	ctx.mu.Unlock()
	if cfg == nil {
		return defaultValue
	}
	k := C.GoString(key)
	v, ok := cfg[k]
	if !ok {
		return defaultValue
	}
	if b, ok := v.(bool); ok {
		if b {
			return 1
		}
		return 0
	}
	return defaultValue
}

//export goConfigGetDouble
func goConfigGetDouble(key *C.char, defaultValue C.double) C.double {
	defer recoverBoundary("goConfigGetDouble")
	ctx := currentPluginCtx()
	if ctx == nil {
		return defaultValue
	}
	ctx.mu.Lock()
	cfg := ctx.cfg
	ctx.mu.Unlock()
	if cfg == nil {
		return defaultValue
	}
	k := C.GoString(key)
	v, ok := cfg[k]
	if !ok {
		return defaultValue
	}
	switch n := v.(type) {
	case float64:
		return C.double(n)
	case int64:
		return C.double(n)
	case int:
		return C.double(n)
	}
	return defaultValue
}

//export goSyscall
func goSyscall(method *C.char, argsJSON *C.char) *C.char {
	defer recoverBoundary("goSyscall")
	m := C.GoString(method)
	a := C.GoString(argsJSON)
	var out string
	rc := syscallDispatch(m, a, &out)
	if rc != 0 {
		if strings.TrimSpace(out) == "" {
			switch rc {
			case -2:
				out = `{"error":"invalid args"}`
			case -3:
				out = `{"error":"method not found"}`
			case -5:
				out = `{"error":"internal error"}`
			default:
				out = fmt.Sprintf(`{"error":"rc=%d"}`, rc)
			}
		} else {
			out = fmt.Sprintf(`{"error":"rc=%d","detail":%s}`, rc, out)
		}
	}
	return C.CString(out)
}

//export goFreeCString
func goFreeCString(p *C.char) {
	defer recoverBoundary("goFreeCString")
	if p != nil {
		C.free(unsafe.Pointer(p))
	}
}

// ---- ABI 1.5 exports ----

//export goEmitMedia
func goEmitMedia(mediaType C.uint, payloadJSON *C.char) C.int {
	defer recoverBoundary("goEmitMedia")
	return C.int(emitMedia(uint32(mediaType), cstr(payloadJSON)))
}

//export goEmitMediaExt
func goEmitMediaExt(mediaType C.uint, format *C.char, payloadJSON *C.char, outEventID *C.uint) C.int {
	defer recoverBoundary("goEmitMediaExt")
	formatStr := ""
	if format != nil {
		formatStr = C.GoString(format)
	}
	id, rc := emitMediaExt(uint32(mediaType), formatStr, cstr(payloadJSON), nil)
	if outEventID != nil {
		*outEventID = C.uint(id)
	}
	return C.int(rc)
}

//export goEmitCustomEvent
func goEmitCustomEvent(eventName *C.char, payloadJSON *C.char) C.int {
	defer recoverBoundary("goEmitCustomEvent")
	return C.int(emitCustomEvent(cstr(eventName), cstr(payloadJSON)))
}

//export goRegisterMediaFormat
func goRegisterMediaFormat(format *C.char, mime *C.char, description *C.char) C.int {
	defer recoverBoundary("goRegisterMediaFormat")
	return C.int(registerMediaFormat(cstr(format), cstr(mime), cstr(description)))
}

//export goConfigSectionJSON
func goConfigSectionJSON() *C.char {
	defer recoverBoundary("goConfigSectionJSON")
	ctx := currentPluginCtx()
	if ctx == nil {
		return nil
	}
	ctx.mu.Lock()
	cfg := ctx.cfg
	ctx.mu.Unlock()
	if cfg == nil {
		return nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	return ctx.trackAlloc(C.CString(string(b)))
}

//export goConfigVersion
func goConfigVersion() C.uint {
	defer recoverBoundary("goConfigVersion")
	return C.uint(getConfigVersion())
}

//export goIsShutdownRequested
func goIsShutdownRequested() C.int {
	defer recoverBoundary("goIsShutdownRequested")
	return C.int(isShutdownRequested())
}

//export goHealthCheck
func goHealthCheck(severity C.uint, message *C.char) {
	defer recoverBoundary("goHealthCheck")
	healthCheck(uint32(severity), cstr(message))
}

//export goMediaMimeType
func goMediaMimeType(mediaType C.uint) *C.char {
	defer recoverBoundary("goMediaMimeType")
	m := mediaMimeType(uint32(mediaType))
	if m == "" {
		return nil
	}
	return C.CString(m)
}

//export goCallToolAsync
func goCallToolAsync(tool *C.char, args *C.char, cb unsafe.Pointer, cbUserdata unsafe.Pointer) C.ulonglong {
	defer recoverBoundary("goCallToolAsync")
	handle := callToolAsync(cstr(tool), cstr(args), cb, cbUserdata)
	return C.ulonglong(handle)
}

// ---- ABI 1.6 exports ----

//export goEmitEvent
func goEmitEvent(eventJSON *C.char) C.int {
	defer recoverBoundary("goEmitEvent")
	return C.int(emitEvent(cstr(eventJSON)))
}

//export goCallTool
func goCallTool(tool *C.char, args *C.char) *C.char {
	defer recoverBoundary("goCallTool")
	var out string
	result, err := callTool(cstr(tool), cstr(args))
	if err != nil {
		out = fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error())
	} else if result == "" {
		out = "null"
	} else {
		out = result
	}
	return C.CString(out)
}

//export goSetAlarm
func goSetAlarm(afterSeconds C.uint, payloadJSON *C.char) C.ulonglong {
	defer recoverBoundary("goSetAlarm")
	return C.ulonglong(setAlarm(uint32(afterSeconds), cstr(payloadJSON)))
}

//export goCancelAlarm
func goCancelAlarm(handle C.ulonglong) C.int {
	defer recoverBoundary("goCancelAlarm")
	return C.int(cancelAlarm(uint64(handle)))
}

//export goIsPluginLoaded
func goIsPluginLoaded(name *C.char) C.int {
	defer recoverBoundary("goIsPluginLoaded")
	return C.int(isPluginLoaded(cstr(name)))
}

//export goNowMs
func goNowMs() C.ulonglong {
	defer recoverBoundary("goNowMs")
	return C.ulonglong(nowMs())
}

//export goLogJSON
func goLogJSON(level C.int, name *C.char, msgJSON *C.char) {
	defer recoverBoundary("goLogJSON")
	n := "addon"
	if name != nil {
		n = C.GoString(name)
	}
	GetLogger(n).log(Level(int(level)), "%s", cstr(msgJSON))
}

//export goConfigEnsure
func goConfigEnsure(name *C.char, defaultsJSON *C.char) C.int {
	defer recoverBoundary("goConfigEnsure")
	if gCfg == nil {
		return -1
	}
	// require active plugin context for config ensure
	if currentPluginCtx() == nil {
		return -1
	}
	var defaults map[string]any
	if err := json.Unmarshal([]byte(cstr(defaultsJSON)), &defaults); err != nil {
		return -1
	}
	created, added, err := gCfg.EnsureSection(cstr(name), defaults)
	if err != nil {
		return -1
	}
	if created {
		return 1
	}
	if added {
		return 2
	}
	return 0
}

//export goIngestEvent
func goIngestEvent(eventJSON *C.char) C.int {
	defer recoverBoundary("goIngestEvent")
	return C.int(ingestEvent(cstr(eventJSON)))
}

// ABI 1.6.1 exports: WebUI widgets
//
//export goWidgetEmit
func goWidgetEmit(id *C.char, dataJSON *C.char) C.int {
	defer recoverBoundary("goWidgetEmit")
	return C.int(widgetEmit(cstr(id), cstr(dataJSON)))
}

//export goWidgetNotify
func goWidgetNotify(id *C.char, level C.int, msg *C.char, duration C.uint) C.int {
	defer recoverBoundary("goWidgetNotify")
	return C.int(widgetNotify(cstr(id), int(level), cstr(msg), uint32(duration)))
}

//export goWidgetStateSet
func goWidgetStateSet(id *C.char, stateJSON *C.char) C.int {
	defer recoverBoundary("goWidgetStateSet")
	return C.int(widgetStateSet(cstr(id), cstr(stateJSON)))
}

//export goWidgetStateGet
func goWidgetStateGet(id *C.char) *C.char {
	defer recoverBoundary("goWidgetStateGet")
	s := widgetStateGet(cstr(id))
	if s == "" {
		return nil
	}
	return C.CString(s)
}

// export transport status to dashboard
//
//export goTransportStatus
func goTransportStatus(name *C.char, connected C.int, detailJSON *C.char) C.int {
	defer recoverBoundary("goTransportStatus")
	transportStatus(cstr(name), int(connected) != 0, cstr(detailJSON))
	return C.int(0)
}

// Config reload / data_dir / watch

// atomic snapshot of plugin configuration sections
var pluginSections atomic.Pointer[map[string]map[string]any]

func setPluginSections(m map[string]map[string]any) {
	if m == nil {
		m = map[string]map[string]any{}
	}
	pluginSections.Store(&m)
}

func currentPluginSections() map[string]map[string]any {
	if p := pluginSections.Load(); p != nil {
		return *p
	}
	if gCfg != nil {
		return gCfg.PluginSections
	}
	return nil
}

func reloadConfig() uint32 {
	if gCfg == nil {
		return getConfigVersion()
	}
	log := GetLogger("core.config")

	// full config reload via LoadConfigRaw
	newCfg, err := LoadConfigRaw(gCfg.SourcePath)
	if err != nil {
		log.Warn("reload error: %v (retaining previous configuration)", err)
		return getConfigVersion()
	}
	sections := newCfg.PluginSections
	setPluginSections(sections)
	ver := configVersionN.Add(1)

	busMu.RLock()
	addons := busAddons
	busMu.RUnlock()

	for _, a := range addons {
		if a == nil {
			continue
		}
		section, _ := sections[a.Name]
		if section == nil {
			section = map[string]any{}
		}
		cfgJSON := "{}"
		if b, e := json.Marshal(section); e == nil {
			cfgJSON = string(b)
		}
		pluginCtxRelease := beginPluginScope(a.Name, section)
		rc := a.OnConfigReload(cfgJSON)
		pluginCtxRelease()
		if rc != 0 && rc != -7 {
			log.Warn("plugin %s: on_config_reload error rc=%d", a.Name, rc)
		}
		a.NotifyConfigVersion(ver)
	}

	// apply runtime settings
	for _, field := range applyRuntimeConfig(gCfg, newCfg, log) {
		log.Warn("hot reload: option %s changed - restart required", field)
	}
	return ver
}

func dataDir() string {
	if gCfg == nil {
		return ""
	}
	ctx := currentPluginCtx()
	if ctx == nil || ctx.name == "" {
		return ""
	}
	dir := filepath.Join(gCfg.Core.DataDir, ctx.name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		GetLogger("core.config").Warn("data dir: mkdir %s: %v", dir, err)
	}
	return dir
}

func startConfigWatch(log *Logger) {
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		lastMod := int64(-1)
		for range ticker.C {
			if gCfg == nil {
				continue
			}
			info, err := os.Stat(gCfg.SourcePath)
			if err != nil {
				continue
			}
			m := info.ModTime().UnixNano()
			if lastMod >= 0 && m != lastMod {
				log.Info("config file changed, reloading settings...")
				ver := reloadConfig()
				log.Info("config reloaded successfully, version=%d", int(ver))
			}
			lastMod = m
		}
	}()
}

//export goConfigReload
func goConfigReload() C.uint {
	defer recoverBoundary("goConfigReload")
	return C.uint(reloadConfig())
}

//export goDataDir
func goDataDir() *C.char {
	defer recoverBoundary("goDataDir")
	d := dataDir()
	if d == "" {
		return nil
	}
	// caller owns allocated string via core->free
	return C.CString(d)
}

func cstr(p *C.char) string {
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

var (
	gGlobalProviderMu sync.RWMutex
	gGlobalProvider   LLMProvider
	gGlobalCfgMu      sync.RWMutex
	gGlobalCfg        *Config
)

func SetGlobalProvider(p LLMProvider) {
	gGlobalProviderMu.Lock()
	gGlobalProvider = p
	gGlobalProviderMu.Unlock()
}

func GetGlobalProvider() LLMProvider {
	gGlobalProviderMu.RLock()
	defer gGlobalProviderMu.RUnlock()
	return gGlobalProvider
}

func SetGlobalConfig(c *Config) {
	gGlobalCfgMu.Lock()
	gGlobalCfg = c
	gGlobalCfgMu.Unlock()
}

func GetGlobalConfig() *Config {
	gGlobalCfgMu.RLock()
	defer gGlobalCfgMu.RUnlock()
	return gGlobalCfg
}

func getRecentChatTranscript(chatID int64) string {
	hist := gHistory.Get(chatID)
	var b strings.Builder
	for _, m := range hist {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// syscallDispatch
func syscallDispatch(method, argsJSON string, outJSON *string) int {
	switch method {
	case "webui.auth_event":
		if w := webui.Global(); w != nil {
			w.AuthEvent(argsJSON)
		}
		var parsed struct {
			Data struct {
				Username  string `json:"username"`
				FirstName string `json:"first_name"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(argsJSON), &parsed) == nil {
			if parsed.Data.Username != "" {
				RegisterBotUsername(parsed.Data.Username)
			}
			if parsed.Data.FirstName != "" {
				RegisterBotName(parsed.Data.FirstName)
			}
		}
		if outJSON != nil {
			*outJSON = `{"status":"ok"}`
		}
		return 0

	case "embedsc.remember":
		var req struct {
			ChatID int64  `json:"chat_id"`
			Role   string `json:"role"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &req)
		if err := scEmbedsc.Remember(req.ChatID, req.Role, req.Text); err != nil {
			if outJSON != nil {
				*outJSON = fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error())
			}
			return 0
		}
		if outJSON != nil {
			*outJSON = `{"status":"ok"}`
		}
		return 0

	case "embedsc.recall":
		var req struct {
			ChatID int64  `json:"chat_id"`
			Query  string `json:"query"`
			TopK   int    `json:"top_k"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &req)
		hits, err := scEmbedsc.Recall(req.ChatID, req.Query, req.TopK)
		if err != nil {
			if outJSON != nil {
				*outJSON = fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error())
			}
			return 0
		}
		if outJSON != nil {
			b, _ := json.Marshal(map[string]any{"hits": hits})
			*outJSON = string(b)
		}
		return 0

	case "embedsc.stats":
		var req struct {
			ChatID int64 `json:"chat_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &req)
		n, m, err := scEmbedsc.Stats(req.ChatID)
		if err != nil {
			if outJSON != nil {
				*outJSON = fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error())
			}
			return 0
		}
		hnswStats, _ := scEmbedsc.IndexStats(req.ChatID)
		if outJSON != nil {
			*outJSON = fmt.Sprintf(`{"count":%d,"model":%q,"index":"hnsw","hnsw":{"count":%d,"max_level":%d,"entry_point":%d,"m":%d,"ef_search":%d}}`,
				n, m, hnswStats.Count, hnswStats.MaxLevel, hnswStats.EntryPoint, hnswStats.M, hnswStats.EfSearch)
		}
		return 0

	case "llm.chat":
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &req); err != nil {
			return -2
		}
		prov := GetGlobalProvider()
		if prov == nil {
			if outJSON != nil {
				*outJSON = `{"status":"error","reason":"llm provider not available"}`
			}
			return 0
		}
		cfg := GetGlobalConfig()
		model := req.Model
		if model == "" && cfg != nil {
			model = cfg.ModelNameFor("chat")
		}
		var chatMsgs []ChatMessage
		for _, m := range req.Messages {
			chatMsgs = append(chatMsgs, ChatMessage{Role: m.Role, Content: m.Content})
		}
		resp, err := prov.Chat(context.Background(), model, chatMsgs, nil)
		if err != nil {
			if outJSON != nil {
				*outJSON = fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error())
			}
			return 0
		}
		if outJSON != nil {
			b, _ := json.Marshal(map[string]any{
				"status":  "ok",
				"content": resp.Content,
				"model":   model,
			})
			*outJSON = string(b)
		}
		return 0

	case "transport.send":
		var req struct {
			Transport string `json:"transport"`
			ChatID    int64  `json:"chat_id"`
			Text      string `json:"text"`
			TopicID   int    `json:"topic_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &req); err != nil {
			return -2
		}
		ev := transportEvent{
			Transport: req.Transport,
			ChatID:    req.ChatID,
			TopicID:   req.TopicID,
		}
		sentID, err := sendTextGetID(ev, req.Text, req.TopicID)
		if err != nil {
			if outJSON != nil {
				*outJSON = fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error())
			}
			return 0
		}
		if outJSON != nil {
			*outJSON = fmt.Sprintf(`{"status":"ok","message_id":%d}`, sentID)
		}
		return 0

	case "transport.list":
		busMu.RLock()
		var list []string
		for t := range transports {
			list = append(list, t)
		}
		busMu.RUnlock()
		b, _ := json.Marshal(map[string]any{"transports": list})
		if outJSON != nil {
			*outJSON = string(b)
		}
		return 0

	case "context.recent":
		var req struct {
			ChatID int64 `json:"chat_id"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &req)
		transcript := getRecentChatTranscript(req.ChatID)
		b, _ := json.Marshal(map[string]any{"transcript": transcript})
		if outJSON != nil {
			*outJSON = string(b)
		}
		return 0

	case "users.list":
		dataDir := "./data"
		if cfg := GetGlobalConfig(); cfg != nil && cfg.Core.DataDir != "" {
			dataDir = cfg.Core.DataDir
		}
		users := getKnownUsernames(dataDir)
		b, _ := json.Marshal(map[string]any{"users": users})
		if outJSON != nil {
			*outJSON = string(b)
		}
		return 0
	}
	if result, handled := pluginSyscall(method, argsJSON); handled != 0 {
		if outJSON != nil {
			*outJSON = result
		}
		return 0
	}
	return -3
}

// LoadedAddon
type LoadedAddon struct {
	Name             string
	Version          string
	Desc             string
	LLMDescription   string
	HumanDescription string
	Path             string

	handle unsafe.Pointer
	api    *C.plugin_api_t

	hooksPtr *C.sc_hook_reg_t
	hooksLen C.size_t

	syscallsPtr *C.sc_syscall_reg_t
	syscallsLen C.size_t

	TransportName string

	// plugin flags
	flags uint32

	// mutex for non-threadsafe plugins
	callMu sync.Mutex

	// transport descriptor if provided
	transportPtr *C.sc_transport_info_t

	widgetsPtr *C.sc_widget_info_t
	widgetsLen C.size_t
}

func (a *LoadedAddon) CallTool(name, args string) (string, error) {
	if a == nil || a.api == nil {
		return "", fmt.Errorf("call_tool: addon is not loaded")
	}
	defer a.enterCall()()
	// bind thread-local context for call duration
	defer a.beginScope()()

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	cArgs := C.CString(args)
	defer C.free(unsafe.Pointer(cArgs))
	var out *C.char
	rc := C.sc_api_call_tool(a.api, cName, cArgs, &out)
	if rc != 0 {
		return "", fmt.Errorf("call_tool %q rc=%d", name, int(rc))
	}
	if out == nil {
		return "", nil
	}
	res := C.GoString(out)
	a.freePluginOut(out)
	return res, nil
}

// reports whether named plugin is already in current call stack
func inPluginScope(name string) bool {
	for c := currentPluginCtx(); c != nil; c = c.parent {
		if c.name == name {
			return true
		}
	}
	return false
}

// free out_json if plugin transferred ownership
func (a *LoadedAddon) freePluginOut(p *C.char) {
	if p != nil && a.flags&pluginFlagOwnsToolResult != 0 {
		C.free(unsafe.Pointer(p))
	}
}

// serialize calls if plugin is not thread-safe
func (a *LoadedAddon) enterCall() func() {
	if a == nil || a.flags&pluginFlagThreadSafe != 0 {
		return func() {}
	}
	if inPluginScope(a.Name) {
		return func() {}
	}
	a.callMu.Lock()
	return a.callMu.Unlock
}

// scope binding for config and data dir access
func (a *LoadedAddon) beginScope() func() {
	if a == nil {
		return func() {}
	}
	return beginPluginScope(a.Name, a.configSection())
}

// returns active plugin configuration section
func (a *LoadedAddon) configSection() map[string]any {
	sections := currentPluginSections()
	if s, ok := sections[a.Name]; ok && s != nil {
		return s
	}
	return map[string]any{}
}

func (a *LoadedAddon) GetTools() ([]map[string]any, error) {
	if a == nil || a.api == nil {
		return nil, nil
	}
	defer a.enterCall()()
	defer a.beginScope()()
	var out *C.char
	rc := C.sc_api_get_tools(a.api, &out)
	if rc != 0 || out == nil {
		return nil, nil
	}
	raw := C.GoString(out)
	a.freePluginOut(out)
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, fmt.Errorf("addon %s: bad tools json: %w", a.Name, err)
	}
	return arr, nil
}

func (a *LoadedAddon) OnStartup() int {
	if a == nil || a.api == nil {
		return 0
	}
	// run startup hook within plugin scope
	defer a.enterCall()()
	defer a.beginScope()()
	return int(C.sc_api_on_startup(a.api))
}

func (a *LoadedAddon) OnConfigReload(cfgJSON string) int {
	if a == nil || a.api == nil {
		return 0
	}
	defer a.enterCall()()
	cCfg := C.CString(cfgJSON)
	defer C.free(unsafe.Pointer(cCfg))
	return int(C.sc_api_on_config_reload(a.api, cCfg))
}

func (a *LoadedAddon) NotifyConfigVersion(ver uint32) {
	if a == nil || a.api == nil {
		return
	}
	defer a.enterCall()()
	defer a.beginScope()()
	fn := C.sc_api_config_callback(a.api)
	if fn == nil {
		return
	}
	ud := C.sc_api_config_userdata(a.api)
	C.sc_api_config_notify(fn, ud, C.uint(ver))
}

func (a *LoadedAddon) GetSystemPrompt() (string, error) {
	if a == nil || a.api == nil {
		return "", nil
	}
	defer a.enterCall()()
	defer a.beginScope()()
	var out *C.char
	rc := C.sc_api_get_system_prompt(a.api, &out)
	if rc == -7 {
		return "", nil
	}
	if rc != 0 {
		return "", fmt.Errorf("get_system_prompt rc=%d", int(rc))
	}
	if out == nil {
		return "", nil
	}
	res := C.GoString(out)
	a.freePluginOut(out)
	return res, nil
}

func (a *LoadedAddon) deinit() int {
	if a == nil || a.api == nil {
		return 0
	}
	defer a.enterCall()()
	defer a.beginScope()()
	return int(C.sc_api_deinit(a.api))
}

func (a *LoadedAddon) Unload() {
	if a.handle != nil {
		a.deinit()
		C.sc_unload(a.handle)
		a.handle = nil
	}
}

func (a *LoadedAddon) onShutdown() int {
	if a == nil || a.api == nil {
		return 0
	}
	defer a.enterCall()()
	defer a.beginScope()()
	return int(C.sc_api_on_shutdown(a.api))
}

func (a *LoadedAddon) HealthCheckEnhanced() string {
	if a == nil || a.api == nil {
		return ""
	}
	defer a.enterCall()()
	defer a.beginScope()()
	var out *C.char
	rc := C.sc_api_health_enhanced(a.api, &out)
	if rc != 0 || out == nil {
		return ""
	}
	res := C.GoString(out)
	a.freePluginOut(out)
	return res
}

func (a *LoadedAddon) HookNames() []string {
	if a == nil || a.hooksPtr == nil {
		return nil
	}
	var names []string
	for i := C.size_t(0); i < a.hooksLen; i++ {
		reg := C.sc_hook_at(a.hooksPtr, i)
		if reg == nil || reg.name == nil {
			continue
		}
		names = append(names, C.GoString(reg.name))
	}
	return names
}

// widget methods
type WidgetInfo struct {
	ID       string
	Title    string
	Icon     string
	DefaultW int
	DefaultH int

	owner *LoadedAddon
	ptr   *C.sc_widget_info_t
}

func (a *LoadedAddon) Widgets() []*WidgetInfo {
	if a == nil || a.widgetsPtr == nil || a.widgetsLen == 0 {
		return nil
	}
	out := make([]*WidgetInfo, 0, int(a.widgetsLen))
	for i := C.size_t(0); i < a.widgetsLen; i++ {
		w := C.sc_widget_at(a.widgetsPtr, i)
		if w == nil || w.id == nil {
			continue
		}
		info := &WidgetInfo{
			ID:       C.GoString(w.id),
			DefaultW: int(w.default_w),
			DefaultH: int(w.default_h),
			owner:    a,
			ptr:      w,
		}
		if w.title != nil {
			info.Title = C.GoString(w.title)
		}
		if w.icon != nil {
			info.Icon = C.GoString(w.icon)
		}
		out = append(out, info)
	}
	return out
}

// render widget html
func (w *WidgetInfo) Render() string {
	if w == nil || w.ptr == nil || w.owner == nil {
		return ""
	}
	defer w.owner.enterCall()()
	defer w.owner.beginScope()()
	var out *C.char
	rc := C.sc_widget_render(w.ptr, &out)
	if rc != 0 || out == nil {
		return ""
	}
	res := C.GoString(out)
	w.owner.freePluginOut(out)
	return res
}

// update widget data
func (w *WidgetInfo) Update() string {
	if w == nil || w.ptr == nil || w.owner == nil {
		return ""
	}
	defer w.owner.enterCall()()
	defer w.owner.beginScope()()
	var out *C.char
	rc := C.sc_widget_update(w.ptr, &out)
	if rc != 0 || out == nil {
		return ""
	}
	res := C.GoString(out)
	w.owner.freePluginOut(out)
	return res
}

// click widget handler
func (w *WidgetInfo) Click(payloadJSON string) string {
	if w == nil || w.ptr == nil || w.owner == nil {
		return ""
	}
	defer w.owner.enterCall()()
	defer w.owner.beginScope()()
	cPayload := C.CString(payloadJSON)
	defer C.free(unsafe.Pointer(cPayload))
	var out *C.char
	rc := C.sc_widget_click(w.ptr, cPayload, &out)
	if rc != 0 || out == nil {
		return ""
	}
	res := C.GoString(out)
	w.owner.freePluginOut(out)
	return res
}

// transport methods

// resolved transport name
func (a *LoadedAddon) TransportNameResolved() string {
	if a == nil {
		return ""
	}
	return a.TransportName
}

// human readable transport name
func (a *LoadedAddon) TransportDisplayName() string {
	if a == nil || a.transportPtr == nil {
		return ""
	}
	if s := C.sc_transport_display_name(a.transportPtr); s != nil {
		return C.GoString(s)
	}
	return ""
}

// transport description
func (a *LoadedAddon) TransportDescription() string {
	if a == nil || a.transportPtr == nil {
		return ""
	}
	if s := C.sc_transport_description(a.transportPtr); s != nil {
		return C.GoString(s)
	}
	return ""
}

// bitmask of supported transport capabilities
func (a *LoadedAddon) TransportCapabilities() uint32 {
	if a == nil || a.transportPtr == nil {
		return 0
	}
	return uint32(C.sc_transport_capabilities(a.transportPtr))
}

// maximum text length limit
func (a *LoadedAddon) TransportMaxTextLen() int {
	if a == nil || a.transportPtr == nil {
		return 0
	}
	return int(C.sc_transport_max_text(a.transportPtr))
}

// reports whether native transport is provided
func (a *LoadedAddon) HasNativeTransport() bool {
	return a != nil && a.transportPtr != nil
}

// check capability flag
func (a *LoadedAddon) TransportSupports(which int) bool {
	if a == nil || a.transportPtr == nil {
		return false
	}
	return C.sc_transport_has(a.transportPtr, C.int(which)) != 0
}

// dispatch transport method
func (a *LoadedAddon) transportCall(which int, payloadJSON string) (string, error) {
	if a == nil || a.transportPtr == nil {
		return "", fmt.Errorf("transport: addon is not loaded")
	}
	defer a.enterCall()()
	defer a.beginScope()()
	cPayload := C.CString(payloadJSON)
	defer C.free(unsafe.Pointer(cPayload))
	var out *C.char
	rc := C.sc_transport_call(a.transportPtr, C.int(which), cPayload, &out)
	var result string
	if out != nil {
		result = C.GoString(out)
		C.c_free(unsafe.Pointer(out))
	}
	if rc != 0 {
		if result != "" {
			return result, fmt.Errorf("transport %s: rc=%d (%s)", a.TransportName, int(rc), result)
		}
		return result, fmt.Errorf("transport %s: rc=%d", a.TransportName, int(rc))
	}
	return result, nil
}

func (a *LoadedAddon) TransportSendText(payloadJSON string) (string, error) {
	return a.transportCall(transportCallText, payloadJSON)
}
func (a *LoadedAddon) TransportSendMedia(payloadJSON string) (string, error) {
	return a.transportCall(transportCallMedia, payloadJSON)
}
func (a *LoadedAddon) TransportReact(payloadJSON string) (string, error) {
	return a.transportCall(transportCallReact, payloadJSON)
}
func (a *LoadedAddon) TransportMarkRead(payloadJSON string) (string, error) {
	return a.transportCall(transportCallRead, payloadJSON)
}
func (a *LoadedAddon) TransportTyping(payloadJSON string) (string, error) {
	return a.transportCall(transportCallTyping, payloadJSON)
}
func (a *LoadedAddon) TransportEdit(payloadJSON string) (string, error) {
	return a.transportCall(transportCallEdit, payloadJSON)
}
func (a *LoadedAddon) TransportDelete(payloadJSON string) (string, error) {
	return a.transportCall(transportCallDelete, payloadJSON)
}
func (a *LoadedAddon) TransportPoll(payloadJSON string) (string, error) {
	return a.transportCall(transportCallPoll, payloadJSON)
}

// loadOneAddon / loadAddons
type addonAbout struct {
	Name             string `toml:"name"`
	LLMDescription   string `toml:"llm_description"`
	HumanDescription string `toml:"human_description"`
}

// validate plugin abi compatibility
func checkPluginABI(major, minor, structSize uint32) (warning string, err error) {
	coreMajor := uint32(C.SC_ABI_MAJOR)
	coreMinor := uint32(C.SC_ABI_MINOR)

	if major != coreMajor {
		return "", fmt.Errorf("incompatible plugin ABI %d.%d: core ABI is %d.%d (major mismatch)",
			major, minor, coreMajor, coreMinor)
	}
	if structSize < uint32(C.SC_API_SZ_11) {
		return "", fmt.Errorf("plugin_api_t struct_size=%d is too small (minimum %d)",
			structSize, uint32(C.SC_API_SZ_11))
	}
	switch {
	case minor > coreMinor:
		return fmt.Sprintf("plugin ABI %d.%d is newer than core %d.%d; unknown fields ignored",
			major, minor, coreMajor, coreMinor), nil
	case minor < coreMinor:
		return fmt.Sprintf("plugin ABI %d.%d is older than core %d.%d; optional fields guarded by struct_size",
			major, minor, coreMajor, coreMinor), nil
	}
	return "", nil
}

func readAddonAbout(dir string) (addonAbout, error) {
	data, err := os.ReadFile(filepath.Join(dir, "about.toml"))
	if err != nil {
		return addonAbout{}, err
	}
	var about addonAbout
	if err := toml.Unmarshal(data, &about); err != nil {
		return addonAbout{}, err
	}
	if strings.TrimSpace(about.Name) == "" || strings.TrimSpace(about.LLMDescription) == "" || strings.TrimSpace(about.HumanDescription) == "" {
		return addonAbout{}, fmt.Errorf("about.toml requires name, llm_description and human_description")
	}
	return about, nil
}

// smoke test plugin library loading
func checkPluginLoad(path string) error {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	handle := C.sc_load(cPath)
	if handle == nil {
		return fmt.Errorf("failed to load %s", path)
	}
	defer C.sc_unload(handle)

	api := C.sc_get_api(handle)
	if api == nil {
		return fmt.Errorf("plugin_get_api missing in %s", path)
	}
	if _, err := checkPluginABI(uint32(api.abi_major), uint32(api.abi_minor), uint32(api.struct_size)); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func loadOneAddon(name, dir, rootDir string, cfg *Config, log *Logger) (*LoadedAddon, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("module directory not found: %s", dir)
	}

	about, err := readAddonAbout(dir)
	if err != nil {
		return nil, fmt.Errorf("addon %s: %w", name, err)
	}
	artifact := findBuiltArtifact(dir, name)
	if artifact != "" && artifactIsStale(artifact, dir, rootDir) {
		log.Warn("plugin %s: binary %s is older than sources, rebuilding", name, artifact)
		if err := buildModule(dir, name, rootDir, log); err != nil {
			if !cfg.Plugins.AllowStale {
				return nil, fmt.Errorf("error rebuilding stale module: %w", err)
			}
			log.Error("plugin %s: rebuild error, using current binary (plugins.allow_stale=true): %v", name, err)
		} else if rebuilt := findBuiltArtifact(dir, name); rebuilt != "" {
			artifact = rebuilt
		}
	}
	if artifact == "" {
		log.Info("plugin %s: binary not found, building", name)
		if err := buildModule(dir, name, rootDir, log); err != nil {
			return nil, fmt.Errorf("build error: %w", err)
		}
		artifact = findBuiltArtifact(dir, name)
		if artifact == "" {
			return nil, fmt.Errorf("binary not found after successful build")
		}
	}

	cPath := C.CString(artifact)
	defer C.free(unsafe.Pointer(cPath))
	handle := C.sc_load(cPath)
	if handle == nil {
		return nil, fmt.Errorf("cannot load shared library %s", artifact)
	}

	api := C.sc_get_api(handle)
	if api == nil {
		C.sc_unload(handle)
		return nil, fmt.Errorf("symbol plugin_get_api not found in %s", artifact)
	}
	if warn, err := checkPluginABI(uint32(api.abi_major), uint32(api.abi_minor), uint32(api.struct_size)); err != nil {
		C.sc_unload(handle)
		return nil, fmt.Errorf("plugin %s: %w", name, err)
	} else if warn != "" {
		log.Warn("plugin %s: %s", name, warn)
	}

	section := cfg.PluginSections[name]
	if section == nil {
		section = map[string]any{}
	}
	cfgJSON := cfg.pluginConfigJSON(name)
	if name == "telegram" {
		log.Info("telegram config loaded: api_id=%v api_hash_present=%t login_mode=%v", section["api_id"], strings.TrimSpace(fmt.Sprint(section["api_hash"])) != "", section["login_mode"])
	}
	cCfg := C.CString(cfgJSON)
	defer C.free(unsafe.Pointer(cCfg))
	releaseCfg := beginPluginScope(name, section)
	rc := C.sc_api_init(api, C.sc_core_api(), cCfg)
	releaseCfg()

	if rc != 0 {
		C.sc_unload(handle)
		return nil, fmt.Errorf("init failed with code rc=%d", int(rc))
	}

	info_ := C.sc_api_get_info(api)
	nameStr := name
	verStr := "?"
	flags := uint32(0)
	if info_ != nil {
		nameStr = C.GoString(info_.name)
		verStr = C.GoString(info_.version)
		flags = uint32(info_.flags)
	}

	var hooksPtr *C.sc_hook_reg_t
	var hooksLen C.size_t
	if rc := C.sc_api_get_hooks(api, &hooksPtr, &hooksLen); rc != 0 {
		log.Debug("plugin %s: get_hooks result rc=%d", nameStr, int(rc))
		hooksPtr, hooksLen = nil, 0
	} else if hooksLen > 0 {
		log.Info("plugin %s: registered hooks: %d", nameStr, int(hooksLen))
	}

	var syscallsPtr *C.sc_syscall_reg_t
	var syscallsLen C.size_t
	if rc := C.sc_api_get_syscalls(api, &syscallsPtr, &syscallsLen); rc != 0 {
		log.Debug("plugin %s: get_syscalls result rc=%d", nameStr, int(rc))
		syscallsPtr, syscallsLen = nil, 0
	} else if syscallsLen > 0 {
		log.Info("plugin %s: registered syscalls: %d", nameStr, int(syscallsLen))
	}

	transportName := ""
	if tn := C.sc_api_transport_name(api); tn != nil {
		transportName = C.GoString(tn)
	}

	// register native transport if available
	var transportPtr *C.sc_transport_info_t
	if tp := C.sc_api_get_transport(api); tp != nil {
		transportPtr = tp
		if tn := C.sc_transport_name(tp); tn != nil && transportName == "" {
			transportName = C.GoString(tn)
		}
	}

	var widgetsPtr *C.sc_widget_info_t
	var widgetsLen C.size_t
	if rc := C.sc_api_get_widgets(api, &widgetsPtr, &widgetsLen); rc == 0 && widgetsPtr != nil && widgetsLen > 0 {
		log.Info("plugin %s: registered widgets: %d", nameStr, int(widgetsLen))
	}

	if about.Name != "" {
		nameStr = about.Name
	}
	return &LoadedAddon{
		Name: nameStr, Version: verStr, Desc: about.HumanDescription,
		LLMDescription: about.LLMDescription, HumanDescription: about.HumanDescription,
		Path: artifact, handle: handle, api: api,
		flags:    flags,
		hooksPtr: hooksPtr, hooksLen: hooksLen,
		syscallsPtr: syscallsPtr, syscallsLen: syscallsLen,
		TransportName: transportName,
		transportPtr:  transportPtr,
		widgetsPtr:    widgetsPtr, widgetsLen: widgetsLen,
	}, nil
}

func loadAddons(rootDir string, cfg *Config, log *Logger) []*LoadedAddon {
	gCfg = cfg
	setPluginSections(cfg.PluginSections)
	if len(cfg.Plugins.Load) == 0 {
		return nil
	}

	preview := make([]dashboard.PluginInfo, 0, len(cfg.Plugins.Load))
	for _, entry := range cfg.Plugins.Load {
		name := filepath.Base(strings.TrimRight(entry, `/\`))
		enabled := cfg.isPluginEnabled(name)
		preview = append(preview, dashboard.PluginInfo{
			Name: name, Enabled: enabled, Loaded: false,
		})
	}
	if gDash != nil {
		gDash.SetPlugins(preview)
	}

	var out []*LoadedAddon
	loaded := make([]dashboard.PluginInfo, 0, len(cfg.Plugins.Load))

	for _, entry := range cfg.Plugins.Load {
		name, dir := resolveModuleDir(rootDir, entry)

		if !cfg.isPluginEnabled(name) {
			log.Info("plugin %s: disabled in config", name)
			loaded = append(loaded, dashboard.PluginInfo{
				Name: name, Version: "-", Enabled: false, Loaded: false,
			})
			if gDash != nil {
				gDash.SetPlugins(loaded)
			}
			continue
		}

		addon, err := loadOneAddon(name, dir, rootDir, cfg, log)
		if err != nil {
			log.Error("plugin %s: load error: %v", name, err)
			loaded = append(loaded, dashboard.PluginInfo{
				Name: name, Version: "err", Enabled: true, Loaded: false,
			})
			if gDash != nil {
				gDash.SetPlugins(loaded)
			}
			continue
		}

		log.Info("plugin loaded successfully: %s v%s", addon.Name, addon.Version)
		loaded = append(loaded, dashboard.PluginInfo{
			Name: addon.Name, Version: addon.Version,
			Desc: addon.Desc, Enabled: true, Loaded: true,
		})
		if gDash != nil {
			gDash.SetPlugins(loaded)
		}

		out = append(out, addon)
	}
	return out
}
