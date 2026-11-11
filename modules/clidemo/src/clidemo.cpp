/* clidemo.cpp - SpermaCore addon (ABI 1.7): demo transport plugin.
 *
 * demonstrates custom transport implementation:
 *   1. get_transport() provides ABI 1.7 transport interface.
 *   2. transport_name provides backward compatibility.
 *   3. get_syscalls handles "clidemo." syscalls.
 *   4. on_transport_send provides legacy fallback hook.
 *   5. core->ingest_event forwards incoming messages to core worker pool.
 */
#include "plugin_api.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const sc_core_api_t* g_core = nullptr;

#define HAVE16(member) (g_core && g_core->abi_minor >= 6 && g_core->member)

/* ---------------------------------------------------------------------
 * syscall handlers (prefix "clidemo.")
 * ------------------------------------------------------------------- */

static sc_status_t clidemo_echo(const char* method, const char* args, char** out) {
    (void)method;
    static SC_THREAD_LOCAL char buf[512];
    /* args is valid json, inject directly without double encoding */
    snprintf(buf, sizeof(buf),
             "{\"status\":\"ok\",\"plugin\":\"clidemo\",\"echo\":%s}",
             (args && args[0]) ? args : "{}");
    if (out) *out = buf;
    return SC_OK;
}

static const sc_syscall_reg_t g_syscalls[] = {
    { "clidemo.", clidemo_echo },
};

