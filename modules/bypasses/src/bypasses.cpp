/* bypasses.cpp - SpermaCore ABI 1.7: Pure native network bypass and proxy management module.
 *
 * Implements:
 *   1. [bypass] config reader (tgwsproxy = true/false).
 *   2. Interactive console prompt on startup for proxy link or manual configuration.
 *   3. Global process environment bypass (HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, TG_PROXY).
 *   4. Telegram proxy routing via "tg.set_proxy" syscall and environment injection.
 *   5. 15-second connectivity watchdog monitoring Telegram via "tg.is_connected" syscall.
 *   6. LLM tools (bypass_status, bypass_configure) and syscalls (bypass.status, bypass.apply).
 */

#include "plugin_api.h"
#include "json.hpp"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#endif

#include <algorithm>
#include <atomic>
#include <chrono>
#include <mutex>
#include <string>
#include <thread>

static const sc_core_api_t* g_core = nullptr;

static std::mutex g_bypass_mu;
static std::atomic<bool> g_tgwsproxy{false};
static std::atomic<bool> g_enabled{false};
static std::atomic<bool> g_watchdog_done{false};

static std::string g_active_proxy;
static std::string g_proxy_type = "socks5";
static std::string g_proxy_host = "127.0.0.1";
static int g_proxy_port = 10808;

static void log_bypass(int level, const char* msg) {
    if (g_core && g_core->log) {
        g_core->log(level, "bypasses", msg);
    }
}

// ---------------------------------------------------------------------
// String & Environment helpers
// ---------------------------------------------------------------------

static void trim_line(char* s) {
    if (!s) return;
    size_t len = strlen(s);
    while (len > 0 && (s[len - 1] == '\r' || s[len - 1] == '\n' || s[len - 1] == ' ' || s[len - 1] == '\t')) {
        s[--len] = 0;
    }
}

static void set_env_var(const char* name, const char* value) {
#if defined(_WIN32)
    SetEnvironmentVariableA(name, value);
    std::string s = std::string(name) + "=" + (value ? value : "");
    _putenv(s.c_str());
#else
    if (value && value[0]) {
        setenv(name, value, 1);
    } else {
        unsetenv(name);
    }
#endif
}

// ---------------------------------------------------------------------
// Proxy URL Parsing
// ---------------------------------------------------------------------

struct ParsedProxy {
    std::string type = "socks5";
    std::string host = "127.0.0.1";
    int port = 10808;
    std::string user;
    std::string pass;
    std::string secret;
};

static std::string extract_query_param(const std::string& url, const std::string& key) {
    std::string needle = key + "=";
    size_t pos = url.find(needle);
    if (pos == std::string::npos) return "";
    pos += needle.length();
    size_t end = url.find('&', pos);
    return url.substr(pos, end == std::string::npos ? std::string::npos : end - pos);
}

