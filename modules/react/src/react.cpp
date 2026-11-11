/* react.cpp - SpermaCore addon: emoji reactions on Telegram messages.
 *
 * forwards reaction requests via tg.set_reaction with fallback to tgbot.set_reaction.
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
        .name = "react",
        .version = "1.2.0",
        .description = "Emoji reactions on Telegram messages (userbot + bot fallback)",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;
    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "react", "react plugin initialized");
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
        "{\"name\":\"react\","
        "\"description\":\"Set an emoji reaction on a Telegram message. "
        "Use this INSTEAD of send_message when the appropriate response is "
        "a short emotional reaction rather than text. "
        "Use for: ❤ 👍 🔥 😂 🤔 😍 👌 🎉 💯 🤯 😎 💀 😭 🥰 😴 and similar. "
        "Premium custom emoji are supported too: pass their numeric id as "
        "\\\"custom:<id>\\\" (e.g. custom:5368324170671202286) or \\\"id:<id>\\\". "
        "NEVER send a text message that consists only of emoji - react instead. "
        "If the incoming message has no question and no info request, and "
        "a single emoji would express your reaction - use this tool.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{"
        "\"chat_id\":{\"type\":\"integer\",\"description\":\"Telegram chat ID\"},"
        "\"message_id\":{\"type\":\"integer\",\"description\":\"ID of the message to react to\"},"
        "\"emoji\":{\"type\":\"string\",\"description\":\"Single emoji character (e.g. ❤, 👍, 🔥); for a Telegram Premium custom emoji use \\\"custom:<numeric id>\\\"\"}"
        "},\"required\":[\"chat_id\",\"message_id\",\"emoji\"]}}"
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

    if (strcmp(name, "react") != 0) return SC_ERR_NOT_FOUND;

    scjson::Value a;
    if (!scjson::parse(args ? args : "{}", a)) {
        scjson::Value err = scjson::make_object();
        err.set("status", scjson::make_string("error"));
        err.set("reason", scjson::make_string("invalid JSON arguments"));
        g_tool_result = scjson::dump(err);
        *out = (char*)g_tool_result.c_str();
        return SC_OK;
    }

    long long chat_id    = scjson::num(&a, "chat_id", 0);
    long long message_id = scjson::num(&a, "message_id", 0);
    std::string emoji    = scjson::str(&a, "emoji");

    if (chat_id == 0 || message_id == 0 || emoji.empty()) {
        scjson::Value err = scjson::make_object();
        err.set("status", scjson::make_string("error"));
        err.set("reason", scjson::make_string("chat_id, message_id and emoji are required"));
        g_tool_result = scjson::dump(err);
        *out = (char*)g_tool_result.c_str();
        return SC_OK;
    }

    if (!g_core || !g_core->syscall) {
        scjson::Value err = scjson::make_object();
        err.set("status", scjson::make_string("error"));
        err.set("reason", scjson::make_string("syscall not available"));
        g_tool_result = scjson::dump(err);
        *out = (char*)g_tool_result.c_str();
        return SC_OK;
    }

    scjson::Value sreq = scjson::make_object();
    sreq.set("chat_id", scjson::make_int(chat_id));
    sreq.set("message_id", scjson::make_int(message_id));
    sreq.set("emoji", scjson::make_string(emoji));
    std::string sargs = scjson::dump(sreq);

    /* try userbot transport first, then bot transport on a hard failure */
    std::string last_reason;
    const char* methods[] = {"tg.set_reaction", "tgbot.set_reaction"};
    for (const char* method : methods) {
        char* res = nullptr;
        int rc = call_syscall(method, sargs.c_str(), &res);
        std::string payload;
        if (res) {
            payload.assign(res);
            g_core->free(res);
        }
        /* A non-zero rc or a status=error payload both mean failure; the
         * syscall rc alone is not enough because the plugin returns SC_OK even
         * when TDLib rejects the request. */
        bool ok = (rc == 0);
        scjson::Value parsed;
        if (!payload.empty() && scjson::parse(payload, parsed)) {
            std::string status = scjson::str(&parsed, "status");
            if (!status.empty() && status != "ok") {
                ok = false;
                std::string reason = scjson::str(&parsed, "reason");
                if (reason.empty()) reason = status;
                last_reason = reason;
            }
        }
        if (ok && rc != 0) last_reason = "syscall failed";
        if (ok) {
            scjson::Value okv = scjson::make_object();
            okv.set("status", scjson::make_string("ok"));
            okv.set("emoji", scjson::make_string(emoji));
            g_tool_result = scjson::dump(okv);
            *out = (char*)g_tool_result.c_str();
            return SC_OK;
        }
        if (last_reason.empty()) last_reason = "reaction failed";
    }

    scjson::Value err = scjson::make_object();
    err.set("status", scjson::make_string("error"));
    err.set("reason", scjson::make_string(last_reason));
    g_tool_result = scjson::dump(err);
    *out = (char*)g_tool_result.c_str();
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
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}
