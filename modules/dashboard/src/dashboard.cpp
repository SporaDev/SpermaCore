/* dashboard.cpp - SpermaCore ABI 1.6.1 / 1.7: Autonomous telemetry and dashboard module.
 *
 * Provides:
 *   1. WebUI widget "dashboard.telemetry" (live stats, uptime, error rate, queue length).
 *   2. LLM tools: dashboard_metrics, dashboard_health.
 *   3. Dashboard syscalls table: "dashboard.*" (dashboard.stats, dashboard.track_reply, dashboard.track_tool).
 *   4. Pipeline hooks: "before_event", "after_llm_response", "on_tool_call".
 */
#include "plugin_api.h"
#include "json.hpp"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <atomic>
#include <chrono>
#include <mutex>
#include <string>

static const sc_core_api_t* g_core = nullptr;

/* ---------------------------------------------------------------------
 * telemetry state
 * ------------------------------------------------------------------- */

static std::mutex g_dash_mu;
static std::atomic<int64_t> g_messages_total{0};
static std::atomic<int64_t> g_replies_total{0};
static std::atomic<int64_t> g_tool_calls_total{0};
static std::atomic<int64_t> g_errors_total{0};
static std::atomic<int64_t> g_total_latency_ms{0};
static std::atomic<int64_t> g_last_latency_ms{0};
static int64_t g_started_ts = 0;

static void log_dash(int level, const char* msg) {
    if (g_core && g_core->log) {
        g_core->log(level, "dashboard", msg);
    }
}

static int64_t get_uptime_seconds(void) {
    time_t now = time(nullptr);
    return (int64_t)(now - g_started_ts);
}

/* ---------------------------------------------------------------------
 * llm tools
 * ------------------------------------------------------------------- */

static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "[{\"name\":\"dashboard_metrics\","
        "\"description\":\"Get live system telemetry: message counts, replies, tool calls, average latency, and uptime.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}},"
        "{\"name\":\"dashboard_health\","
        "\"description\":\"Check system health, error rates, and operational status.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}}]";
    *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL std::string g_tool_out;