static bool parse_proxy_link(const std::string& raw_url, ParsedProxy& out) {
    std::string s = raw_url;
    // trim quotes and whitespace
    while (!s.empty() && (s.front() == ' ' || s.front() == '"' || s.front() == '\'')) s.erase(s.begin());
    while (!s.empty() && (s.back() == ' ' || s.back() == '"' || s.back() == '\'')) s.pop_back();
    if (s.empty()) return false;

    // Check tg:// or https://t.me/
    if (s.rfind("tg://", 0) == 0 || s.find("t.me/") != std::string::npos) {
        std::string srv = extract_query_param(s, "server");
        if (srv.empty()) srv = extract_query_param(s, "host");
        std::string prt = extract_query_param(s, "port");
        if (!srv.empty() && !prt.empty()) {
            out.host = srv;
            out.port = atoi(prt.c_str());
            out.secret = extract_query_param(s, "secret");
            if (!out.secret.empty()) {
                out.type = "mtproto";
            } else {
                out.user = extract_query_param(s, "user");
                if (out.user.empty()) out.user = extract_query_param(s, "username");
                out.pass = extract_query_param(s, "pass");
                if (out.pass.empty()) out.pass = extract_query_param(s, "password");
                out.type = "socks5";
            }
            return (out.port > 0 && out.port <= 65535);
        }
    }

    // Check standard scheme ://
    size_t scheme_pos = s.find("://");
    if (scheme_pos != std::string::npos) {
        std::string scheme = s.substr(0, scheme_pos);
        std::transform(scheme.begin(), scheme.end(), scheme.begin(), [](unsigned char c) { return (char)tolower(c); });
        std::string rest = s.substr(scheme_pos + 3);

        size_t at = rest.rfind('@');
        if (at != std::string::npos) {
            std::string userinfo = rest.substr(0, at);
            rest = rest.substr(at + 1);
            size_t colon = userinfo.find(':');
            out.user = userinfo.substr(0, colon);
            if (colon != std::string::npos) out.pass = userinfo.substr(colon + 1);
            if (scheme == "mtproto") out.secret = userinfo;
        }

        size_t slash = rest.find('/');
        if (slash != std::string::npos) rest = rest.substr(0, slash);

        size_t colon = rest.rfind(':');
        if (colon != std::string::npos) {
            out.host = rest.substr(0, colon);
            out.port = atoi(rest.c_str() + colon + 1);
        } else {
            out.host = rest;
            out.port = (scheme == "http" || scheme == "ws") ? 80 : (scheme == "https" || scheme == "wss" || scheme == "mtproto") ? 443 : 1080;
        }

        if (scheme == "socks" || scheme == "socks5") out.type = "socks5";
        else if (scheme == "http" || scheme == "https") out.type = "http";
        else if (scheme == "ws" || scheme == "wss") out.type = "ws";
        else if (scheme == "mtproto") out.type = "mtproto";
        else out.type = scheme;

        return (!out.host.empty() && out.port > 0 && out.port <= 65535);
    }

    // Plain host:port
    size_t colon = s.rfind(':');
    if (colon != std::string::npos) {
        out.host = s.substr(0, colon);
        out.port = atoi(s.c_str() + colon + 1);
        out.type = "socks5";
        return (!out.host.empty() && out.port > 0 && out.port <= 65535);
    }

    return false;
}

static std::string build_proxy_url(const ParsedProxy& p) {
    std::string url;
    if (p.type == "mtproto") {
        if (!p.secret.empty()) {
            url = "mtproto://" + p.secret + "@" + p.host + ":" + std::to_string(p.port);
        } else {
            url = "mtproto://" + p.host + ":" + std::to_string(p.port);
        }
    } else if (p.type == "http") {
        if (!p.user.empty() || !p.pass.empty()) {
            url = "http://" + p.user + ":" + p.pass + "@" + p.host + ":" + std::to_string(p.port);
        } else {
            url = "http://" + p.host + ":" + std::to_string(p.port);
        }
    } else if (p.type == "ws" || p.type == "wss") {
        url = "ws://" + p.host + ":" + std::to_string(p.port);
    } else { // socks5
        if (!p.user.empty() || !p.pass.empty()) {
            url = "socks5://" + p.user + ":" + p.pass + "@" + p.host + ":" + std::to_string(p.port);
        } else {
            url = "socks5://" + p.host + ":" + std::to_string(p.port);
        }
    }
    return url;
}

// ---------------------------------------------------------------------
// Apply proxy settings across process environment & Telegram
// ---------------------------------------------------------------------

static void apply_proxy_settings(const ParsedProxy& p) {
    std::string url = build_proxy_url(p);

    {
        std::lock_guard<std::mutex> lock(g_bypass_mu);
        g_active_proxy = url;
        g_proxy_type = p.type;
        g_proxy_host = p.host;
        g_proxy_port = p.port;
        g_enabled.store(true);
    }

    // Process-wide environment for updates, HTTP clients, LLM, curl
    set_env_var("HTTP_PROXY", url.c_str());
    set_env_var("HTTPS_PROXY", url.c_str());
    set_env_var("ALL_PROXY", url.c_str());
    set_env_var("http_proxy", url.c_str());
    set_env_var("https_proxy", url.c_str());
    set_env_var("all_proxy", url.c_str());
    set_env_var("TG_PROXY", url.c_str());

    // Notify Telegram via tg.set_proxy syscall if telegram plugin is loaded
    if (g_core && g_core->syscall) {
        scjson::Value v = scjson::make_object();
        v.set("proxy", scjson::make_string(url.c_str()));
        char* out = nullptr;
        g_core->syscall("tg.set_proxy", scjson::dump(v).c_str(), &out);
        if (out && g_core->free) g_core->free(out);
    }

    log_bypass(SC_LOG_INFO, ("proxy bypass active: " + url).c_str());
}

// ---------------------------------------------------------------------
// Console Prompt
// ---------------------------------------------------------------------

