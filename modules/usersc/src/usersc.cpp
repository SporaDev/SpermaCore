/* usersc.cpp - SpermaCore addon: tools for querying Telegram users.
 *
 * provides get_user and resolve_username tools via tg.* syscalls.
 */
#include "plugin_api.h"
#include "json.hpp"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <string>

static const sc_core_api_t* g_core = nullptr;

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
        .name = "usersc",
        .version = "1.2.0",
        .description = "Telegram user lookup: get_user, resolve_username",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;
    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "usersc", "usersc plugin initialized");
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
        "{\"name\":\"get_user\","
        "\"description\":\"Look up a Telegram user by ID or username. Returns chat_id (same as user_id), username, first_name, last_name, bio, phone (if in contacts).\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"user_id\":{\"type\":\"integer\",\"description\":\"Numeric user ID\"},"
        "\"username\":{\"type\":\"string\",\"description\":\"@username with or without @\"}"
        "},\"required\":[]}},"
        "{\"name\":\"resolve_username\","
        "\"description\":\"Resolve @username to user info (id, name, bio). Faster than get_user when you only have the handle.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"username\":{\"type\":\"string\",\"description\":\"Username with or without @\"}"
        "},\"required\":[\"username\"]}}"
        "]";
    *out = (char*)tools;
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * call_tool
 * ------------------------------------------------------------------- */
static SC_THREAD_LOCAL std::string g_result_str;

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;

    if (!g_core || !g_core->syscall) {
        scjson::Value err = scjson::make_object();
        err.set("status", scjson::make_string("error"));
        err.set("reason", scjson::make_string("syscall not available"));
        g_result_str = scjson::dump(err);
        *out = (char*)g_result_str.c_str();
        return SC_OK;
    }

    scjson::Value parsed_args;
    if (!scjson::parse(args ? args : "{}", parsed_args)) {
        scjson::Value err = scjson::make_object();
        err.set("status", scjson::make_string("error"));
        err.set("reason", scjson::make_string("invalid JSON arguments"));
        g_result_str = scjson::dump(err);
        *out = (char*)g_result_str.c_str();
        return SC_OK;
    }

    /* ---- get_user ---- */
    if (strcmp(name, "get_user") == 0) {
        long long user_id = scjson::num(&parsed_args, "user_id", 0);
        std::string username = scjson::str(&parsed_args, "username");

        if (user_id == 0 && username.empty()) {
            scjson::Value err = scjson::make_object();
            err.set("status", scjson::make_string("error"));
            err.set("reason", scjson::make_string("user_id or username required"));
            g_result_str = scjson::dump(err);
            *out = (char*)g_result_str.c_str();
            return SC_OK;
        }

        char* result = nullptr;
        int rc = 0;

        if (user_id > 0) {
            scjson::Value sreq = scjson::make_object();
            sreq.set("user_id", scjson::make_int(user_id));
            rc = call_syscall("tg.get_user", scjson::dump(sreq).c_str(), &result);
        } else {
            if (username[0] == '@') {
                username = username.substr(1);
            }
            scjson::Value sreq = scjson::make_object();
            sreq.set("username", scjson::make_string(username));
            rc = call_syscall("tg.resolve_username", scjson::dump(sreq).c_str(), &result);
        }

        if (rc != 0 || !result) {
            if (result) g_core->free(result);
            scjson::Value err = scjson::make_object();
            err.set("status", scjson::make_string("error"));
            err.set("reason", scjson::make_string("lookup failed"));
            g_result_str = scjson::dump(err);
            *out = (char*)g_result_str.c_str();
            return SC_OK;
        }

        g_result_str = result;
        g_core->free(result);
        *out = (char*)g_result_str.c_str();
        return SC_OK;
    }

    /* ---- resolve_username ---- */
    if (strcmp(name, "resolve_username") == 0) {
        std::string username = scjson::str(&parsed_args, "username");

        if (username.empty()) {
            scjson::Value err = scjson::make_object();
            err.set("status", scjson::make_string("error"));
            err.set("reason", scjson::make_string("username required"));
            g_result_str = scjson::dump(err);
            *out = (char*)g_result_str.c_str();
            return SC_OK;
        }

        if (username[0] == '@') {
            username = username.substr(1);
        }

        scjson::Value sreq = scjson::make_object();
        sreq.set("username", scjson::make_string(username));
        char* result = nullptr;
        int rc = call_syscall("tg.resolve_username", scjson::dump(sreq).c_str(), &result);

        if (rc != 0 || !result) {
            if (result) g_core->free(result);
            scjson::Value err = scjson::make_object();
            err.set("status", scjson::make_string("error"));
            err.set("reason", scjson::make_string("resolve failed"));
            g_result_str = scjson::dump(err);
            *out = (char*)g_result_str.c_str();
            return SC_OK;
        }

        g_result_str = result;
        g_core->free(result);
        *out = (char*)g_result_str.c_str();
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