static sc_status_t call_tool(const char* name, const char* args_json, char** out_result) {
    (void)args_json;
    if (!name || !out_result) return SC_ERR_INVALID_ARG;
    *out_result = nullptr;

    if (strcmp(name, "dashboard_metrics") == 0) {
        int64_t replies = g_replies_total.load();
        int64_t avg_lat = (replies > 0) ? (g_total_latency_ms.load() / replies) : 0;
        scjson::Value res = scjson::make_object();
        res.set("uptime_sec", scjson::make_int(get_uptime_seconds()));
        res.set("messages", scjson::make_int(g_messages_total.load()));
        res.set("replies", scjson::make_int(replies));
        res.set("tool_calls", scjson::make_int(g_tool_calls_total.load()));
        res.set("errors", scjson::make_int(g_errors_total.load()));
        res.set("avg_latency_ms", scjson::make_int(avg_lat));
        res.set("last_latency_ms", scjson::make_int(g_last_latency_ms.load()));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "dashboard_health") == 0) {
        int64_t errors = g_errors_total.load();
        int64_t replies = g_replies_total.load();
        std::string status = "healthy";
        if (errors > 10 && replies > 0 && (errors * 100 / (replies + errors)) > 20) {
            status = "degraded";
        }
        scjson::Value res = scjson::make_object();
        res.set("status", scjson::make_string(status));
        res.set("uptime_sec", scjson::make_int(get_uptime_seconds()));
        res.set("errors", scjson::make_int(errors));
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

static sc_status_t handle_dashboard_syscall(const char* method, const char* args_json, char** out_json) {
    if (!method || !out_json) return SC_ERR_INVALID_ARG;
    *out_json = nullptr;

    if (strcmp(method, "dashboard.stats") == 0) {
        int64_t replies = g_replies_total.load();
        int64_t avg_lat = (replies > 0) ? (g_total_latency_ms.load() / replies) : 0;
        scjson::Value res = scjson::make_object();
        res.set("uptime_sec", scjson::make_int(get_uptime_seconds()));
        res.set("messages", scjson::make_int(g_messages_total.load()));
        res.set("replies", scjson::make_int(replies));
        res.set("tool_calls", scjson::make_int(g_tool_calls_total.load()));
        res.set("errors", scjson::make_int(g_errors_total.load()));
        res.set("avg_latency_ms", scjson::make_int(avg_lat));
        res.set("last_latency_ms", scjson::make_int(g_last_latency_ms.load()));
        g_syscall_out = scjson::dump(res);
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "dashboard.track_reply") == 0) {
        scjson::Value in;
        scjson::parse(args_json ? args_json : "{}", in);
        int64_t lat = scjson::num(&in, "duration_ms", 0);
        if (lat > 0) {
            g_replies_total++;
            g_total_latency_ms += lat;
            g_last_latency_ms.store(lat);
        }
        g_syscall_out = "{\"status\":\"ok\"}";
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "dashboard.track_tool") == 0) {
        g_tool_calls_total++;
        g_syscall_out = "{\"status\":\"ok\"}";
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "dashboard.track_error") == 0) {
        g_errors_total++;
        g_syscall_out = "{\"status\":\"ok\"}";
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

static const sc_syscall_reg_t g_syscalls[] = {
    {
        .prefix = "dashboard",
        .fn = handle_dashboard_syscall,
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
    (void)ud; (void)name; (void)payload;
    g_messages_total++;
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t hook_after_llm_response(void* ud, const char* name, const char* payload, char** out) {
    (void)ud; (void)name; (void)payload;
    g_replies_total++;
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t hook_on_tool_call(void* ud, const char* name, const char* payload, char** out) {
    (void)ud; (void)name; (void)payload;
    g_tool_calls_total++;
    if (out) *out = nullptr;
    return SC_OK;
}

static const sc_hook_reg_t g_hooks[] = {
    { "before_event", hook_before_event, nullptr, 10, 0 },
    { "after_llm_response", hook_after_llm_response, nullptr, 10, 0 },
    { "on_tool_call", hook_on_tool_call, nullptr, 10, 0 },
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
    int64_t uptime = get_uptime_seconds();
    int64_t hours = uptime / 3600;
    int64_t mins = (uptime % 3600) / 60;
    int64_t secs = uptime % 60;

    int64_t replies = g_replies_total.load();
    int64_t avg_lat = (replies > 0) ? (g_total_latency_ms.load() / replies) : 0;

    char buf[1024];
    snprintf(buf, sizeof(buf),
        "<div style='padding:6px;'>"
        "  <div style='display:flex; justify-content:space-between; align-items:center; margin-bottom:8px;'>"
        "    <span style='font-size:12px; font-weight:600; color:var(--text-1);'>Telemetry</span>"
        "    <span style='font-size:10px; color:var(--green); font-family:monospace;'>%02lld:%02lld:%02lld</span>"
        "  </div>"
        "  <div style='display:grid; grid-template-columns:1fr 1fr; gap:6px; font-size:11px;'>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Messages / Ingest</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--accent);'>%lld</div>"
        "    </div>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Replies Sent</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--text-1);'>%lld</div>"
        "    </div>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Tool Executions</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--text-1);'>%lld</div>"
        "    </div>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Avg Latency</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--text-1);'>%lldms</div>"
        "    </div>"
        "  </div>"
        "</div>",
        (long long)hours, (long long)mins, (long long)secs,
        (long long)g_messages_total.load(),
        (long long)replies,
        (long long)g_tool_calls_total.load(),
        (long long)avg_lat
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
    root.set("messages", scjson::make_int(g_messages_total.load()));
    root.set("replies", scjson::make_int(g_replies_total.load()));
    root.set("uptime", scjson::make_int(get_uptime_seconds()));
    g_widget_out = scjson::dump(root);
    *out_json = (char*)g_widget_out.c_str();
    return SC_OK;
}

static sc_status_t widget_click(const char* payload_json, char** out_json) {
    (void)payload_json;
    return widget_update(out_json);
}

static const sc_widget_info_t g_widgets[] = {
    {
        .id = "dashboard.telemetry",
        .title = "Telemetry & Metrics",
        .icon = "📊",
        .default_w = 300,
        .default_h = 170,
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
        .name        = "dashboard",
        .version     = "1.0.0",
        .description = "System telemetry, vitals, live metrics and WebUI telemetry widget",
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

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    (void)cfg;
    if (!api) return SC_ERR_INVALID_ARG;
    g_core = api;
    g_started_ts = (int64_t)time(nullptr);
    log_dash(SC_LOG_INFO, "dashboard module initialized (telemetry live)");
    return SC_OK;
}

static sc_status_t deinit(void) {
    log_dash(SC_LOG_INFO, "dashboard module deinitialized");
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
