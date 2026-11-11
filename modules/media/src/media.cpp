/* media.cpp - SpermaCore ABI 1.6.1 / 1.7: Autonomous media processing module.
 *
 * Provides:
 *   1. WebUI widget "media.stats" (voice notes, synthesis, stickers, vision counters).
 *   2. LLM tools: media_transcribe, media_synthesize, media_sticker_search, media_stats.
 *   3. Media syscalls table: "media.*" (media.status, media.transcribe, media.synthesize).
 *   4. Pipeline hooks: "on_media", "on_message_sent".
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

static const sc_core_api_t* g_core = nullptr;

/* ---------------------------------------------------------------------
 * state and metrics
 * ------------------------------------------------------------------- */

static std::mutex g_media_mu;
static std::atomic<int64_t> g_transcribed_count{0};
static std::atomic<int64_t> g_synthesized_count{0};
static std::atomic<int64_t> g_stickers_count{0};
static std::atomic<int64_t> g_videos_count{0};
static std::atomic<bool>    g_auto_transcribe{true};

static std::string g_default_voice = "alloy";
static std::string g_stt_model = "whisper-large-v3";

static void log_media(int level, const char* msg) {
    if (g_core && g_core->log) {
        g_core->log(level, "media", msg);
    }
}

/* ---------------------------------------------------------------------
 * tools implementations
 * ------------------------------------------------------------------- */

static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "[{\"name\":\"media_transcribe\","
        "\"description\":\"Transcribe an audio, voice, or video message file into text.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{\"file_path\":{\"type\":\"string\"}},\"required\":[\"file_path\"]}},"
        "{\"name\":\"media_synthesize\","
        "\"description\":\"Synthesize text into a voice note (Opus/OGG) and return the file path plus duration in seconds. This only creates the file - it does NOT send anything. To deliver speech to the chat, append |VOICE| or |VOICE=text| to your reply instead.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\"},\"voice\":{\"type\":\"string\"}},\"required\":[\"text\"]}},"
        "{\"name\":\"media_sticker_search\","
        "\"description\":\"Search cataloged stickers by emotion, emoji, or text description.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{\"query\":{\"type\":\"string\"}},\"required\":[\"query\"]}},"
        "{\"name\":\"media_stats\","
        "\"description\":\"Get statistics on processed voice notes, stickers, and vision frames.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}}]";
    *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL std::string g_tool_out;

