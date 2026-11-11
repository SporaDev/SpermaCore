/* time addon: current time tool for llm */
#include "plugin_api.h"
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <time.h>

static const sc_core_api_t* g_core = nullptr;

// json helpers

// escape string into quotes
static size_t json_put_string(char* out, size_t outsz, const char* s) {
    size_t i = 0;
    if (i < outsz) out[i++] = '"';
    if (!s) s = "";
    for (; *s && i + 8 < outsz; s++) {
        unsigned char c = (unsigned char)*s;
        switch (c) {
            case '"':  out[i++]='\\'; out[i++]='"';  break;
            case '\\': out[i++]='\\'; out[i++]='\\'; break;
            case '\n': out[i++]='\\'; out[i++]='n';  break;
            case '\r': out[i++]='\\'; out[i++]='r';  break;
            case '\t': out[i++]='\\'; out[i++]='t';  break;
            default:
                if (c < 0x20) {
                    i += (size_t)snprintf(out + i, 8, "\\u%04x", c);
                } else {
                    out[i++] = (char)c;
                }
        }
    }
    if (i < outsz) out[i++] = '"';
    if (i < outsz) out[i] = 0;
    return i;
}

// get boolean field, handles string booleans too
static int json_get_bool(const char* json, const char* field, int default_value) {
    if (!json || !field) return default_value;
    char pattern[128];
    snprintf(pattern, sizeof(pattern), "\"%s\"", field);
    const char* p = strstr(json, pattern);
    if (!p) return default_value;
    p += strlen(pattern);
    while (*p && *p != ':') p++;
    if (*p != ':') return default_value;
    p++;
    while (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n') p++;

    if (strncmp(p, "true", 4) == 0)  return 1;
    if (strncmp(p, "false", 5) == 0) return 0;
    if (*p == '"') {
        p++;
        if (strncmp(p, "true", 4) == 0)  return 1;
        if (strncmp(p, "false", 5) == 0) return 0;
    }
    return default_value;
}

// format time string
static void format_time(int use_utc, char* out, size_t outsz) {
    time_t now = time(nullptr);
    struct tm tmv;
#if defined(_WIN32)
    if (use_utc) gmtime_s(&tmv, &now);
    else         localtime_s(&tmv, &now);
#else
    if (use_utc) gmtime_r(&now, &tmv);
    else         localtime_r(&now, &tmv);
#endif
    snprintf(out, outsz, "%04d-%02d-%02d %02d:%02d:%02d",
             tmv.tm_year + 1900, tmv.tm_mon + 1, tmv.tm_mday,
             tmv.tm_hour, tmv.tm_min, tmv.tm_sec);
}

// get utc offset in seconds
static long get_utc_offset_seconds(void) {
    time_t now = time(nullptr);
#if defined(_WIN32)
    // windows offset via mktime difference
    struct tm g, l;
    gmtime_s(&g, &now);
    localtime_s(&l, &now);
    g.tm_isdst = 0;
    long diff_g = (long)mktime(&g);
    long diff_l = (long)mktime(&l);
    return diff_l - diff_g;
#else
    struct tm g;
    gmtime_r(&now, &g);
    return g.tm_gmtoff;
#endif
}

// format time payload json
static void build_time_json(int use_utc, char* out, size_t outsz) {
    char ts[32];
    format_time(use_utc, ts, sizeof(ts));

    long offset_sec = get_utc_offset_seconds();

    int off_h = (int)(offset_sec / 3600);
    int off_m = (int)((offset_sec % 3600) / 60);
    if (off_m < 0) off_m = -off_m;

    size_t i = 0;
    i += (size_t)snprintf(out + i, outsz - i, "{\"time\":");
    i += json_put_string(out + i, outsz - i, ts);
    i += (size_t)snprintf(out + i, outsz - i,
                          ",\"timezone\":\"%c%02d:%02d\",\"timestamp\":%ld}",
                          offset_sec >= 0 ? '+' : '-',
                          off_h < 0 ? -off_h : off_h,
                          off_m,
                          (long)time(nullptr));
}

// plugin abi

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name = "time",
        .version = "1.1.0",
        .description = "Provides current time to the model",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;
    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "time", "time plugin initialized");
    }
    return SC_OK;
}

static sc_status_t deinit(void) {
    g_core = nullptr;
    return SC_OK;
}

static sc_status_t get_hooks(const sc_hook_reg_t** out_hooks, size_t* count) {
    if (out_hooks) *out_hooks = nullptr;
    if (count)     *count = 0;
    return SC_OK;
}

// tool definitions
static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "[{\"name\":\"get_time\","
        "\"description\":\"Get the current date and time. Returns a formatted timestamp "
        "(YYYY-MM-DD HH:MM:SS), timezone offset, and unix timestamp. "
        "By default returns UTC - pass utc=false to get local time. "
        "Use this whenever you need to know today's date, current time, or day of week.\","
        "\"parameters\":{\"type\":\"object\","
        "\"properties\":{"
        "\"utc\":{\"type\":\"boolean\",\"description\":\"If true (default) returns UTC, otherwise local time\"}"
        "},"
        "\"required\":[]}}]";
    *out = (char*)tools;
    return SC_OK;
}

// response buffer
static SC_THREAD_LOCAL char g_result[256];

// call tool
static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;

    if (strcmp(name, "get_time") != 0) return SC_ERR_NOT_FOUND;

    // default to utc unless explicitly requested
    int use_utc = json_get_bool(args, "utc", 1);

    build_time_json(use_utc, g_result, sizeof(g_result));
    *out = g_result;
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
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}