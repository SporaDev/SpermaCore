/* embedsc.cpp - SpermaCore addon: per-chat semantic memory tools.
 *
 * provides recall_semantic and embedding_stats via embedsc.* syscalls.
 */
#include "plugin_api.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const sc_core_api_t* g_core = nullptr;

/* ---------------------------------------------------------------------
 * json parsing helpers
 * ------------------------------------------------------------------- */

static long long json_get_int64(const char* json, const char* field, long long dflt) {
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
    long long v = 0;
    while (*p >= '0' && *p <= '9') { v = v * 10 + (*p - '0'); p++; }
    return neg ? -v : v;
}

static int json_get_string(const char* json, const char* field,
                           char* out, size_t outsz) {
    if (!json || !field || !out || outsz == 0) return 0;
    char pattern[128];
    snprintf(pattern, sizeof(pattern), "\"%s\"", field);
    const char* p = strstr(json, pattern);
    if (!p) return 0;
    p += strlen(pattern);
    while (*p && *p != ':') p++;
    if (*p != ':') return 0;
    p++;
    while (*p == ' ' || *p == '\t') p++;
    if (*p != '"') return 0;
    p++;
    size_t i = 0;
    while (*p && *p != '"' && i + 1 < outsz) {
        if (*p == '\\' && *(p + 1)) {
            p++;
            switch (*p) {
                case 'n': out[i++] = '\n'; break;
                case 't': out[i++] = '\t'; break;
                case 'r': out[i++] = '\r'; break;
                case '"': out[i++] = '"';  break;
                case '\\':out[i++] = '\\'; break;
                case '/': out[i++] = '/';  break;
                default:  out[i++] = *p;   break;
            }
            p++;
        } else {
            out[i++] = *p++;
        }
    }
    out[i] = 0;
    return 1;
}

/* json_escape escapes quotes and special characters for json */
static int json_escape(const char* src, char* dst, size_t dstsz) {
    size_t j = 0;
    if (dstsz == 0) return 0;
    for (size_t i = 0; src[i]; i++) {
        if (j + 7 >= dstsz) { dst[j] = 0; return 0; }
        unsigned char c = (unsigned char)src[i];
        if (c == '"' || c == '\\') {
            dst[j++] = '\\';
            dst[j++] = (char)c;
        } else if (c == '\n') {
            dst[j++] = '\\'; dst[j++] = 'n';
        } else if (c == '\r') {
            dst[j++] = '\\'; dst[j++] = 'r';
        } else if (c == '\t') {
            dst[j++] = '\\'; dst[j++] = 't';
        } else if (c < 0x20) {
            j += (size_t)snprintf(dst + j, 7, "\\u%04x", c);
        } else {
            dst[j++] = (char)c;
        }
    }
    dst[j] = 0;
    return 1;
}

/* ---------------------------------------------------------------------
 * syscall wrapper with automatic memory management
 * ------------------------------------------------------------------- */
static int call_syscall(const char* method, const char* args_json, char** out_result) {
    if (!g_core || !g_core->syscall) return -1;
    char* result = nullptr;
    int rc = g_core->syscall(method, args_json, &result);
    if (out_result) {
        *out_result = result;
    } else if (result) {
        g_core->free(result);
    }
    return rc;
}

/* ---------------------------------------------------------------------
 * metadata and lifecycle
 * ------------------------------------------------------------------- */

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name = "embedsc",
        .version = "1.1.0",
        .description = "Per-chat semantic memory via Ollama embeddings",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;
    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "embedsc", "embedsc plugin initialized");
    }
    return SC_OK;
}

static sc_status_t deinit(void) { return SC_OK; }

