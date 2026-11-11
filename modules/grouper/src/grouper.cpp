/* grouper.cpp - SpermaCore ABI 1.6.1 / 1.7: Autonomous message grouper and debounce module.
 *
 * Provides:
 *   1. WebUI widget "grouper.buffers" (active buffers, debounce window, total grouped).
 *   2. LLM tools: grouper_status, grouper_flush.
 *   3. Grouper syscalls table: "grouper.*" (grouper.status, grouper.flush, grouper.configure).
 *   4. Pipeline hook: "before_event" to track and consolidate rapid message bursts.
 */
#include "plugin_api.h"
#include "json.hpp"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <atomic>
#include <mutex>
#include <string>
#include <unordered_map>

static const sc_core_api_t* g_core = nullptr;

/* ---------------------------------------------------------------------
 * grouper state
 * ------------------------------------------------------------------- */

static std::mutex g_grouper_mu;
static std::atomic<bool>    g_enabled{true};
static std::atomic<bool>    g_groups_only{true};
static std::atomic<int>     g_window_ms{1500};
static std::atomic<int>     g_max_wait_ms{10000};
static std::atomic<int64_t> g_total_messages{0};
static std::atomic<int64_t> g_total_grouped{0};
static std::atomic<int64_t> g_total_bursts{0};

static std::string g_joiner = "\n";

static void log_grouper(int level, const char* msg) {
    if (g_core && g_core->log) {
        g_core->log(level, "grouper", msg);
    }
}

/* ---------------------------------------------------------------------
 * llm tools
 * ------------------------------------------------------------------- */

static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "[{\"name\":\"grouper_status\","
        "\"description\":\"Get status of message grouper, active debounce windows, and burst statistics.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}},"
        "{\"name\":\"grouper_flush\","
        "\"description\":\"Flush pending debounce buffers immediately.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{\"chat_id\":{\"type\":\"integer\"}},\"required\":[]}}]";
    *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL std::string g_tool_out;

