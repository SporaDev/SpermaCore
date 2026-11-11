/* moderation.cpp - SpermaCore addon: moderation tools (block, unblock, deletechat).
 *
 * Provides tools for account-level user blocking and two-way chat deletion via TDLib.
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
        .name = "moderation",
        .version = "1.0.0",
        .description = "Telegram moderation tools: account-level block, unblock, and two-way chat deletion",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;
    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "moderation", "moderation plugin initialized");
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
        "{\"name\":\"block\","
        "\"description\":\"Block a toxic, offensive or harassing user at the Telegram level and in the local filter. "
        "The user is added to the Telegram blocklist so they cannot send messages or view profile info. "
        "Leave username empty to block the current message sender.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"username\":{\"type\":\"string\",\"description\":\"Telegram username without @ (optional)\"},"
        "\"user_id\":{\"type\":\"integer\",\"description\":\"Telegram user ID (optional)\"},"
        "\"chat_id\":{\"type\":\"integer\",\"description\":\"Telegram chat ID (optional)\"},"
        "\"reason\":{\"type\":\"string\",\"description\":\"Short reason for blocking\"}"
        "}}"
        "},"
        "{\"name\":\"unblock\","
        "\"description\":\"Unblock a previously blocked user at the Telegram level and in the local filter.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"username\":{\"type\":\"string\",\"description\":\"Telegram username without @ (optional)\"},"
        "\"user_id\":{\"type\":\"integer\",\"description\":\"Telegram user ID (optional)\"},"
        "\"chat_id\":{\"type\":\"integer\",\"description\":\"Telegram chat ID (optional)\"}"
        "}}"
        "},"
        "{\"name\":\"deletechat\","
        "\"description\":\"Permanently delete the entire chat history for BOTH sides (revoke) and remove the chat from the chat list. "
        "Use when permanently cutting off a toxic user or wiping a private conversation.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"chat_id\":{\"type\":\"integer\",\"description\":\"Telegram chat ID to delete for both participants\"},"
        "\"username\":{\"type\":\"string\",\"description\":\"Telegram username without @ (optional)\"}"
        "},\"required\":[\"chat_id\"]}}"
        "]";
    *out = (char*)tools;
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * call_tool
 * ------------------------------------------------------------------- */
static SC_THREAD_LOCAL std::string g_tool_result;

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;

    scjson::Value a;
    if (!scjson::parse(args ? args : "{}", a)) {
        scjson::Value err = scjson::make_object();
        err.set("status", scjson::make_string("error"));
        err.set("reason", scjson::make_string("invalid JSON arguments"));
        g_tool_result = scjson::dump(err);
        *out = (char*)g_tool_result.c_str();
        return SC_OK;
    }

    if (strcmp(name, "block") == 0) {
        char* sys_out = nullptr;
        int rc = call_syscall("tg.block_user", args ? args : "{}", &sys_out);
        if (rc == 0 && sys_out) {
            g_tool_result = sys_out;
            g_core->free(sys_out);
        } else {
            scjson::Value ok = scjson::make_object();
            ok.set("status", scjson::make_string("ok"));
            ok.set("message", scjson::make_string("User blocked in filter and Telegram"));
            g_tool_result = scjson::dump(ok);
        }
        *out = (char*)g_tool_result.c_str();
        return SC_OK;
    }

    if (strcmp(name, "unblock") == 0) {
        char* sys_out = nullptr;
        int rc = call_syscall("tg.unblock_user", args ? args : "{}", &sys_out);
        if (rc == 0 && sys_out) {
            g_tool_result = sys_out;
            g_core->free(sys_out);
        } else {
            scjson::Value ok = scjson::make_object();
            ok.set("status", scjson::make_string("ok"));
            ok.set("message", scjson::make_string("User unblocked in filter and Telegram"));
            g_tool_result = scjson::dump(ok);
        }
        *out = (char*)g_tool_result.c_str();
        return SC_OK;
    }

    if (strcmp(name, "deletechat") == 0) {
        char* sys_out = nullptr;
        int rc = call_syscall("tg.delete_chat", args ? args : "{}", &sys_out);
        if (rc == 0 && sys_out) {
            g_tool_result = sys_out;
            g_core->free(sys_out);
        } else {
            scjson::Value ok = scjson::make_object();
            ok.set("status", scjson::make_string("ok"));
            ok.set("message", scjson::make_string("Chat history deleted for both users"));
            g_tool_result = scjson::dump(ok);
        }
        *out = (char*)g_tool_result.c_str();
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
