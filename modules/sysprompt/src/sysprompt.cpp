/* sysprompt.cpp - SpermaCore addon: system prompt from file.
 *
 * reads data/sysprompt.txt and provides it via get_system_prompt().
 */
#include "plugin_api.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#ifdef _WIN32
#include <direct.h>
#else
#include <sys/stat.h>
#include <sys/types.h>
#endif

#define DEFAULT_PROMPT_PATH "data/sysprompt.txt"
static char g_prompt_path[512] = DEFAULT_PROMPT_PATH;

static const sc_core_api_t* g_core = nullptr;
static char* g_prompt = nullptr;

/* default prompt written on initial run */
static const char* DEFAULT_PROMPT =
    "You are SpermaCore, a modular AI assistant.\n"
    "You speak concisely, warmly, and without filler.\n"
    "You use tools whenever a tool can answer the question.\n"
    "You never invent data a tool can return.\n"
    "\n"
    "# Edit this file (data/sysprompt.txt) to change your agent's persona.\n"
    "# The file is re-read on each startup.\n";

/* ---------------------------------------------------------------------
 * file utilities
 * ------------------------------------------------------------------- */

static char* read_file(const char* path, size_t* out_size) {
    FILE* f = fopen(path, "rb");
    if (!f) return nullptr;

    if (fseek(f, 0, SEEK_END) != 0) { fclose(f); return nullptr; }
    long sz = ftell(f);
    if (sz < 0) { fclose(f); return nullptr; }
    if (fseek(f, 0, SEEK_SET) != 0) { fclose(f); return nullptr; }

    char* buf = (char*)malloc((size_t)sz + 1);
    if (!buf) { fclose(f); return nullptr; }

    size_t rd = fread(buf, 1, (size_t)sz, f);
    buf[rd] = 0;  /* null terminate buffer */
    fclose(f);

    if (out_size) *out_size = rd;
    return buf;
}

/* write_file returns 0 on success, -1 on error */
static int write_file(const char* path, const char* content) {
    FILE* f = fopen(path, "wb");
    if (!f) {
        char dir[512];
        snprintf(dir, sizeof(dir), "%s", path);
        char* slash = strrchr(dir, '/');
        if (!slash) slash = strrchr(dir, '\\');
        if (slash) {
            *slash = 0;
#ifdef _WIN32
            _mkdir(dir);
#else
            mkdir(dir, 0755);
#endif
            f = fopen(path, "wb");
        }
    }
    if (!f) return -1;
    size_t n = strlen(content);
    size_t w = fwrite(content, 1, n, f);
    fclose(f);
    return (w == n) ? 0 : -1;
}

/* ---------------------------------------------------------------------
 * logging via core
 * ------------------------------------------------------------------- */

static void log_msg(int level, const char* msg) {
    if (g_core && g_core->log) g_core->log(level, "sysprompt", msg);
}

/* ---------------------------------------------------------------------
 * prompt loading
 * ------------------------------------------------------------------- */

static void load_prompt(void) {
    if (g_prompt) {
        free(g_prompt);
        g_prompt = nullptr;
    }

    size_t sz = 0;
    char* buf = read_file(g_prompt_path, &sz);

    if (buf) {
        g_prompt = buf;
        if (sz > 0) {
            char msg[128];
            snprintf(msg, sizeof(msg), "loaded system prompt (%llu bytes)",
                     (unsigned long long)sz);
            log_msg(SC_LOG_INFO, msg);
        } else {
            log_msg(SC_LOG_INFO, "system prompt file is empty - using none");
        }
        return;
    }

    /* write default template if file does not exist */
    if (write_file(g_prompt_path, DEFAULT_PROMPT) == 0) {
        log_msg(SC_LOG_INFO, "created data/sysprompt.txt with default template");
    } else {
        log_msg(SC_LOG_WARN, "cannot write data/sysprompt.txt, using builtin default");
    }

    /* fallback to duplicate of default prompt */
    g_prompt = strdup(DEFAULT_PROMPT);
}

/* ---------------------------------------------------------------------
 * plugin abi
 * ------------------------------------------------------------------- */

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name = "sysprompt",
        .version = "1.1.0",
        .description = "Injects a system prompt from data/sysprompt.txt",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (g_core && g_core->data_dir) {
        char* d = (char*)g_core->data_dir();
        if (d && d[0]) {
            snprintf(g_prompt_path, sizeof(g_prompt_path), "%s/sysprompt.txt", d);
            if (g_core->free) g_core->free(d);
        }
    }
    load_prompt();
    return SC_OK;
}

static sc_status_t deinit(void) {
    if (g_prompt) {
        free(g_prompt);
        g_prompt = nullptr;
    }
    /* clear core pointer on deinit */
    g_core = nullptr;
    return SC_OK;
}

static sc_status_t get_hooks(const sc_hook_reg_t** o, size_t* c) {
    if (o) *o = nullptr;
    if (c) *c = 0;
    return SC_OK;
}

/* no tools provided */
static sc_status_t get_tools(char** out) {
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    (void)name; (void)args;
    if (out) *out = nullptr;
    return SC_ERR_NOT_FOUND;
}

/* reload prompt file on startup */
static sc_status_t on_startup(void) {
    load_prompt();
    if (g_prompt && *g_prompt) {
        char msg[128];
        snprintf(msg, sizeof(msg), "active, prompt length = %llu",
                 (unsigned long long)strlen(g_prompt));
        log_msg(SC_LOG_INFO, msg);
    }
    return SC_OK;
}

/* get_system_prompt returns pointer to active prompt buffer */
static sc_status_t get_system_prompt(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    if (!g_prompt || !*g_prompt) {
        *out = nullptr;
        return SC_ERR_NOT_IMPLEMENTED;
    }
    *out = g_prompt;
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
    .on_startup = on_startup,
    .get_system_prompt = get_system_prompt,
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}