static sc_status_t get_syscalls(const sc_syscall_reg_t** o, size_t* c) {
    if (o) *o = g_syscalls;
    if (c) *c = sizeof(g_syscalls) / sizeof(g_syscalls[0]);
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * event bus hooks
 * ------------------------------------------------------------------- */

/* on_transport_send handles legacy transport sends by logging */
static sc_status_t on_transport_send(void* ud, const char* name,
                                     const char* payload, char** out) {
    (void)ud; (void)name;
    if (g_core && g_core->log) {
        g_core->log(SC_LOG_INFO, "clidemo.send", payload);
    }
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t on_custom_event(void* ud, const char* name,
                                   const char* payload, char** out) {
    (void)ud; (void)name;
    if (g_core && g_core->log) {
        g_core->log(SC_LOG_INFO, "clidemo.event", payload);
    }
    if (out) *out = nullptr;
    return SC_OK;
}

static const sc_hook_reg_t g_hooks[] = {
    { "on_transport_send", on_transport_send, nullptr, 100, 0 },
    { "on_custom_event",   on_custom_event,   nullptr, 100, 0 },
};

/* ---------------------------------------------------------------------
 * abi 1.7 transport implementation
 * ------------------------------------------------------------------- */

static sc_status_t transport_send_text(const char* payload, char** out) {
    if (g_core && g_core->log) g_core->log(SC_LOG_INFO, "clidemo.send", payload);
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t transport_send_media(const char* payload, char** out) {
    if (g_core && g_core->log) g_core->log(SC_LOG_INFO, "clidemo.media", payload);
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t transport_typing(const char* payload, char** out) {
    if (g_core && g_core->log) g_core->log(SC_LOG_DEBUG, "clidemo.typing", payload);
    if (out) *out = nullptr;
    return SC_OK;
}

static const sc_transport_info_t g_transport = {
    "clidemo",
    "CLI demo",
    "ABI 1.7 demo transport: logs outgoing messages, accepts ingest_event",
    SC_TRANSPORT_CAP_TEXT | SC_TRANSPORT_CAP_MEDIA | SC_TRANSPORT_CAP_TYPING,
    0,      /* max_text_len: 0 = unlimited */
    0,      /* flags */
    {0, 0, 0},
    transport_send_text,
    transport_send_media,
    nullptr,   /* react */
    nullptr,   /* mark_read */
    transport_typing,
    nullptr,   /* edit_text */
    nullptr,   /* delete_message */
    nullptr,   /* poll */
    {0, 0, 0, 0},
};

static const sc_transport_info_t* get_transport(void) {
    return &g_transport;
}

/* ---------------------------------------------------------------------
 * metadata and lifecycle
 * ------------------------------------------------------------------- */

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        "clidemo", "1.7.0",
        "ABI 1.7 demo: plugin transport via sc_transport_info_t (no core edits)",
        nullptr,
        /* 1.4: */
        1,   /* min_abi_major */
        6,   /* min_abi_minor: supports legacy cores via hooks */
        SC_PLUGIN_FLAG_THREAD_SAFE | SC_PLUGIN_FLAG_EVENT_FILTERING |
            SC_PLUGIN_FLAG_TRANSPORT,
        0,   /* _reserved */
        nullptr,/* requires */
        /* 1.5: */
        0,   /* plugin_config_version */
        0,   /* shutdown_timeout_ms */
        nullptr,/* health_update_fn */
        nullptr,/* on_config_version_change */
        /* 1.6: */
        "clidemo",  /* transport_name: legacy field */
        nullptr,       /* on_config_version_change_ud */
        /* 1.6.1 / 1.7: */
        0,          /* abi_patch */
        {0, 0, 0},
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;
    if (g_core->log) {
        char buf[128];
        snprintf(buf, sizeof(buf), "init: transport=%s syscalls=clidemo.*",
                 g_core->abi_minor >= 6 ? "registered" : "n/a");
        g_core->log(SC_LOG_INFO, "clidemo", buf);
    }
    /* ABI 1.7: report transport status to core for dashboard */
    if (g_core->abi_minor >= 7 && g_core->transport_status) {
        g_core->transport_status("clidemo", 1,
            "{\"mode\":\"demo\",\"note\":\"logs outgoing messages\"}");
    }
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * on_startup verifies syscall registry by calling clidemo.echo via core->syscall
 * ------------------------------------------------------------------- */
static sc_status_t on_startup(void) {
    if (g_core && g_core->syscall) {
        char* r = nullptr;
        int rc = g_core->syscall("clidemo.echo", "{\"ping\":1}", &r);
        if (g_core->log) {
            char buf[256];
            if (rc == 0 && r) {
                snprintf(buf, sizeof(buf), "syscall self-test OK: %s", r);
            } else {
                snprintf(buf, sizeof(buf), "syscall self-test FAILED rc=%d", rc);
            }
            g_core->log(SC_LOG_INFO, "clidemo", buf);
        }
        /* always free response buffer */
        if (r) g_core->free(r);
    }
    /* data_dir: plugin-specific storage directory */
    if (HAVE16(data_dir) && g_core->log) {
        const char* d = g_core->data_dir();
        if (d) {
            g_core->log(SC_LOG_INFO, "clidemo", d);
            g_core->free((void*)d);
        }
    }
    return SC_OK;
}

static sc_status_t deinit(void) { return SC_OK; }

static sc_status_t get_hooks(const sc_hook_reg_t** o, size_t* c) {
    if (o) *o = g_hooks;
    if (c) *c = sizeof(g_hooks) / sizeof(g_hooks[0]);
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * tools
 * ------------------------------------------------------------------- */

static sc_status_t get_tools(char** out) {
    static const char* tools =
        "["
        "{\"name\":\"clidemo.echo\","
        "\"description\":\"Demo: echoes the argument through the plugin's own "
        "syscall registry (tests plugin syscall routing).\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"text\":{\"type\":\"string\"}"
        "},\"required\":[\"text\"]}},"
        "{\"name\":\"clidemo.notify\","
        "\"description\":\"Demo: emit a custom event to the event bus.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"text\":{\"type\":\"string\"}"
        "},\"required\":[\"text\"]}},"
        "{\"name\":\"clidemo.ingest\","
        "\"description\":\"Demo: inject a message into the core as if it came "
        "from a transport (ingest_event). WARNING: goes through the LLM pipeline.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"text\":{\"type\":\"string\"}"
        "},\"required\":[\"text\"]}}"
        "]";
    if (out) *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL char g_result[4096];

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;
    if (strcmp(name, "clidemo.echo") == 0) {
        /* invoke own syscall via core routing */
        char* r = nullptr;
        int rc = g_core && g_core->syscall
            ? g_core->syscall("clidemo.echo", args, &r) : -1;
        if (rc == 0 && r) {
            snprintf(g_result, sizeof(g_result),
                     "{\"status\":\"ok\",\"syscall_result\":%s}", r);
            g_core->free(r);
        } else {
            snprintf(g_result, sizeof(g_result),
                     "{\"status\":\"error\",\"reason\":\"syscall routing failed rc=%d\"}", rc);
        }
        *out = g_result;
        return SC_OK;
    }
    if (strcmp(name, "clidemo.notify") == 0) {
        if (HAVE16(emit_custom_event)) {
            g_core->emit_custom_event("clidemo.notify", args);
        }
        snprintf(g_result, sizeof(g_result), "{\"status\":\"ok\",\"event\":\"clidemo.notify\"}");
        *out = g_result;
        return SC_OK;
    }
    if (strcmp(name, "clidemo.ingest") == 0) {
        if (!HAVE16(ingest_event)) {
            snprintf(g_result, sizeof(g_result),
                     "{\"status\":\"error\",\"reason\":\"ABI 1.6 ingest_event not available\"}");
            *out = g_result;
            return SC_OK;
        }
        /* simulate incoming message from clidemo transport */
        char ev[1024];
        snprintf(ev, sizeof(ev),
            "{\"type\":\"new_message\",\"transport\":\"clidemo\",\"chat_id\":0,"
            "\"sender_id\":1,\"sender_name\":\"clidemo\",\"sender_username\":\"clidemo\","
            "\"message_id\":1,\"reply_to_id\":0,\"text\":%s,\"media_type\":\"\"}",
            args ? args : "\"hello from clidemo\"");
        int rc = g_core->ingest_event(ev);
        snprintf(g_result, sizeof(g_result),
                 "{\"status\":\"ok\",\"ingested\":true,\"rc\":%d}", rc);
        *out = g_result;
        return SC_OK;
    }
    return SC_ERR_NOT_FOUND;
}

/* ---------------------------------------------------------------------
 * plugin registration
 * ------------------------------------------------------------------- */

static sc_plugin_api_t g_api = {
    SC_ABI_MAJOR,
    SC_ABI_MINOR,
    sizeof(sc_plugin_api_t),
    get_info,
    init,
    deinit,
    get_hooks,
    get_tools,
    call_tool,
    on_startup,    /* 1.1 */
    nullptr,          /* 1.2 get_system_prompt */
    nullptr,        /* 1.4 health */
    nullptr,        /* 1.4 on_config_reload */
    nullptr,        /* 1.5 health_check_enhanced */
    nullptr,        /* 1.5 on_shutdown */
    nullptr,        /* 1.5 config_version_sync */
    get_syscalls, /* 1.6 get_syscalls */
    nullptr,         /* 1.6.1 get_widgets */
    get_transport,/* 1.7 get_transport */
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}
