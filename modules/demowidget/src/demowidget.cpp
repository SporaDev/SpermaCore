/* demowidget: webui widgets demo */
#include "plugin_api.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static const sc_core_api_t* g_core = nullptr;

// widget state
static int  g_counter = 0;        // main counter
static int  g_clicked = 0;        // click counter
static time_t g_started_at = 0;   // start timestamp
static int  g_auto_tick = 1;      // auto-increment flag

// initial widget html rendering
static sc_status_t widget_render(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;

    *out = (char*)
        "<div style='padding:4px;'>"
        "  <div style='display:flex; align-items:baseline; gap:8px;'>"
        "    <div id='dw-counter' style='font-size:44px; font-weight:700;"
        "         color:var(--accent); line-height:1;'>0</div>"
        "    <div style='color:var(--text-3); font-size:10px;'>"
        "      counter"
        "    </div>"
        "  </div>"
        "  <div style='display:flex; gap:6px; margin-top:10px; font-size:10px;'>"
        "    <div style='color:var(--text-3);'>"
        "      clicks: <span id='dw-clicked' style='color:var(--text-1);'>0</span>"
        "    </div>"
        "    <div style='color:var(--text-3); margin-left:auto;' id='dw-status'>"
        "      ● running"
        "    </div>"
        "  </div>"
        "  <div style='margin-top:8px; color:var(--text-3); font-size:10px;"
        "       text-align:center; padding-top:6px;"
        "       border-top:1px solid var(--border-0);'>"
        "    click to +10 · Ctrl+click for toast · Shift+click to toggle pause"
        "  </div>"
        "</div>";

    return SC_OK;
}

// periodic widget update
static SC_THREAD_LOCAL char g_update_buf[1024];

static sc_status_t widget_update(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;

    // auto-increment if not paused
    if (g_auto_tick) {
        g_counter++;
    }

    // format full html markup
    snprintf(g_update_buf, sizeof(g_update_buf),
        "<div style='padding:4px;'>"
        "  <div style='display:flex; align-items:baseline; gap:8px;'>"
        "    <div style='font-size:44px; font-weight:700; color:var(--accent);"
        "         line-height:1;'>%d</div>"
        "    <div style='color:var(--text-3); font-size:10px;'>counter</div>"
        "  </div>"
        "  <div style='display:flex; gap:6px; margin-top:10px; font-size:10px;'>"
        "    <div style='color:var(--text-3);'>clicks: "
        "      <span style='color:var(--text-1);'>%d</span>"
        "    </div>"
        "    <div style='color:var(--text-3); margin-left:auto;'>%s</div>"
        "  </div>"
        "  <div style='margin-top:8px; color:var(--text-3); font-size:10px;"
        "       text-align:center; padding-top:6px;"
        "       border-top:1px solid var(--border-0);'>"
        "    click to +10 · Ctrl+click for toast · Shift+click to toggle pause"
        "  </div>"
        "</div>",
        g_counter,
        g_clicked,
        g_auto_tick ? "● running" : "❚❚ paused"
    );

    // wrap html in json object
    static SC_THREAD_LOCAL char g_json_buf[2048];
    snprintf(g_json_buf, sizeof(g_json_buf),
        "{\"__html\":%s}", "\"__placeholder\"");

    // serialize html into json string
    {
        size_t j = 0;
        size_t cap = sizeof(g_json_buf) - 16;
        g_json_buf[j++] = '{';
        const char* key = "\"__html\":";
        for (size_t k = 0; key[k] && j < cap; k++) g_json_buf[j++] = key[k];
        g_json_buf[j++] = '"';
        for (size_t i = 0; g_update_buf[i] && j + 8 < cap; i++) {
            char c = g_update_buf[i];
            switch (c) {
                case '"':  g_json_buf[j++]='\\'; g_json_buf[j++]='"'; break;
                case '\\': g_json_buf[j++]='\\'; g_json_buf[j++]='\\'; break;
                case '\n': g_json_buf[j++]='\\'; g_json_buf[j++]='n'; break;
                case '\r': g_json_buf[j++]='\\'; g_json_buf[j++]='r'; break;
                case '\t': g_json_buf[j++]='\\'; g_json_buf[j++]='t'; break;
                default:
                    if ((unsigned char)c < 0x20) {
                        j += (size_t)snprintf(g_json_buf + j, 8, "\\u%04x", c);
                    } else {
                        g_json_buf[j++] = c;
                    }
            }
        }
        g_json_buf[j++] = '"';
        g_json_buf[j++] = '}';
        g_json_buf[j] = 0;
    }

    *out = g_json_buf;
    return SC_OK;
}