static sc_status_t get_hooks(const sc_hook_reg_t** o, size_t* c) {
    if (o) *o = nullptr;
    if (c) *c = 0;
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * tool declarations
 * ------------------------------------------------------------------- */
static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "["
        "{\"name\":\"recall_semantic\","
        "\"description\":\"Semantic search over this chat's past messages. "
        "Finds messages that are conceptually related to the query, not by exact words. "
        "Use when you want to remember what you or the user talked about before - "
        "topics, moods, stories, preferences. The search is meaning-based, so "
        "'what did i say about coffee' works even if the word 'coffee' was not used.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"chat_id\":{\"type\":\"integer\",\"description\":\"Current chat ID from context block\"},"
        "\"query\":{\"type\":\"string\",\"description\":\"What to search for, in natural language\"},"
        "\"top_k\":{\"type\":\"integer\",\"description\":\"How many results (default 5)\"}"
        "},\"required\":[\"chat_id\",\"query\"]}},"
        "{\"name\":\"embedding_stats\","
        "\"description\":\"Show how many messages are stored in this chat's semantic memory and which embedding model is used.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"chat_id\":{\"type\":\"integer\"}"
        "},\"required\":[\"chat_id\"]}}"
        "]";
    *out = (char*)tools;
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * response buffers
 * ------------------------------------------------------------------- */
static SC_THREAD_LOCAL char g_result[65536];
static SC_THREAD_LOCAL char g_tmp[131072];

/* ---------------------------------------------------------------------
 * call_tool
 * ------------------------------------------------------------------- */

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;

    if (!g_core || !g_core->syscall) {
        snprintf(g_result, sizeof(g_result),
            "{\"status\":\"error\",\"reason\":\"syscall not available\"}");
        *out = g_result;
        return SC_OK;
    }

    /* ---- recall_semantic ---- */
    if (strcmp(name, "recall_semantic") == 0) {
        long long chat_id = json_get_int64(args, "chat_id", 0);
        char query[2048] = {0};
        json_get_string(args, "query", query, sizeof(query));
        long long top_k = json_get_int64(args, "top_k", 5);

        if (chat_id == 0 || !query[0]) {
            snprintf(g_result, sizeof(g_result),
                "{\"status\":\"error\",\"reason\":\"chat_id and query required\"}");
            *out = g_result;
            return SC_OK;
        }

        /* escape query string for json payload */
        static SC_THREAD_LOCAL char esc[4096];
        json_escape(query, esc, sizeof(esc));

        static SC_THREAD_LOCAL char sargs[5120];
        snprintf(sargs, sizeof(sargs),
            "{\"chat_id\":%lld,\"query\":\"%s\",\"top_k\":%lld}",
            chat_id, esc, top_k);

        char* result = nullptr;
        int rc = call_syscall("embedsc.recall", sargs, &result);

        if (rc != 0 || !result) {
            if (result) g_core->free(result);
            snprintf(g_result, sizeof(g_result),
                "{\"status\":\"error\",\"reason\":\"recall failed, rc=%d\"}", rc);
            *out = g_result;
            return SC_OK;
        }

        /* copy result into g_tmp buffer and free allocated memory */
        size_t rlen = strlen(result);
        if (rlen >= sizeof(g_tmp)) rlen = sizeof(g_tmp) - 1;
        memcpy(g_tmp, result, rlen);
        g_tmp[rlen] = 0;
        g_core->free(result);

        *out = g_tmp;
        return SC_OK;
    }

    /* ---- embedding_stats ---- */
    if (strcmp(name, "embedding_stats") == 0) {
        long long chat_id = json_get_int64(args, "chat_id", 0);

        static SC_THREAD_LOCAL char sargs[128];
        snprintf(sargs, sizeof(sargs), "{\"chat_id\":%lld}", chat_id);

        char* result = nullptr;
        int rc = call_syscall("embedsc.stats", sargs, &result);

        if (rc != 0 || !result) {
            if (result) g_core->free(result);
            snprintf(g_result, sizeof(g_result),
                "{\"status\":\"error\",\"reason\":\"stats failed, rc=%d\"}", rc);
            *out = g_result;
            return SC_OK;
        }

        snprintf(g_result, sizeof(g_result), "%s", result);
        g_core->free(result);
        *out = g_result;
        return SC_OK;
    }

    /* ---- on_embedsc_event (internal) ---- */
    if (strcmp(name, "on_embedsc_event") == 0) {
        if (g_core->emit_event && args) {
            g_core->emit_event(args);
        }
        snprintf(g_result, sizeof(g_result), "{\"status\":\"ok\"}");
        *out = g_result;
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
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
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}