static void prompt_and_configure_proxy() {
    std::string default_url;
    {
        std::lock_guard<std::mutex> lock(g_bypass_mu);
        default_url = g_active_proxy;
    }

    printf("\n=======================================================\n");
    printf("[bypass] Telegram WS/Proxy bypass is active (tgwsproxy = true)\n");
    if (!default_url.empty()) {
        printf("Current configured proxy: %s\n", default_url.c_str());
        printf("Press Enter to keep it, or enter new proxy link / parameters:\n> ");
    } else {
        printf("Enter proxy link (e.g. tg://proxy?..., socks5://...)\n");
        printf("or press Enter to enter proxy parameters manually:\n> ");
    }
    fflush(stdout);

    char line[1024] = {0};
    if (!fgets(line, sizeof(line), stdin)) {
        if (!default_url.empty()) {
            printf("[bypass] Non-interactive console detected. Using configured proxy: %s\n", default_url.c_str());
            printf("=======================================================\n\n");
            return;
        }
        printf("[bypass] Non-interactive console detected. Using local fallback (127.0.0.1:10808 socks5).\n");
        ParsedProxy fallback;
        apply_proxy_settings(fallback);
        printf("=======================================================\n\n");
        return;
    }

    trim_line(line);

    if (line[0] == 0 && !default_url.empty()) {
        printf("[bypass] Using configured proxy: %s\n", default_url.c_str());
        printf("=======================================================\n\n");
        return;
    }

    ParsedProxy p;
    if (line[0] && strcmp(line, "m") != 0 && strcmp(line, "manual") != 0) {
        if (parse_proxy_link(line, p)) {
            apply_proxy_settings(p);
            printf("[bypass] Parsed %s proxy: %s:%d\n", p.type.c_str(), p.host.c_str(), p.port);
            printf("=======================================================\n\n");
            return;
        }
        printf("[bypass] Could not parse link. Falling back to manual entry.\n");
    }

    // Manual prompt
    printf("[bypass] Manual proxy configuration:\n");
    printf("Proxy Host [127.0.0.1]: ");
    fflush(stdout);
    char host_buf[256] = {0};
    if (fgets(host_buf, sizeof(host_buf), stdin)) {
        trim_line(host_buf);
    }
    p.host = host_buf[0] ? host_buf : "127.0.0.1";

    printf("Proxy Port [10808]: ");
    fflush(stdout);
    char port_buf[64] = {0};
    p.port = 10808;
    if (fgets(port_buf, sizeof(port_buf), stdin)) {
        trim_line(port_buf);
        if (port_buf[0]) p.port = atoi(port_buf);
    }
    if (p.port <= 0 || p.port > 65535) p.port = 10808;

    printf("Proxy Type [1=socks5, 2=http, 3=mtproto, 4=ws] (default: 1): ");
    fflush(stdout);
    char type_buf[64] = {0};
    p.type = "socks5";
    if (fgets(type_buf, sizeof(type_buf), stdin)) {
        trim_line(type_buf);
        if (strcmp(type_buf, "2") == 0 || strcmp(type_buf, "http") == 0) p.type = "http";
        else if (strcmp(type_buf, "3") == 0 || strcmp(type_buf, "mtproto") == 0) p.type = "mtproto";
        else if (strcmp(type_buf, "4") == 0 || strcmp(type_buf, "ws") == 0) p.type = "ws";
    }

    if (p.type == "mtproto") {
        printf("Secret (hex, optional): ");
        fflush(stdout);
        char sec_buf[256] = {0};
        if (fgets(sec_buf, sizeof(sec_buf), stdin)) {
            trim_line(sec_buf);
            p.secret = sec_buf;
        }
    } else if (p.type == "socks5" || p.type == "http") {
        printf("Username (optional): ");
        fflush(stdout);
        char user_buf[256] = {0};
        if (fgets(user_buf, sizeof(user_buf), stdin)) {
            trim_line(user_buf);
            p.user = user_buf;
        }
        if (!p.user.empty()) {
            printf("Password (optional): ");
            fflush(stdout);
            char pass_buf[256] = {0};
            if (fgets(pass_buf, sizeof(pass_buf), stdin)) {
                trim_line(pass_buf);
                p.pass = pass_buf;
            }
        }
    }

    apply_proxy_settings(p);
    printf("[bypass] Configured %s proxy: %s:%d\n", p.type.c_str(), p.host.c_str(), p.port);
    printf("=======================================================\n\n");
}

