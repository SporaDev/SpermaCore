/* mediademo.cpp - SpermaCore addon: demo ABI 1.5 event bus.
 *
 * demonstrates ABI 1.5 capabilities: custom media formats,
 * media event emission, and extended lifecycle hooks.
 */
#include "plugin_api.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const sc_core_api_t* g_core = nullptr;

/* ---------------------------------------------------------------------
 * helper macro: call ABI 1.5 feature only if present
 * ------------------------------------------------------------------- */
#define HAVE15(member) (g_core && g_core->abi_minor >= 5 && g_core->member)

/* ---------------------------------------------------------------------
 * event bus hooks
 * ------------------------------------------------------------------- */

static sc_status_t on_media(void* ud, const char* name,
                            const char* payload, char** out) {
    (void)ud; (void)name;
    if (HAVE15(log)) {
        g_core->log(SC_LOG_INFO, "mediademo", payload);
    }
    /* allow event to propagate to main pipeline */
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t on_custom_event(void* ud, const char* name,
                                   const char* payload, char** out) {
    (void)ud; (void)name;
    if (HAVE15(log)) {
        g_core->log(SC_LOG_INFO, "mediademo.custom", payload);
    }
    if (out) *out = nullptr;
    return SC_OK;
}

static const sc_hook_reg_t g_hooks[] = {
    { "on_media", on_media, nullptr, 100, 0 },
    { "on_custom_event", on_custom_event, nullptr, 100, 0 },
};

/* ---------------------------------------------------------------------
 * metadata and lifecycle
 * ------------------------------------------------------------------- */

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name = "mediademo",
        .version = "1.5.0",
        .description = "ABI 1.5 event-bus demo: custom media formats, hooks, shutdown",
        .capabilities = nullptr,
        /* 1.4: */
        .min_abi_major = 1,
        .min_abi_minor = 5,
        .flags = SC_PLUGIN_FLAG_MEDIA_HANDLING | SC_PLUGIN_FLAG_EVENT_FILTERING,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;

    /* register custom media format */
    if (HAVE15(register_media_format)) {
        g_core->register_media_format("tg:poll", "application/json",
            "Telegram poll custom format");
    }

    /* report startup health status */
    if (HAVE15(health_check)) {
        g_core->health_check(SC_HEALTH_OK, "mediademo initialized");
    }

    /* fetch section config during init */
    if (HAVE15(config_section_json)) {
        const char* sec = g_core->config_section_json();
        if (sec) {
            g_core->log(SC_LOG_INFO, "mediademo", sec);
            g_core->free((void*)sec);
        }
    }

    if (HAVE15(log)) {
        char buf[128];
        snprintf(buf, sizeof(buf), "init: ABI %u.%u, config_version=%u",
            g_core->abi_major, g_core->abi_minor,
            HAVE15(config_version) ? g_core->config_version() : 0);
        g_core->log(SC_LOG_INFO, "mediademo", buf);
    }
    return SC_OK;
}

static sc_status_t on_startup(void) {
    /* broadcast custom startup event */
    if (HAVE15(emit_custom_event)) {
        g_core->emit_custom_event("mediademo.started",
            "{\"plugin\":\"mediademo\",\"event\":\"started\"}");
    }
    return SC_OK;
}

static sc_status_t deinit(void) { return SC_OK; }

static sc_status_t on_shutdown(void) {
    if (HAVE15(log)) {
        g_core->log(SC_LOG_INFO, "mediademo", "on_shutdown: flushing");
    }
    return SC_OK;
}

static sc_status_t health_check_enhanced(char** out) {
    static SC_THREAD_LOCAL char buf[256];
    snprintf(buf, sizeof(buf),
        "{\"ok\":true,\"plugin\":\"mediademo\",\"formats\":[\"tg:poll\"]}");
    if (out) *out = buf;
    return SC_OK;
}

static sc_status_t get_hooks(const sc_hook_reg_t** o, size_t* c) {
    if (o) *o = g_hooks;
    if (c) *c = sizeof(g_hooks) / sizeof(g_hooks[0]);
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * tool: emit custom media event via emit_media_ext
 * ------------------------------------------------------------------- */

static sc_status_t get_tools(char** out) {
    static const char* tools =
        "[{\"name\":\"mediademo.emit_poll\","
        "\"description\":\"Emit a custom-media event with a custom format "
        "(tg:poll) to all plugins subscribed to the event bus.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"question\":{\"type\":\"string\",\"description\":\"Poll question\"}"
        "},\"required\":[\"question\"]}}]";
    if (out) *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL char g_result[4096];

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;
    if (strcmp(name, "mediademo.emit_poll") != 0) return SC_ERR_NOT_FOUND;

    if (!HAVE15(emit_media_ext)) {
        snprintf(g_result, sizeof(g_result),
            "{\"status\":\"error\",\"reason\":\"ABI 1.5 emit_media_ext not available\"}");
        *out = g_result;
        return SC_OK;
    }

    uint32_t ev_id = 0;
    /* forward input args or fallback payload */
    const char* payload = (args && args[0]) ? args : "{\"event\":\"poll\"}";
    int rc = g_core->emit_media_ext(
        SC_MEDIA_NONE,          /* no matching enum value */
        "tg:poll",              /* custom format identifier */
        payload,
        &ev_id);

    snprintf(g_result, sizeof(g_result),
        "{\"status\":\"%s\",\"event_id\":%u,\"mime\":\"%s\"}",
        rc == 0 ? "ok" : "error", ev_id,
        g_core->media_mime_type ? g_core->media_mime_type(SC_MEDIA_VOICE) : "?");
    *out = g_result;
    (void)rc;
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * plugin registration
 * ------------------------------------------------------------------- */

static sc_plugin_api_t g_api = {
    .abi_major = SC_ABI_MAJOR,
    .abi_minor = SC_ABI_MINOR,
    .struct_size = sizeof(sc_plugin_api_t),
    .get_info = get_info,
    .init = init,
    .deinit = deinit,
    .get_hooks = get_hooks,
    .get_tools = get_tools,
    .call_tool = call_tool,
    .on_startup = on_startup,
    .health_check_enhanced = health_check_enhanced,
    .on_shutdown = on_shutdown,
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}