// click handler
static int json_get_int(const char* json, const char* field, int dflt) {
    if (!json || !field) return dflt;
    char pattern[128];
    snprintf(pattern, sizeof(pattern), "\"%s\"", field);
    const char* p = strstr(json, pattern);
    if (!p) return dflt;
    p += strlen(pattern);
    while (*p && *p != ':') p++;
    if (*p != ':') return dflt;
    p++;
    while (*p == ' ' || *p == '\t') p++;
    int neg = 0;
    if (*p == '-') { neg = 1; p++; }
    if (*p < '0' || *p > '9') return dflt;
    int v = 0;
    while (*p >= '0' && *p <= '9') { v = v * 10 + (*p - '0'); p++; }
    return neg ? -v : v;
}

static SC_THREAD_LOCAL char g_click_buf[2048];

static sc_status_t widget_click(const char* payload_json, char** out) {
    if (!out) return SC_ERR_INVALID_ARG;

    // increment click count
    g_clicked++;

    // toggle pause if click payload targeted status text
    int is_toggle = 0;
    if (payload_json && strstr(payload_json, "paused")) {
        is_toggle = 1;
        g_auto_tick = 1;
    } else if (payload_json && strstr(payload_json, "running")) {
        is_toggle = 1;
        g_auto_tick = 0;
    }

    if (!is_toggle) {
        g_counter += 10;
        if (g_core && g_core->log) {
            char msg[128];
            snprintf(msg, sizeof(msg), "clicked at (%d,%d), +10 -> %d",
                json_get_int(payload_json, "x", 0),
                json_get_int(payload_json, "y", 0),
                g_counter);
            g_core->log(SC_LOG_INFO, "demowidget", msg);
        }

        // push toast notification
        if (g_core && g_core->widget_notify) {
            g_core->widget_notify("demowidget.counter", 3,
                "+10 to counter!", 1500);
        }
    } else {
        if (g_core && g_core->log) {
            g_core->log(SC_LOG_INFO, "demowidget",
                g_auto_tick ? "resumed" : "paused");
        }
    }

    // return updated html
    snprintf(g_click_buf, sizeof(g_click_buf),
        "{\"__html\":\""
        "<div style='padding:4px;'>"
        "  <div style='display:flex; align-items:baseline; gap:8px;'>"
        "    <div style='font-size:44px; font-weight:700; color:var(--accent);"
        "         line-height:1;'>%d</div>"
        "    <div style='color:var(--text-3); font-size:10px;'>counter</div>"
        "  </div>"
        "  <div style='display:flex; gap:6px; margin-top:10px; font-size:10px;'>"
        "    <div style='color:var(--text-3);'>clicks: "
        "      <span style='color:var(--text-1);'>%d</span>"
        "    </div>"
        "    <div style='color:var(--text-3); margin-left:auto;'>%s</div>"
        "  </div>"
        "  <div style='margin-top:8px; color:var(--text-3); font-size:10px;"
        "       text-align:center; padding-top:6px;"
        "       border-top:1px solid var(--border-0);'>"
        "    click to +10 · Ctrl+click for toast · Shift+click to toggle pause"
        "  </div>"
        "</div>\"}",
        g_counter, g_clicked,
        g_auto_tick ? "● running" : "❚❚ paused");

    *out = g_click_buf;
    return SC_OK;
}

// widget descriptors
static const sc_widget_info_t g_widgets[] = {
    {
        .id = "demowidget.counter",
        .title = "Demo Counter",
        .icon = "🔢",
        .default_w = 260,
        .default_h = 180,
        .render_fn = widget_render,
        .update_fn = widget_update,
        .click_handler = widget_click,
        ._reserved = {0, 0, 0, 0},
    },
};

static sc_status_t get_widgets(const sc_widget_info_t** out, size_t* count) {
    if (!out || !count) return SC_ERR_INVALID_ARG;
    *out = g_widgets;
    *count = sizeof(g_widgets) / sizeof(g_widgets[0]);
    return SC_OK;
}

// metadata
static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name        = "demowidget",
        .version     = "1.0.0",
        .description = "ABI 1.6.1 demo: WebUI widget with live data and click handling",
        .capabilities = nullptr,

        .min_abi_major = 1,
        .min_abi_minor = 6,
        .flags         = SC_PLUGIN_FLAG_THREAD_SAFE | SC_PLUGIN_FLAG_HAS_WIDGETS,
        ._reserved     = 0,
        .requires_plugins      = "",

        .plugin_config_version = 0,
        .shutdown_timeout_ms   = 0,
        .health_update_fn      = nullptr,
        .on_config_version_change = nullptr,

        .transport_name = nullptr,
        .on_config_version_change_ud = nullptr,

        .abi_patch = 1,
        ._reserved_1_6_1 = {0, 0, 0},
    };
    return &info;
}