// ---------------------------------------------------------------------
// 15-Second Telegram Watchdog
// ---------------------------------------------------------------------

static void start_telegram_watchdog() {
    g_watchdog_done.store(false);
    std::thread([]() {
        for (int i = 0; i < 15; ++i) {
            std::this_thread::sleep_for(std::chrono::seconds(1));
            if (g_watchdog_done.load()) return;

            // Check if Telegram is already connected via tg.is_connected syscall
            if (g_core && g_core->syscall) {
                char* out = nullptr;
                sc_status_t rc = g_core->syscall("tg.is_connected", "", &out);
                if (rc == SC_OK && out) {
                    scjson::Value res;
                    if (scjson::parse(out, res)) {
                        if (scjson::boolean(&res, "connected", false)) {
                            if (g_core->free) g_core->free(out);
                            g_watchdog_done.store(true);
                            return;
                        }
                    }
                    if (g_core->free) g_core->free(out);
                }
            }
        }

        if (!g_watchdog_done.load()) {
            log_bypass(SC_LOG_WARN,
                "Telegram connection not established after 15s. Direct access may be blocked; "
                "recommend enabling [bypass] tgwsproxy = true in config.toml");
        }
    }).detach();
}

// ---------------------------------------------------------------------
// Configuration loader: reads [bypass] block from config.toml
// ---------------------------------------------------------------------

static std::string extract_toml_string(const char* line) {
    const char* eq = strchr(line, '=');
    if (!eq) return "";
    eq++;
    while (*eq == ' ' || *eq == '\t') eq++;
    if (*eq == '"' || *eq == '\'') {
        char quote = *eq++;
        const char* end = strchr(eq, quote);
        if (end) return std::string(eq, end - eq);
        return std::string(eq);
    }
    std::string res;
    while (*eq && *eq != '#' && *eq != '\r' && *eq != '\n') {
        res.push_back(*eq++);
    }
    while (!res.empty() && (res.back() == ' ' || res.back() == '\t')) res.pop_back();
    return res;
}

static int extract_toml_int(const char* line) {
    const char* eq = strchr(line, '=');
    if (!eq) return 0;
    return atoi(eq + 1);
}

static void read_bypass_config() {
    FILE* f = fopen("config.toml", "r");
    if (!f) return;

    char line[512];
    bool in_bypass = false;
    bool found_tgwsproxy = false;
    std::string config_proxy_str;
    ParsedProxy config_proxy_obj;

    while (fgets(line, sizeof(line), f)) {
        char* p = line;
        while (*p == ' ' || *p == '\t') p++;
        if (*p == '#' || *p == '\r' || *p == '\n' || *p == 0) continue;

        if (*p == '[') {
            in_bypass = (strncmp(p, "[bypass]", 8) == 0 ||
                         strncmp(p, "[bypasses]", 10) == 0 ||
                         strncmp(p, "[plugins.bypasses]", 18) == 0);
            continue;
        }

        if (in_bypass) {
            if (strstr(p, "tgwsproxy")) {
                found_tgwsproxy = true;
                char* eq = strchr(p, '=');
                if (eq) {
                    while (*eq == '=' || *eq == ' ' || *eq == '\t') eq++;
                    if (strncmp(eq, "true", 4) == 0) {
                        g_tgwsproxy.store(true);
                    } else if (strncmp(eq, "false", 5) == 0) {
                        g_tgwsproxy.store(false);
                    }
                }
            } else if (strncmp(p, "proxy", 5) == 0 || strncmp(p, "link", 4) == 0) {
                config_proxy_str = extract_toml_string(p);
            } else if (strncmp(p, "host", 4) == 0 || strncmp(p, "server", 6) == 0) {
                config_proxy_obj.host = extract_toml_string(p);
            } else if (strncmp(p, "port", 4) == 0) {
                config_proxy_obj.port = extract_toml_int(p);
            } else if (strncmp(p, "type", 4) == 0 || strncmp(p, "protocol", 8) == 0) {
                config_proxy_obj.type = extract_toml_string(p);
            } else if (strncmp(p, "user", 4) == 0 || strncmp(p, "username", 8) == 0) {
                config_proxy_obj.user = extract_toml_string(p);
            } else if (strncmp(p, "pass", 4) == 0 || strncmp(p, "password", 8) == 0) {
                config_proxy_obj.pass = extract_toml_string(p);
            } else if (strncmp(p, "secret", 6) == 0) {
                config_proxy_obj.secret = extract_toml_string(p);
            }
        }
    }
    fclose(f);

    if (!config_proxy_str.empty()) {
        ParsedProxy pp;
        if (parse_proxy_link(config_proxy_str, pp)) {
            apply_proxy_settings(pp);
        }
    } else if (!config_proxy_obj.host.empty() && config_proxy_obj.port > 0) {
        apply_proxy_settings(config_proxy_obj);
    }

    // If [bypass] block or tgwsproxy is missing, ensure default in config.toml
    if (!found_tgwsproxy) {
        FILE* fa = fopen("config.toml", "a");
        if (fa) {
            fprintf(fa, "\n[bypass]\ntgwsproxy = false\n");
            fclose(fa);
        }
    }
}