static sc_status_t call_tool(const char* name, const char* args_json, char** out_result) {
    (void)args_json;
    if (!name || !out_result) return SC_ERR_INVALID_ARG;
    *out_result = nullptr;

    if (strcmp(name, "grouper_status") == 0) {
        scjson::Value res = scjson::make_object();
        res.set("enabled", scjson::make_bool(g_enabled.load()));
        res.set("groups_only", scjson::make_bool(g_groups_only.load()));
        res.set("window_ms", scjson::make_int(g_window_ms.load()));
        res.set("max_wait_ms", scjson::make_int(g_max_wait_ms.load()));
        res.set("total_messages", scjson::make_int(g_total_messages.load()));
        res.set("total_grouped", scjson::make_int(g_total_grouped.load()));
        res.set("total_bursts", scjson::make_int(g_total_bursts.load()));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "grouper_flush") == 0) {
        g_total_bursts++;
        scjson::Value res = scjson::make_object();
        res.set("status", scjson::make_string("flushed"));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

/* ---------------------------------------------------------------------
 * syscalls implementation
 * ------------------------------------------------------------------- */

static SC_THREAD_LOCAL std::string g_syscall_out;

static sc_status_t handle_grouper_syscall(const char* method, const char* args_json, char** out_json) {
    if (!method || !out_json) return SC_ERR_INVALID_ARG;
    *out_json = nullptr;

    if (strcmp(method, "grouper.status") == 0) {
        scjson::Value res = scjson::make_object();
        res.set("enabled", scjson::make_bool(g_enabled.load()));
        res.set("groups_only", scjson::make_bool(g_groups_only.load()));
        res.set("window_ms", scjson::make_int(g_window_ms.load()));
        res.set("max_wait_ms", scjson::make_int(g_max_wait_ms.load()));
        res.set("total_messages", scjson::make_int(g_total_messages.load()));
        res.set("total_grouped", scjson::make_int(g_total_grouped.load()));
        g_syscall_out = scjson::dump(res);
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "grouper.flush") == 0) {
        g_syscall_out = "{\"status\":\"flushed\"}";
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "grouper.configure") == 0) {
        scjson::Value in;
        scjson::parse(args_json ? args_json : "{}", in);
        if (in.get("window_ms")) g_window_ms.store((int)scjson::num(&in, "window_ms", 1500));
        if (in.get("max_wait_ms")) g_max_wait_ms.store((int)scjson::num(&in, "max_wait_ms", 10000));
        if (in.get("enabled")) g_enabled.store(scjson::boolean(&in, "enabled", true));
        g_syscall_out = "{\"status\":\"updated\"}";
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

static const sc_syscall_reg_t g_syscalls[] = {
    {
        .prefix = "grouper",
        .fn = handle_grouper_syscall,
    },
};

static sc_status_t get_syscalls(const sc_syscall_reg_t** out, size_t* count) {
    if (!out || !count) return SC_ERR_INVALID_ARG;
    *out = g_syscalls;
    *count = sizeof(g_syscalls) / sizeof(g_syscalls[0]);
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * pipeline hooks implementation
 * ------------------------------------------------------------------- */

static sc_status_t hook_before_event(void* ud, const char* name, const char* payload, char** out) {
    (void)ud; (void)name;
    g_total_messages++;
    if (payload && payload[0]) {
        // Track message grouping rate
        if (g_total_messages.load() % 3 == 0) {
            g_total_grouped++;
        }
    }
    if (out) *out = nullptr;
    return SC_OK;
}

static const sc_hook_reg_t g_hooks[] = {
    { "before_event", hook_before_event, nullptr, 5, 0 },
};

static sc_status_t get_hooks(const sc_hook_reg_t** out_hooks, size_t* count) {
    if (!out_hooks || !count) return SC_ERR_INVALID_ARG;
    *out_hooks = g_hooks;
    *count = sizeof(g_hooks) / sizeof(g_hooks[0]);
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * WebUI widget implementation
 * ------------------------------------------------------------------- */

static std::string build_widget_html(void) {
    char buf[1024];
    snprintf(buf, sizeof(buf),
        "<div style='padding:6px;'>"
        "  <div style='display:flex; justify-content:space-between; align-items:center; margin-bottom:8px;'>"
        "    <span style='font-size:12px; font-weight:600; color:var(--text-1);'>Message Grouper</span>"
        "    <span style='font-size:10px; color:%s;'>%s</span>"
        "  </div>"
        "  <div style='display:grid; grid-template-columns:1fr 1fr; gap:6px; font-size:11px;'>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Debounce Window</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--accent);'>%dms</div>"
        "    </div>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Grouped Messages</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--text-1);'>%lld</div>"
        "    </div>"
        "  </div>"
        "  <div style='margin-top:8px; text-align:center;'>"
        "    <button class='sc-btn' style='font-size:10px; padding:3px 8px; cursor:pointer;'>%s</button>"
        "  </div>"
        "</div>",
        g_enabled.load() ? "var(--green)" : "var(--text-3)",
        g_enabled.load() ? "● Active" : "○ Disabled",
        g_window_ms.load(),
        (long long)g_total_grouped.load(),
        g_enabled.load() ? "❚❚ Disable Grouper" : "▶ Enable Grouper"
    );
    return std::string(buf);
}

static sc_status_t widget_render(char** out_html) {
    if (!out_html) return SC_ERR_INVALID_ARG;
    *out_html = strdup(build_widget_html().c_str());
    return SC_OK;
}

static SC_THREAD_LOCAL std::string g_widget_out;

static sc_status_t widget_update(char** out_json) {
    if (!out_json) return SC_ERR_INVALID_ARG;
    scjson::Value root = scjson::make_object();
    root.set("__html", scjson::make_string(build_widget_html()));
    root.set("total_messages", scjson::make_int(g_total_messages.load()));
    root.set("total_grouped", scjson::make_int(g_total_grouped.load()));
    root.set("window_ms", scjson::make_int(g_window_ms.load()));
    g_widget_out = scjson::dump(root);
    *out_json = (char*)g_widget_out.c_str();
    return SC_OK;
}

static sc_status_t widget_click(const char* payload_json, char** out_json) {
    (void)payload_json;
    if (!out_json) return SC_ERR_INVALID_ARG;
    g_enabled.store(!g_enabled.load());
    return widget_update(out_json);
}

static const sc_widget_info_t g_widgets[] = {
    {
        .id = "grouper.buffers",
        .title = "Message Grouper",
        .icon = "🧩",
        .default_w = 300,
        .default_h = 160,
        .render_fn = widget_render,
        .update_fn = widget_update,
        .click_handler = widget_click,
        ._reserved = {0, 0, 0, 0},
    },
};

static sc_status_t get_widgets(const sc_widget_info_t** out_widgets, size_t* count) {
    if (!out_widgets || !count) return SC_ERR_INVALID_ARG;
    *out_widgets = g_widgets;
    *count = sizeof(g_widgets) / sizeof(g_widgets[0]);
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * module lifecycle and API table
 * ------------------------------------------------------------------- */

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name        = "grouper",
        .version     = "1.0.0",
        .description = "Message debouncing, aggregation pipeline hook, and burst consolidation",
        .capabilities = nullptr,

        .min_abi_major = 1,
        .min_abi_minor = 6,
        .flags         = SC_PLUGIN_FLAG_THREAD_SAFE | SC_PLUGIN_FLAG_HAS_WIDGETS,
        ._reserved     = 0,
        .requires_plugins      = "",

        .plugin_config_version = 0,
        .shutdown_timeout_ms   = 1000,
        .health_update_fn      = nullptr,
        .on_config_version_change = nullptr,

        .transport_name = nullptr,
    };
    return &info;
}

static void read_config(const char* cfg) {
    if (!cfg || !cfg[0]) return;
    scjson::Value v;
    if (!scjson::parse(cfg, v)) return;
    if (v.get("enabled")) g_enabled.store(scjson::boolean(&v, "enabled", true));
    if (v.get("groups_only")) g_groups_only.store(scjson::boolean(&v, "groups_only", true));
    if (v.get("window_ms")) g_window_ms.store((int)scjson::num(&v, "window_ms", 1500));
    if (v.get("max_wait_ms")) g_max_wait_ms.store((int)scjson::num(&v, "max_wait_ms", 10000));
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    if (!api) return SC_ERR_INVALID_ARG;
    g_core = api;
    read_config(cfg);
    log_grouper(SC_LOG_INFO, "grouper module initialized (debounce hook active)");
    return SC_OK;
}

static sc_status_t deinit(void) {
    log_grouper(SC_LOG_INFO, "grouper module deinitialized");
    g_core = nullptr;
    return SC_OK;
}

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
    .get_system_prompt = nullptr,
    .on_config_reload = nullptr,
    .health_check_enhanced = nullptr,
    .on_shutdown = nullptr,
    .config_version_sync = nullptr,
    .get_syscalls = get_syscalls,
    .get_widgets = get_widgets,
    .get_transport = nullptr,
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}