// lifecycle
static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;

    g_started_at = time(nullptr);

    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "demowidget", "init");
    }

    // restore persisted widget state
    if (g_core->widget_state_get) {
        const char* state = g_core->widget_state_get("demowidget.counter");
        if (state) {
            g_counter = json_get_int(state, "counter", 0);
            g_clicked = json_get_int(state, "clicked", 0);
            g_auto_tick = json_get_int(state, "auto_tick", 1);
            if (g_core->log) {
                char msg[128];
                snprintf(msg, sizeof(msg),
                    "restored state: counter=%d clicked=%d",
                    g_counter, g_clicked);
                g_core->log(SC_LOG_INFO, "demowidget", msg);
            }
        }
    }

    return SC_OK;
}

static sc_status_t deinit(void) {
    // persist state before shutdown
    if (g_core && g_core->widget_state_set) {
        char buf[128];
        snprintf(buf, sizeof(buf),
            "{\"counter\":%d,\"clicked\":%d,\"auto_tick\":%d}",
            g_counter, g_clicked, g_auto_tick);
        g_core->widget_state_set("demowidget.counter", buf);
    }

    if (g_core && g_core->log) {
        char msg[128];
        snprintf(msg, sizeof(msg), "deinit, counter=%d", g_counter);
        g_core->log(SC_LOG_INFO, "demowidget", msg);
    }

    g_core = nullptr;
    return SC_OK;
}

static sc_status_t on_startup(void) {
    if (g_core && g_core->log) {
        g_core->log(SC_LOG_INFO, "demowidget",
            "startup, widget 'demowidget.counter' registered");
    }
    return SC_OK;
}

static sc_status_t on_shutdown(void) {
    if (g_core && g_core->log) {
        g_core->log(SC_LOG_INFO, "demowidget", "shutdown");
    }
    return SC_OK;
}

// llm tools
static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "[{\"name\":\"demo_counter\","
        "\"description\":\"Return the current demo widget counter value. "
        "Use this when the user asks about the demo counter.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}},"
        "{\"name\":\"demo_bump\","
        "\"description\":\"Increase the demo widget counter by a specified amount. "
        "Also pushes update to the browser.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"amount\":{\"type\":\"integer\",\"description\":\"How much to add (default 1)\"}"
        "},\"required\":[]}}]";
    *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL char g_tool_buf[256];

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;

    if (strcmp(name, "demo_counter") == 0) {
        snprintf(g_tool_buf, sizeof(g_tool_buf),
            "{\"counter\":%d,\"clicked\":%d,\"auto_tick\":%d}",
            g_counter, g_clicked, g_auto_tick);
        *out = g_tool_buf;
        return SC_OK;
    }

    if (strcmp(name, "demo_bump") == 0) {
        int amount = json_get_int(args, "amount", 1);
        if (amount == 0) amount = 1;
        g_counter += amount;

        // push immediate widget update to webui
        if (g_core && g_core->widget_emit) {
            char upd[256];
            snprintf(upd, sizeof(upd),
                "{\"counter\":%d,\"clicked\":%d,\"auto_tick\":%d}",
                g_counter, g_clicked, g_auto_tick);
            g_core->widget_emit("demowidget.counter", upd);
        }

        snprintf(g_tool_buf, sizeof(g_tool_buf),
            "{\"status\":\"ok\",\"counter\":%d,\"added\":%d}",
            g_counter, amount);
        *out = g_tool_buf;
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

// hooks
static sc_status_t on_custom_event(void* ud, const char* name,
                                    const char* payload, char** out) {
    (void)ud; (void)name; (void)payload; (void)out;
    return SC_OK;
}

static const sc_hook_reg_t g_hooks[] = {
    {
        .name = "on_custom_event",
        .fn = on_custom_event,
        .userdata = nullptr,
        .priority = 0,
        ._reserved = 0,
    },
};

static sc_status_t get_hooks(const sc_hook_reg_t** out, size_t* count) {
    if (!out || !count) return SC_ERR_INVALID_ARG;
    *out = g_hooks;
    *count = sizeof(g_hooks) / sizeof(g_hooks[0]);
    return SC_OK;
}

// registration
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
    .on_shutdown = on_shutdown,
    .get_widgets = get_widgets,
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}