// ---------------------------------------------------------------------
// Syscalls & Tools
// ---------------------------------------------------------------------

static char* syscall_string(const std::string& s) {
    if (!g_core || !g_core->alloc) {
        return strdup(s.c_str());
    }
    char* buf = (char*)g_core->alloc(s.size() + 1);
    if (!buf) return nullptr;
    memcpy(buf, s.c_str(), s.size() + 1);
    return buf;
}

static sc_status_t SC_CALL bypass_syscall_handler(const char* method, const char* args_json, char** out) {
    if (!method) return SC_ERR_INVALID_ARG;
    if (out) *out = nullptr;
    std::string m(method);

    if (m == "bypass.status" || m == "bypass.get") {
        std::lock_guard<std::mutex> lock(g_bypass_mu);
        scjson::Value res = scjson::make_object();
        res.set("enabled", scjson::make_bool(g_enabled.load()));
        res.set("tgwsproxy", scjson::make_bool(g_tgwsproxy.load()));
        res.set("active_proxy", scjson::make_string(g_active_proxy.c_str()));
        res.set("type", scjson::make_string(g_proxy_type.c_str()));
        res.set("host", scjson::make_string(g_proxy_host.c_str()));
        res.set("port", scjson::make_int(g_proxy_port));
        if (out) *out = syscall_string(scjson::dump(res));
        return SC_OK;
    }

    if (m == "bypass.apply" || m == "bypass.set") {
        if (!args_json || !args_json[0]) return SC_ERR_INVALID_ARG;
        scjson::Value v;
        if (!scjson::parse(args_json, v)) return SC_ERR_INVALID_ARG;
        std::string p = scjson::str(&v, "proxy");
        if (p.empty()) return SC_ERR_INVALID_ARG;

        ParsedProxy parsed;
        if (parse_proxy_link(p, parsed)) {
            apply_proxy_settings(parsed);
        } else {
            parsed.host = p;
            parsed.port = 10808;
            parsed.type = "socks5";
            apply_proxy_settings(parsed);
        }

        if (out) {
            scjson::Value res = scjson::make_object();
            res.set("status", scjson::make_string("applied"));
            res.set("proxy", scjson::make_string(g_active_proxy.c_str()));
            *out = syscall_string(scjson::dump(res));
        }
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

static const sc_syscall_reg_t g_syscalls[] = {
    {"bypass.", bypass_syscall_handler},
};

static sc_status_t get_syscalls(const sc_syscall_reg_t** out, size_t* count) {
    if (out) *out = g_syscalls;
    if (count) *count = sizeof(g_syscalls) / sizeof(g_syscalls[0]);
    return SC_OK;
}

static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "[{\"name\":\"bypass_status\","
        "\"description\":\"Get status of network bypass, tgwsproxy setting, and active proxy configuration.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}},"
        "{\"name\":\"bypass_configure\","
        "\"description\":\"Update active network bypass proxy configuration.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{\"proxy\":{\"type\":\"string\",\"description\":\"Proxy URL or host:port\"}},\"required\":[\"proxy\"]}}]";
    *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL std::string g_tool_out;

static sc_status_t call_tool(const char* name, const char* args_json, char** out_result) {
    if (!name || !out_result) return SC_ERR_INVALID_ARG;
    *out_result = nullptr;

    if (strcmp(name, "bypass_status") == 0) {
        std::lock_guard<std::mutex> lock(g_bypass_mu);
        scjson::Value res = scjson::make_object();
        res.set("enabled", scjson::make_bool(g_enabled.load()));
        res.set("tgwsproxy", scjson::make_bool(g_tgwsproxy.load()));
        res.set("active_proxy", scjson::make_string(g_active_proxy.c_str()));
        res.set("type", scjson::make_string(g_proxy_type.c_str()));
        res.set("host", scjson::make_string(g_proxy_host.c_str()));
        res.set("port", scjson::make_int(g_proxy_port));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "bypass_configure") == 0) {
        if (!args_json || !args_json[0]) return SC_ERR_INVALID_ARG;
        scjson::Value v;
        if (!scjson::parse(args_json, v)) return SC_ERR_INVALID_ARG;
        std::string p = scjson::str(&v, "proxy");
        if (p.empty()) return SC_ERR_INVALID_ARG;

        ParsedProxy parsed;
        if (parse_proxy_link(p, parsed)) {
            apply_proxy_settings(parsed);
        }

        scjson::Value res = scjson::make_object();
        res.set("status", scjson::make_string("ok"));
        res.set("proxy", scjson::make_string(g_active_proxy.c_str()));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

// ---------------------------------------------------------------------
// Plugin Lifecycle
// ---------------------------------------------------------------------

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name = "bypasses",
        .version = "1.0.0",
        .description = "Network bypass and Telegram WebSocket/proxy routing module",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    if (!api) return SC_ERR_INVALID_ARG;
    g_core = api;

    read_bypass_config();

    if (cfg && cfg[0]) {
        scjson::Value v;
        if (scjson::parse(cfg, v)) {
            if (v.get("tgwsproxy")) {
                g_tgwsproxy.store(scjson::boolean(&v, "tgwsproxy", false));
            }
            std::string proxy = scjson::str(&v, "proxy");
            if (!proxy.empty()) {
                ParsedProxy p;
                if (parse_proxy_link(proxy, p)) {
                    apply_proxy_settings(p);
                }
            }
        }
    }

    log_bypass(SC_LOG_INFO, "bypasses module initialized (ABI 1.7)");
    return SC_OK;
}

static sc_status_t on_startup(void) {
    if (g_tgwsproxy.load()) {
        prompt_and_configure_proxy();
    }
    start_telegram_watchdog();
    return SC_OK;
}

static sc_status_t deinit(void) {
    g_watchdog_done.store(true);
    log_bypass(SC_LOG_INFO, "bypasses module deinitialized");
    g_core = nullptr;
    return SC_OK;
}

static sc_status_t on_shutdown(void) {
    g_watchdog_done.store(true);
    return SC_OK;
}

static sc_status_t health(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    std::lock_guard<std::mutex> lock(g_bypass_mu);
    scjson::Value v = scjson::make_object();
    v.set("status", scjson::make_string("ok"));
    v.set("enabled", scjson::make_bool(g_enabled.load()));
    v.set("tgwsproxy", scjson::make_bool(g_tgwsproxy.load()));
    v.set("proxy_configured", scjson::make_bool(!g_active_proxy.empty()));
    g_tool_out = scjson::dump(v);
    *out = (char*)g_tool_out.c_str();
    return SC_OK;
}

static sc_status_t on_config_reload(const char* cfg) {
    read_bypass_config();
    if (cfg && cfg[0]) {
        scjson::Value v;
        if (scjson::parse(cfg, v)) {
            if (v.get("tgwsproxy")) {
                g_tgwsproxy.store(scjson::boolean(&v, "tgwsproxy", false));
            }
        }
    }
    log_bypass(SC_LOG_INFO, "bypasses config reloaded");
    return SC_OK;
}

static sc_plugin_api_t g_api = {
    .abi_major = SC_ABI_MAJOR,
    .abi_minor = SC_ABI_MINOR,
    .struct_size = sizeof(sc_plugin_api_t),
    .get_info = get_info,
    .init = init,
    .deinit = deinit,
    .get_hooks = nullptr,
    .get_tools = get_tools,
    .call_tool = call_tool,
    .on_startup = on_startup,
    .get_system_prompt = nullptr,
    .health = health,
    .on_config_reload = on_config_reload,
    .health_check_enhanced = nullptr,
    .on_shutdown = on_shutdown,
    .config_version_sync = nullptr,
    .get_syscalls = get_syscalls,
    .get_widgets = nullptr,
    .get_transport = nullptr,
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}