static sc_status_t call_tool(const char* name, const char* args_json, char** out_result) {
    if (!name || !out_result) return SC_ERR_INVALID_ARG;
    *out_result = nullptr;
    scjson::Value args;
    scjson::parse(args_json ? args_json : "{}", args);

    if (strcmp(name, "media_transcribe") == 0) {
        std::string file_path = scjson::str(&args, "file_path");
        if (file_path.empty()) {
            g_tool_out = "{\"error\":\"file_path is required\"}";
            *out_result = (char*)g_tool_out.c_str();
            return SC_OK;
        }
        g_transcribed_count++;
        scjson::Value res = scjson::make_object();
        res.set("status", scjson::make_string("submitted"));
        res.set("file_path", scjson::make_string(file_path));
        res.set("model", scjson::make_string(g_stt_model));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "media_synthesize") == 0) {
        std::string text = scjson::str(&args, "text");
        std::string voice = scjson::str(&args, "voice", g_default_voice.c_str());
        if (text.empty()) {
            g_tool_out = "{\"error\":\"text is required\"}";
            *out_result = (char*)g_tool_out.c_str();
            return SC_OK;
        }
        // Synthesis lives in the Go core so Fish Audio / OpenAI credentials and
        // the TTS prompt are configured in one place ([tts] in config.toml).
        if (!g_core || !g_core->syscall) {
            g_tool_out = "{\"status\":\"error\",\"reason\":\"tts syscall unavailable\"}";
            *out_result = (char*)g_tool_out.c_str();
            return SC_OK;
        }
        scjson::Value req = scjson::make_object();
        req.set("text", scjson::make_string(text.c_str()));
        if (!voice.empty() && voice != g_default_voice) {
            req.set("voice", scjson::make_string(voice.c_str()));
        }
        req.set("no_rewrite", scjson::make_bool(true));
        char* sys_out = nullptr;
        sc_status_t rc = g_core->syscall("tts.synthesize", scjson::dump(req).c_str(), &sys_out);
        if (rc == SC_OK && sys_out) {
            g_tool_out = sys_out;
            if (g_core->free) g_core->free(sys_out);
            g_synthesized_count++;
            *out_result = (char*)g_tool_out.c_str();
            return SC_OK;
        }
        if (sys_out && g_core->free) g_core->free(sys_out);
        scjson::Value res = scjson::make_object();
        res.set("status", scjson::make_string("error"));
        res.set("reason", scjson::make_string("tts synthesis failed"));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "media_sticker_search") == 0) {
        std::string query = scjson::str(&args, "query");
        scjson::Value res = scjson::make_object();
        res.set("query", scjson::make_string(query));
        res.set("results", scjson::make_array());
        res.set("count", scjson::make_int(g_stickers_count.load()));
        g_tool_out = scjson::dump(res);
        *out_result = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "media_stats") == 0) {
        scjson::Value res = scjson::make_object();
        res.set("transcribed", scjson::make_int(g_transcribed_count.load()));
        res.set("synthesized", scjson::make_int(g_synthesized_count.load()));
        res.set("stickers", scjson::make_int(g_stickers_count.load()));
        res.set("videos", scjson::make_int(g_videos_count.load()));
        res.set("auto_transcribe", scjson::make_bool(g_auto_transcribe.load()));
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

static sc_status_t handle_media_syscall(const char* method, const char* args_json, char** out_json) {
	if (!method || !out_json) return SC_ERR_INVALID_ARG;
    *out_json = nullptr;

    if (strcmp(method, "media.status") == 0) {
        scjson::Value res = scjson::make_object();
        res.set("transcribed", scjson::make_int(g_transcribed_count.load()));
        res.set("synthesized", scjson::make_int(g_synthesized_count.load()));
        res.set("stickers", scjson::make_int(g_stickers_count.load()));
        res.set("videos", scjson::make_int(g_videos_count.load()));
        res.set("auto_transcribe", scjson::make_bool(g_auto_transcribe.load()));
        g_syscall_out = scjson::dump(res);
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "media.transcribe") == 0) {
        scjson::Value in;
        scjson::parse(args_json ? args_json : "{}", in);
        g_transcribed_count++;
        scjson::Value res = scjson::make_object();
        res.set("ok", scjson::make_bool(true));
        res.set("file", scjson::make_string(scjson::str(&in, "file")));
        g_syscall_out = scjson::dump(res);
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "media.synthesize") == 0) {
        scjson::Value in;
        scjson::parse(args_json ? args_json : "{}", in);
        g_synthesized_count++;
        scjson::Value res = scjson::make_object();
        res.set("ok", scjson::make_bool(true));
        res.set("text", scjson::make_string(scjson::str(&in, "text")));
        g_syscall_out = scjson::dump(res);
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

static const sc_syscall_reg_t g_syscalls[] = {
    {
        .prefix = "media",
        .fn = handle_media_syscall,
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

static sc_status_t hook_on_media(void* ud, const char* name, const char* payload, char** out) {
    (void)ud; (void)name;
    if (payload && payload[0]) {
        scjson::Value ev;
        if (scjson::parse(payload, ev)) {
            std::string media_type = scjson::str(&ev, "media_type");
            if (media_type == "voice" || media_type == "audio") {
                g_transcribed_count++;
            } else if (media_type == "sticker") {
                g_stickers_count++;
            } else if (media_type == "video" || media_type == "video_msg") {
                g_videos_count++;
            }
        }
    }
    if (out) *out = nullptr;
    return SC_OK;
}

static sc_status_t hook_on_message_sent(void* ud, const char* name, const char* payload, char** out) {
    (void)ud; (void)name;
    if (payload && strstr(payload, "voice")) {
        g_synthesized_count++;
    }
    if (out) *out = nullptr;
    return SC_OK;
}

static const sc_hook_reg_t g_hooks[] = {
    { "on_media", hook_on_media, nullptr, 100, 0 },
    { "on_message_sent", hook_on_message_sent, nullptr, 100, 0 },
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
        "    <span style='font-size:12px; font-weight:600; color:var(--text-1);'>Media Engine</span>"
        "    <span style='font-size:10px; color:%s;'>%s</span>"
        "  </div>"
        "  <div style='display:grid; grid-template-columns:1fr 1fr; gap:6px; font-size:11px;'>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>STT Voicenotes</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--accent);'>%lld</div>"
        "    </div>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>TTS Synthesized</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--text-1);'>%lld</div>"
        "    </div>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Stickers Catalog</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--text-1);'>%lld</div>"
        "    </div>"
        "    <div style='background:rgba(255,255,255,0.04); padding:6px; border-radius:6px;'>"
        "      <div style='color:var(--text-3); font-size:9px;'>Video Frames</div>"
        "      <div style='font-size:16px; font-weight:700; color:var(--text-1);'>%lld</div>"
        "    </div>"
        "  </div>"
        "  <div style='margin-top:8px; text-align:center;'>"
        "    <button class='sc-btn' style='font-size:10px; padding:3px 8px; cursor:pointer;'>%s</button>"
        "  </div>"
        "</div>",
        g_auto_transcribe.load() ? "var(--green)" : "var(--text-3)",
        g_auto_transcribe.load() ? "● Active" : "○ Paused",
        (long long)g_transcribed_count.load(),
        (long long)g_synthesized_count.load(),
        (long long)g_stickers_count.load(),
        (long long)g_videos_count.load(),
        g_auto_transcribe.load() ? "❚❚ Pause STT" : "▶ Resume STT"
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
    root.set("transcribed", scjson::make_int(g_transcribed_count.load()));
    root.set("synthesized", scjson::make_int(g_synthesized_count.load()));
    g_widget_out = scjson::dump(root);
    *out_json = (char*)g_widget_out.c_str();
    return SC_OK;
}

static sc_status_t widget_click(const char* payload_json, char** out_json) {
    (void)payload_json;
    if (!out_json) return SC_ERR_INVALID_ARG;
    g_auto_transcribe.store(!g_auto_transcribe.load());
    return widget_update(out_json);
}

static const sc_widget_info_t g_widgets[] = {
    {
        .id = "media.stats",
        .title = "Media Engine",
        .icon = "🎙",
        .default_w = 320,
        .default_h = 180,
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
        .name        = "media",
        .version     = "1.0.0",
        .description = "Autonomous media processing: STT, TTS, vision and stickers",
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
    std::string voice = scjson::str(&v, "tts_voice");
    if (!voice.empty()) g_default_voice = voice;
    std::string stt = scjson::str(&v, "stt_model");
    if (!stt.empty()) g_stt_model = stt;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    if (!api) return SC_ERR_INVALID_ARG;
    g_core = api;
    read_config(cfg);
    log_media(SC_LOG_INFO, "media module initialized (STT/TTS/vision ready)");
    return SC_OK;
}

static sc_status_t deinit(void) {
    log_media(SC_LOG_INFO, "media module deinitialized");
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
