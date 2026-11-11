/* telegram.cpp - SpermaCore addon: Telegram MTProto transport via TDLib.
 *
 * TDLib is loaded dynamically (tdjson.dll / libtdjson.so).
 * Incoming events flow into core via core->ingest_event.
 * Outgoing requests are handled via sc_transport_info_t and tg.* syscalls.
 */
#if defined(__linux__) && !defined(_GNU_SOURCE)
#define _GNU_SOURCE
#endif

#if defined(_WIN32)
#include <winsock2.h>
#include <ws2tcpip.h>
#else
#include <netdb.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>
#endif

#include "plugin_api.h"
#include "json.hpp"
#include "tdjson.hpp"

#include <algorithm>
#include <atomic>
#include <cctype>
#include <chrono>
#include <condition_variable>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <map>
#include <mutex>
#include <string>
#include <thread>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

#if defined(_WIN32)
#include <windows.h>
#else
#include <dlfcn.h>
#endif

namespace fs = std::filesystem;
using scjson::Value;

static const sc_core_api_t* g_core = nullptr;
static tdjson::Client g_td;

static std::mutex g_state_mu;
static std::atomic<bool> g_loaded{false};
static std::atomic<bool> g_ready{false};
static std::atomic<bool> g_net_ready{false};
static std::atomic<bool> g_stopping{false};
static std::atomic<long long> g_self_id{0};
static std::string g_self_username;
static std::string g_self_first_name;
static std::string g_auth_state;
static std::atomic<bool> g_qr_worker{false};

static int         g_api_id = 0;
static std::string g_api_hash;
static std::string g_phone;
static std::string g_login_mode = "qr";
static std::string g_device_model = "iPhone 16 Pro";
static std::string g_app_version = "11.0";
static std::string g_system_version = "SpermaCore";
static std::string g_session_path;
static std::string g_proxy;
static bool g_reply_to_enabled = true;
static bool g_reply_first_only = true;
static std::string g_data_root;
static std::string g_data_dir;
static std::string g_db_dir;
static std::string g_files_dir;
static std::string g_database_encryption_key;

static std::mutex g_cache_mu;
static std::map<long long, std::string> g_user_names;
static std::map<long long, std::string> g_user_usernames;
static std::map<long long, bool> g_forum_chats;
static std::map<std::pair<long long, long long>, long long> g_own_messages;

static long long now_seconds() {
    return std::chrono::duration_cast<std::chrono::seconds>(
               std::chrono::system_clock::now().time_since_epoch())
        .count();
}

static void log_msg(int level, const std::string& text) {
    if (g_core && g_core->log) {
        g_core->log(level, "telegram", text.c_str());
    }
}

static char* dup_string(const std::string& s) {
    char* p = static_cast<char*>(std::malloc(s.size() + 1));
    if (!p) return nullptr;
    std::memcpy(p, s.c_str(), s.size() + 1);
    return p;
}

static std::string current_module_dir() {
#if defined(_WIN32)
    HMODULE handle = nullptr;
    if (!GetModuleHandleExA(GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS |
                                GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT,
                            reinterpret_cast<LPCSTR>(&current_module_dir), &handle)) {
        return {};
    }
    char buf[MAX_PATH];
    DWORD n = GetModuleFileNameA(handle, buf, MAX_PATH);
    if (n == 0 || n >= MAX_PATH) return {};
    return fs::path(std::string(buf, n)).parent_path().string();
#else
    Dl_info info;
    if (dladdr(reinterpret_cast<void*>(&current_module_dir), &info) == 0 || !info.dli_fname) {
        return {};
    }
    return fs::path(info.dli_fname).parent_path().string();
#endif
}

// ---------------------------------------------------------------------
// tdlib json builders
// ---------------------------------------------------------------------

static Value v_obj(const char* type) {
    Value v = scjson::make_object();
    v.set("@type", scjson::make_string(type));
    return v;
}

static Value v_str(const std::string& s) { return scjson::make_string(s); }
static Value v_int(long long n) { return scjson::make_int(n); }
static Value v_bool(bool b) { return scjson::make_bool(b); }

// ---------------------------------------------------------------------
// custom emoji cache
// ---------------------------------------------------------------------

struct custom_emoji_info {
    long long id = 0;
    std::string fallback;
    std::string pack;
};

static std::mutex g_custom_emojis_mu;
static std::unordered_map<long long, custom_emoji_info> g_custom_emojis;

static void remember_custom_emoji(long long id, const std::string& fallback, const std::string& pack = "") {
    if (id <= 0) return;
    std::lock_guard<std::mutex> lk(g_custom_emojis_mu);
    custom_emoji_info info;
    info.id = id;
    info.fallback = fallback.empty() ? "✨" : fallback;
    info.pack = pack;
    g_custom_emojis[id] = info;
    if (g_custom_emojis.size() > 5000) {
        g_custom_emojis.erase(g_custom_emojis.begin());
    }
}

static std::string expand_custom_emoji_tags(const std::string& text) {
    if (text.empty()) return text;
    std::string result;
    result.reserve(text.size() + 64);

    size_t i = 0;
    while (i < text.size()) {
        if (text[i] == '[' && i + 2 < text.size() && text[i + 1] == ':') {
            size_t end = text.find(":]", i + 2);
            if (end != std::string::npos) {
                std::string inner = text.substr(i + 2, end - (i + 2));
                size_t colon = inner.find(':');
                std::string id_str = (colon == std::string::npos) ? inner : inner.substr(0, colon);
                std::string fallback = (colon == std::string::npos) ? "" : inner.substr(colon + 1);
                if (fallback.empty()) fallback = "✨";
                if (!id_str.empty() && std::all_of(id_str.begin(), id_str.end(), ::isdigit)) {
                    result += "<tg-emoji emoji-id=\"" + id_str + "\">" + fallback + "</tg-emoji>";
                    i = end + 2;
                    continue;
                }
            }
        } else if (text.compare(i, 14, "[custom_emoji:") == 0) {
            size_t end = text.find(']', i + 14);
            if (end != std::string::npos) {
                std::string inner = text.substr(i + 14, end - (i + 14));
                size_t colon = inner.find(':');
                std::string id_str = (colon == std::string::npos) ? inner : inner.substr(0, colon);
                std::string fallback = (colon == std::string::npos) ? "" : inner.substr(colon + 1);
                if (fallback.empty()) fallback = "✨";
                if (!id_str.empty() && std::all_of(id_str.begin(), id_str.end(), ::isdigit)) {
                    result += "<tg-emoji emoji-id=\"" + id_str + "\">" + fallback + "</tg-emoji>";
                    i = end + 1;
                    continue;
                }
            }
        }
        result.push_back(text[i++]);
    }
    return result;
}

static bool td_error(const Value& v, std::string* reason = nullptr);

static Value formatted_text(const std::string& text) {
    if (text.empty()) {
        Value v = v_obj("formattedText");
        v.set("text", v_str(""));
        v.set("entities", scjson::make_array());
        return v;
    }

    std::string expanded = expand_custom_emoji_tags(text);

    // parse via tdlib if html formatting tags are present (<tg-emoji, <b>, <i>, <code>, <a>, <tg-spoiler>)
    if (expanded.find('<') != std::string::npos && expanded.find('>') != std::string::npos) {
        Value req = v_obj("parseTextEntities");
        req.set("text", v_str(expanded));
        req.set("parse_mode", v_obj("textParseModeHTML"));
        Value out;
        if (g_td.execute(req, out) && !td_error(out) && scjson::str(&out, "@type") == "formattedText") {
            return out;
        }
    }

    Value v = v_obj("formattedText");
    v.set("text", v_str(text));
    v.set("entities", scjson::make_array());
    return v;
}

static Value link_preview_options() {
    Value v = v_obj("linkPreviewOptions");
    v.set("is_disabled", v_bool(false));
    v.set("url", v_str(""));
    v.set("force_small_media", v_bool(false));
    v.set("force_large_media", v_bool(false));
    v.set("show_above_text", v_bool(false));
    return v;
}

static Value text_content(const std::string& text) {
    Value v = v_obj("inputMessageText");
    v.set("text", formatted_text(text));
    v.set("link_preview_options", link_preview_options());
    v.set("clear_draft", v_bool(false));
    return v;
}

static Value reply_to_message(long long message_id) {
    Value v = v_obj("inputMessageReplyToMessage");
    v.set("message_id", v_int(message_id));
    v.set("quote", Value{});
    v.set("checklist_task_id", v_int(0));
    v.set("poll_option_id", v_str(""));
    return v;
}

static Value topic_forum(int topic_id) {
    Value v = v_obj("messageTopicForum");
    v.set("forum_topic_id", v_int(topic_id));
    return v;
}

static Value send_options() {
    Value v = v_obj("messageSendOptions");
    v.set("suggested_post_info", Value{});
    v.set("disable_notification", v_bool(false));
    v.set("from_background", v_bool(false));
    v.set("protect_content", v_bool(false));
    v.set("allow_paid_broadcast", v_bool(false));
    v.set("paid_message_star_count", v_int(0));
    v.set("update_order_of_installed_sticker_sets", v_bool(false));
    v.set("scheduling_state", Value{});
    v.set("effect_id", v_int(0));
    v.set("sending_id", v_int(0));
    v.set("only_preview", v_bool(false));
    return v;
}

static Value file_local(const std::string& path) {
    Value v = v_obj("inputFileLocal");
    v.set("path", v_str(path));
    return v;
}

static Value file_known(int file_id) {
    Value v = v_obj("inputFileId");
    v.set("id", v_int(file_id));
    return v;
}

static Value make_input_file(const std::string& path) {
    if (path.rfind("http://", 0) == 0 || path.rfind("https://", 0) == 0) {
        Value v = v_obj("inputFileRemote");
        v.set("id", v_str(path));
        return v;
    }
    if (!path.empty() && std::all_of(path.begin(), path.end(), ::isdigit)) {
        try {
            int fid = std::stoi(path);
            if (fid > 0) return file_known(fid);
        } catch (...) {}
    }
    return file_local(path);
}

// TDLib wraps every media file in a dedicated "input<X>" object (inputPhoto,
// inputVideo, inputVoiceNote, ...) which then goes into inputMessage<X>. Passing
// the raw InputFile directly makes TDLib drop the field and fail with
// "InputFile is not specified".

static Value input_photo(const Value& file, const std::string& caption = "") {
    Value media = v_obj("inputPhoto");
    media.set("photo", file);
    media.set("thumbnail", Value{});
    media.set("video", Value{});
    media.set("added_sticker_file_ids", scjson::make_array());
    media.set("width", v_int(0));
    media.set("height", v_int(0));

    Value v = v_obj("inputMessagePhoto");
    v.set("photo", media);
    v.set("caption", formatted_text(caption));
    v.set("show_caption_above_media", v_bool(false));
    v.set("self_destruct_type", Value{});
    v.set("has_spoiler", v_bool(false));
    return v;
}

static Value input_video(const Value& file, const std::string& caption = "") {
    Value media = v_obj("inputVideo");
    media.set("video", file);
    media.set("thumbnail", Value{});
    media.set("cover", Value{});
    media.set("start_timestamp", v_int(0));
    media.set("added_sticker_file_ids", scjson::make_array());
    media.set("duration", v_int(0));
    media.set("width", v_int(0));
    media.set("height", v_int(0));
    media.set("supports_streaming", v_bool(false));

    Value v = v_obj("inputMessageVideo");
    v.set("video", media);
    v.set("caption", formatted_text(caption));
    v.set("show_caption_above_media", v_bool(false));
    v.set("self_destruct_type", Value{});
    v.set("has_spoiler", v_bool(false));
    return v;
}

[[maybe_unused]] static Value input_animation(const Value& file, const std::string& caption = "") {
    Value media = v_obj("inputAnimation");
    media.set("animation", file);
    media.set("thumbnail", Value{});
    media.set("added_sticker_file_ids", scjson::make_array());
    media.set("duration", v_int(0));
    media.set("width", v_int(0));
    media.set("height", v_int(0));

    Value v = v_obj("inputMessageAnimation");
    v.set("animation", media);
    v.set("caption", formatted_text(caption));
    v.set("show_caption_above_media", v_bool(false));
    v.set("has_spoiler", v_bool(false));
    return v;
}

static Value input_document(const Value& file, const std::string& caption = "") {
    Value media = v_obj("inputDocument");
    media.set("document", file);
    media.set("thumbnail", Value{});
    media.set("disable_content_type_detection", v_bool(false));

    Value v = v_obj("inputMessageDocument");
    v.set("document", media);
    v.set("caption", formatted_text(caption));
    return v;
}

static Value input_voice(const Value& file, const std::string& caption = "", int duration = 0) {
    Value media = v_obj("inputVoiceNote");
    media.set("voice_note", file);
    media.set("duration", v_int(duration > 0 ? duration : 0));
    // Telegram expects a 5-bit amplitude waveform for voice notes; TDLib JSON
    // transports bytes as base64. A flat mid-level envelope is a valid value.
    media.set("waveform", v_str("EBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEA=="));

    Value v = v_obj("inputMessageVoiceNote");
    v.set("voice_note", media);
    v.set("caption", formatted_text(caption));
    v.set("self_destruct_type", Value{});
    return v;
}

static Value input_audio(const Value& file, const std::string& caption = "", int duration = 0) {
    Value media = v_obj("inputAudio");
    media.set("audio", file);
    media.set("album_cover_thumbnail", Value{});
    media.set("duration", v_int(duration > 0 ? duration : 0));
    media.set("title", v_str(""));
    media.set("performer", v_str(""));

    Value v = v_obj("inputMessageAudio");
    v.set("audio", media);
    v.set("caption", formatted_text(caption));
    return v;
}

static Value input_video_note(const Value& file) {
    Value media = v_obj("inputVideoNote");
    media.set("video_note", file);
    media.set("thumbnail", Value{});
    media.set("duration", v_int(0));
    media.set("length", v_int(0));

    Value v = v_obj("inputMessageVideoNote");
    v.set("video_note", media);
    v.set("self_destruct_type", Value{});
    return v;
}

static Value input_sticker(const Value& file, const std::string& emoji = "",
                           int width = 0, int height = 0) {
    Value media = v_obj("inputSticker");
    media.set("sticker", file);
    media.set("thumbnail", Value{});
    media.set("width", v_int(width > 0 ? width : 512));
    media.set("height", v_int(height > 0 ? height : 512));

    Value v = v_obj("inputMessageSticker");
    v.set("sticker", media);
    v.set("emoji", v_str(emoji));
    return v;
}

static bool td_error(const Value& v, std::string* reason) {
    if (v.type != scjson::Type::Object) return true;
    if (scjson::str(&v, "@type") != "error") return false;
    if (reason) {
        *reason = scjson::str(&v, "message");
        if (reason->empty()) reason->assign("tdlib error");
    }
    return true;
}

static bool td_request(const Value& req, Value& out, int timeout_ms = 15000) {
    if (!g_loaded.load()) return false;
    if (!g_td.request(req, out, timeout_ms)) return false;
    if (td_error(out)) return false;
    return true;
}

// ---------------------------------------------------------------------
// users and chats
// ---------------------------------------------------------------------

static std::string join_name(const Value* user) {
    std::string name = scjson::str(user, "first_name");
    std::string last = scjson::str(user, "last_name");
    if (!last.empty()) {
        if (!name.empty()) name += " ";
        name += last;
    }
    return name;
}

static std::string user_username(const Value* user) {
    const Value* usernames = user ? user->get("usernames") : nullptr;
    if (usernames) {
        const Value* list = usernames->get("active_usernames");
        if (list && list->type == scjson::Type::Array && !list->array.empty()) {
            return list->array[0].string;
        }
        std::string secondary = scjson::str(usernames, "secondary_username");
        if (!secondary.empty()) return secondary;
    }
    return {};
}

static bool fetch_user(long long user_id, std::string* name, std::string* username) {
    {
        std::lock_guard<std::mutex> lock(g_cache_mu);
        auto it = g_user_names.find(user_id);
        if (it != g_user_names.end()) {
            if (name) *name = it->second;
            if (username) *username = g_user_usernames[user_id];
            return true;
        }
    }
    Value req = v_obj("getUser");
    req.set("user_id", v_int(user_id));
    Value out;
    if (!td_request(req, out)) return false;
    std::string n = join_name(&out);
    std::string u = user_username(&out);
    {
        std::lock_guard<std::mutex> lock(g_cache_mu);
        g_user_names[user_id] = n;
        g_user_usernames[user_id] = u;
    }
    if (name) *name = n;
    if (username) *username = u;
    return true;
}

static bool chat_is_forum(long long chat_id) {
    {
        std::lock_guard<std::mutex> lock(g_cache_mu);
        auto it = g_forum_chats.find(chat_id);
        if (it != g_forum_chats.end()) return it->second;
    }
    Value req = v_obj("getChat");
    req.set("chat_id", v_int(chat_id));
    Value out;
    bool forum = false;
    if (td_request(req, out)) {
        forum = scjson::boolean(&out, "view_as_topics", false) ||
                scjson::boolean(&out, "is_forum", false);
    }
    {
        std::lock_guard<std::mutex> lock(g_cache_mu);
        g_forum_chats[chat_id] = forum;
    }
    return forum;
}

// ---------------------------------------------------------------------
// map temporary message ids to permanent server ids
// ---------------------------------------------------------------------

static std::mutex g_msg_id_mu;
static std::condition_variable g_msg_id_cv;
static std::unordered_map<long long, long long> g_msg_id_map; // old_id -> new_id

static void record_message_id_mapping(long long old_id, long long new_id) {
    if (old_id == 0 || new_id == 0) return;
    std::lock_guard<std::mutex> lk(g_msg_id_mu);
    g_msg_id_map[old_id] = new_id;
    if (g_msg_id_map.size() > 10000) {
        g_msg_id_map.erase(g_msg_id_map.begin());
    }
    g_msg_id_cv.notify_all();
}

static long long resolve_message_id(long long id) {
    if (id == 0) return 0;
    std::lock_guard<std::mutex> lk(g_msg_id_mu);
    auto it = g_msg_id_map.find(id);
    if (it != g_msg_id_map.end()) {
        return it->second;
    }
    return id;
}

static long long wait_for_real_message_id(long long temp_id, int timeout_ms = 2500) {
    if (temp_id == 0) return 0;
    std::unique_lock<std::mutex> lk(g_msg_id_mu);
    auto it = g_msg_id_map.find(temp_id);
    if (it != g_msg_id_map.end()) {
        return it->second;
    }
    g_msg_id_cv.wait_for(lk, std::chrono::milliseconds(timeout_ms), [&] {
        return g_msg_id_map.find(temp_id) != g_msg_id_map.end();
    });
    it = g_msg_id_map.find(temp_id);
    if (it != g_msg_id_map.end()) {
        return it->second;
    }
    return temp_id;
}

static void remember_own(long long chat_id, long long message_id) {
    if (chat_id == 0 || message_id == 0) return;
    std::lock_guard<std::mutex> lock(g_cache_mu);
    g_own_messages[{chat_id, message_id}] = now_seconds();
}

static bool is_own(long long chat_id, long long message_id) {
    std::lock_guard<std::mutex> lock(g_cache_mu);
    if (g_own_messages.find({chat_id, message_id}) != g_own_messages.end()) return true;
    long long real_id = resolve_message_id(message_id);
    if (real_id != message_id) {
        return g_own_messages.find({chat_id, real_id}) != g_own_messages.end();
    }
    return false;
}

// ---------------------------------------------------------------------
// sending
// ---------------------------------------------------------------------

static Value make_send_message(long long chat_id, const Value& content,
                               long long reply_to_id, int topic_id) {
    Value req = v_obj("sendMessage");
    req.set("chat_id", v_int(chat_id));
    if (topic_id > 0) {
        req.set("topic_id", topic_forum(topic_id));
    } else {
        req.set("topic_id", Value{});
    }
    if (reply_to_id > 0) {
        req.set("reply_to", reply_to_message(reply_to_id));
    } else {
        req.set("reply_to", Value{});
    }
    req.set("options", send_options());
    req.set("reply_markup", Value{});
    req.set("input_message_content", content);
    return req;
}

static bool send_content(long long chat_id, const Value& content, long long reply_to_id,
                         int topic_id, long long* message_id, std::string* error,
                         int timeout_ms = 30000) {
    if (!g_ready.load()) {
        if (error) *error = "telegram is not connected";
        return false;
    }
    Value req = make_send_message(chat_id, content, reply_to_id, topic_id);
    Value out;
    if (!td_request(req, out, timeout_ms)) {
        {
            std::ofstream dbg("data/logs/td_debug.log", std::ios::app);
            if (dbg) dbg << "REQUEST " << scjson::dump(req) << "\nRESPONSE "
                         << scjson::dump(out) << "\n\n";
        }
        if (error) {
            std::string msg = scjson::str(&out, "message");
            int code = static_cast<int>(scjson::num(&out, "code", 0));
            if (!msg.empty()) {
                *error = "sendMessage failed: code=" + std::to_string(code) + " " + msg;
            } else {
                *error = "sendMessage failed (no TDLib error details)";
            }
        }
        return false;
    }
    long long id = scjson::num(&out, "id", 0);
    remember_own(chat_id, id);
    if (message_id) {
        // wait briefly for updateMessageSendSucceeded to resolve permanent server id
        if (out.get("sending_state") != nullptr) {
            id = wait_for_real_message_id(id, 1500);
        }
        *message_id = id;
    }
    return true;
}

static bool send_text(long long chat_id, const std::string& text, long long reply_to_id,
                      int topic_id, long long* message_id, std::string* error) {
    return send_content(chat_id, text_content(text), reply_to_id, topic_id, message_id, error);
}

static std::string download_file(int file_id, int timeout_ms = 30000) {
    if (file_id <= 0) return {};
    Value req = v_obj("downloadFile");
    req.set("file_id", v_int(file_id));
    req.set("priority", v_int(32));
    req.set("offset", v_int(0));
    req.set("limit", v_int(0));
    req.set("synchronous", v_bool(true));
    Value out;
    if (!td_request(req, out, timeout_ms)) return {};
    const Value* local = out.get("local");
    if (!local) return {};
    if (!scjson::boolean(local, "is_downloading_completed", false)) return {};
    return scjson::str(local, "path");
}

static const Value* largest_photo_size(const Value* photo) {
    if (!photo) return nullptr;
    const Value* sizes = photo->get("sizes");
    if (!sizes || sizes->type != scjson::Type::Array) return nullptr;
    const Value* best = nullptr;
    long long best_area = -1;
    for (const auto& size : sizes->array) {
        long long w = scjson::num(&size, "width", 0);
        long long h = scjson::num(&size, "height", 0);
        long long area = w * h;
        if (area > best_area) {
            best_area = area;
            best = &size;
        }
    }
    return best;
}

// ---------------------------------------------------------------------
// incoming events
// ---------------------------------------------------------------------

static void emit_ingest(const std::string& event_json) {
    if (g_core && g_core->ingest_event) {
        g_core->ingest_event(event_json.c_str());
    }
}

static std::string sender_name_for(long long sender_id, bool sender_is_chat,
                                   long long* out_id) {
    if (sender_is_chat) {
        Value req = v_obj("getChat");
        req.set("chat_id", v_int(sender_id));
        Value out;
        if (td_request(req, out)) {
            if (out_id) *out_id = sender_id;
            return scjson::str(&out, "title");
        }
        return {};
    }
    std::string name;
    std::string username;
    fetch_user(sender_id, &name, &username);
    if (out_id) *out_id = sender_id;
    return name;
}

// ---------------------------------------------------------------------
// inline keyboards, callback queries, inline bots and invite links
// ---------------------------------------------------------------------

static const char kB64Alphabet[] =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

// base64_decode turns the base64 payload TDLib uses for callback data into
// printable UTF-8 when possible; binary payloads are returned as-is.
static std::string base64_decode(const std::string& in) {
    std::string clean;
    clean.reserve(in.size());
    for (char ch : in) {
        if (ch == '\r' || ch == '\n' || ch == ' ' || ch == '\t') continue;
        clean.push_back(ch);
    }
    std::string out;
    out.reserve(clean.size() * 3 / 4 + 3);
    int val = 0;
    int bits = -8;
    for (char ch : clean) {
        if (ch == '=') break;
        const char* pos = std::strchr(kB64Alphabet, ch);
        if (!pos) return in;
        val = (val << 6) + static_cast<int>(pos - kB64Alphabet);
        bits += 6;
        if (bits >= 0) {
            out.push_back(static_cast<char>((val >> bits) & 0xFF));
            bits -= 8;
        }
    }
    // keep the decoded payload printable so it survives the JSON event path
    for (char& ch : out) {
        unsigned char u = static_cast<unsigned char>(ch);
        if (u < 0x20 || u == 0x7F) ch = ' ';
    }
    return out;
}

struct button_info {
    int index = 0;
    int row = 0;
    int col = 0;
    std::string text;
    std::string kind;
    std::string data;
    std::string data_text;
    std::string url;
};

static std::vector<button_info> message_buttons(const Value* msg) {
    std::vector<button_info> out;
    if (!msg) return out;
    const Value* markup = msg->get("reply_markup");
    if (!markup || scjson::str(markup, "@type") != "replyMarkupInlineKeyboard") {
        return out;
    }
    const Value* rows = markup->get("rows");
    if (!rows || rows->type != scjson::Type::Array) return out;
    int index = 1;
    int row_idx = 0;
    for (const auto& row : rows->array) {
        if (row.type != scjson::Type::Array) {
            row_idx++;
            continue;
        }
        int col_idx = 0;
        for (const auto& btn : row.array) {
            button_info b;
            b.index = index++;
            b.row = row_idx;
            b.col = col_idx++;
            b.text = scjson::str(&btn, "text");
            const Value* t = btn.get("type");
            std::string tname = scjson::str(t, "@type");
            b.kind = "callback";
            if (tname == "inlineKeyboardButtonTypeCallback") {
                b.data = scjson::str(t, "data");
                b.data_text = base64_decode(b.data);
            } else if (tname == "inlineKeyboardButtonTypeCallbackWithPassword") {
                b.data = scjson::str(t, "data");
                b.data_text = base64_decode(b.data);
            } else if (tname == "inlineKeyboardButtonTypeUrl") {
                b.kind = "url";
                b.url = scjson::str(t, "url");
            } else if (tname == "inlineKeyboardButtonTypeCallbackGame") {
                b.kind = "game";
            } else if (tname == "inlineKeyboardButtonTypeSwitchInline") {
                b.kind = "switch_inline";
                b.data_text = scjson::str(t, "query");
            } else if (tname == "inlineKeyboardButtonTypeWebApp") {
                b.kind = "web_app";
                b.url = scjson::str(t, "url");
            } else if (tname == "inlineKeyboardButtonTypeLoginUrl") {
                b.kind = "login_url";
                b.url = scjson::str(t, "url");
            } else if (tname == "inlineKeyboardButtonTypeCopyText") {
                b.kind = "copy_text";
                b.data_text = scjson::str(t, "text");
            }
            out.push_back(std::move(b));
        }
        row_idx++;
    }
    return out;
}

static Value buttons_json(const std::vector<button_info>& buttons) {
    Value arr = scjson::make_array();
    for (const auto& b : buttons) {
        Value v = scjson::make_object();
        v.set("index", v_int(b.index));
        v.set("row", v_int(b.row));
        v.set("col", v_int(b.col));
        v.set("text", v_str(b.text));
        v.set("type", v_str(b.kind));
        if (!b.data.empty()) {
            v.set("data", v_str(b.data));
            v.set("data_text", v_str(b.data_text));
        } else if (!b.data_text.empty()) {
            v.set("data_text", v_str(b.data_text));
        }
        if (!b.url.empty()) v.set("url", v_str(b.url));
        arr.push(v);
    }
    return arr;
}

// extract_topic_id follows the same rules as handle_message.
static int extract_topic_id(const Value& msg) {
    const Value* topic = msg.get("topic_id");
    if (!topic) return 0;
    std::string topic_type = scjson::str(topic, "@type");
    if (topic_type == "messageTopicForum") {
        return static_cast<int>(scjson::num(topic, "forum_topic_id", 0));
    }
    if (topic_type == "messageTopicThread") {
        return static_cast<int>(scjson::num(topic, "message_thread_id", 0));
    }
    return 0;
}

static std::string invite_hash(const std::string& raw) {
    std::string s = raw;
    while (!s.empty() && std::isspace(static_cast<unsigned char>(s.front()))) s.erase(s.begin());
    while (!s.empty() && std::isspace(static_cast<unsigned char>(s.back()))) s.pop_back();
    const char* markers[] = {"t.me/+", "t.me/joinchat/", "telegram.me/+",
                             "telegram.me/joinchat/"};
    for (const char* marker : markers) {
        size_t pos = s.find(marker);
        if (pos != std::string::npos) {
            std::string tail = s.substr(pos + std::strlen(marker));
            size_t end = tail.find_first_of("?# \t\r\n\"'<>");
            if (end != std::string::npos) tail = tail.substr(0, end);
            return tail;
        }
    }
    size_t tg = s.find("tg://join?invite=");
    if (tg != std::string::npos) {
        std::string tail = s.substr(tg + std::strlen("tg://join?invite="));
        size_t end = tail.find_first_of("&# \t\r\n\"'<>");
        if (end != std::string::npos) tail = tail.substr(0, end);
        return tail;
    }
    return {};
}

// public_username extracts @name from an @mention or a public t.me link.
static std::string public_username(const std::string& raw) {
    std::string s = raw;
    while (!s.empty() && std::isspace(static_cast<unsigned char>(s.front()))) s.erase(s.begin());
    while (!s.empty() && std::isspace(static_cast<unsigned char>(s.back()))) s.pop_back();
    if (s.empty()) return {};
    if (s[0] == '@') s.erase(s.begin());
    const char* markers[] = {"t.me/", "telegram.me/"};
    for (const char* marker : markers) {
        size_t pos = s.find(marker);
        if (pos != std::string::npos) {
            std::string tail = s.substr(pos + std::strlen(marker));
            if (!tail.empty() && tail[0] == '+') return {};
            size_t end = tail.find_first_of("/?# \t\r\n\"'<>");
            if (end != std::string::npos) tail = tail.substr(0, end);
            return tail;
        }
    }
    if (s.find("://") != std::string::npos) return {};
    for (char ch : s) {
        if (!std::isalnum(static_cast<unsigned char>(ch)) && ch != '_') return {};
    }
    return s;
}

static std::string lower_copy(std::string s) {
    std::transform(s.begin(), s.end(), s.begin(),
                   [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
    return s;
}

static bool join_chat_target(const std::string& raw, Value& out, std::string* error) {
    std::string hash = invite_hash(raw);
    if (!hash.empty()) {
        Value req = v_obj("joinChatByInviteLink");
        req.set("invite_link", v_str("https://t.me/+" + hash));
        if (!td_request(req, out, 30000)) {
            if (error) *error = "joinChatByInviteLink failed";
            return false;
        }
        return true;
    }
    std::string name = public_username(raw);
    if (name.empty()) {
        if (error) *error = "not an invite link or public username";
        return false;
    }
    Value req = v_obj("searchPublicChat");
    req.set("username", v_str(name));
    Value chat;
    if (!td_request(req, chat)) {
        if (error) *error = "chat not found: @" + name;
        return false;
    }
    long long chat_id = scjson::num(&chat, "id", 0);
    if (chat_id == 0) {
        if (error) *error = "chat not found: @" + name;
        return false;
    }
    Value jreq = v_obj("joinChat");
    jreq.set("chat_id", v_int(chat_id));
    Value jresp;
    if (!td_request(jreq, jresp, 30000)) {
        if (error) *error = "joinChat failed for @" + name;
        return false;
    }
    out = chat;
    return true;
}

static bool resolve_bot_user(const std::string& raw, long long* user_id, std::string* username,
                             std::string* error) {
    std::string name = public_username(raw);
    if (name.empty()) {
        if (error) *error = "invalid bot username";
        return false;
    }
    Value req = v_obj("searchPublicChat");
    req.set("username", v_str(name));
    Value chat;
    if (!td_request(req, chat)) {
        if (error) *error = "bot not found: @" + name;
        return false;
    }
    const Value* type = chat.get("type");
    if (!type || scjson::str(type, "@type") != "chatTypePrivate") {
        if (error) *error = "@" + name + " is not a private bot account";
        return false;
    }
    *user_id = scjson::num(type, "user_id", 0);
    if (username) *username = name;
    return *user_id != 0;
}


static void handle_message(const Value& msg, bool is_edit = false) {
    long long chat_id = scjson::num(&msg, "chat_id", 0);
    long long message_id = scjson::num(&msg, "id", 0);
    if (chat_id == 0 || message_id == 0) return;
    long long date = scjson::num(&msg, "date", 0);

    if (scjson::boolean(&msg, "is_outgoing", false)) {
        remember_own(chat_id, message_id);
        return;
    }

    const Value* content = msg.get("content");
    if (!content) return;
    const std::string content_type = scjson::str(content, "@type");

    std::string text;
    std::string media_type;
    int file_id = 0;
    std::string sticker_emoji;
    bool sticker_animated = false;
    int sticker_thumb_file_id = 0;
    int sticker_width = 0;
    int sticker_height = 0;
    std::vector<long long> custom_emoji_ids;
    bool has_poll = false;
    Value poll_json;

    auto extract_custom_emojis = [&](const Value* ft) {
        if (!ft) return;
        const Value* ents = ft->get("entities");
        if (ents && ents->type == scjson::Type::Array) {
            for (const auto& ent : ents->array) {
                const Value* type = ent.get("type");
                if (type && scjson::str(type, "@type") == "textEntityTypeCustomEmoji") {
                    long long ceid = scjson::num(type, "custom_emoji_id", 0);
                    if (ceid > 0) {
                        remember_custom_emoji(ceid, "✨");
                        custom_emoji_ids.push_back(ceid);
                    }
                }
            }
        }
    };
    extract_custom_emojis(content->get("text"));
    extract_custom_emojis(content->get("caption"));

    if (content_type == "messageText") {
        const Value* ft = content->get("text");
        text = scjson::str(ft, "text");
    } else if (content_type == "messagePhoto") {
        media_type = "photo";
        const Value* photo = content->get("photo");
        const Value* best = largest_photo_size(photo);
        if (best) {
            const Value* file = best->get("photo");
            file_id = static_cast<int>(scjson::num(file, "id", 0));
        }
        text = scjson::str(content->get("caption"), "text");
    } else if (content_type == "messageVideo") {
        media_type = "video";
        const Value* video = content->get("video");
        const Value* file = video ? video->get("video") : nullptr;
        file_id = static_cast<int>(scjson::num(file, "id", 0));
        text = scjson::str(content->get("caption"), "text");
    } else if (content_type == "messageAnimation") {
        media_type = "gif";
        const Value* anim = content->get("animation");
        const Value* file = anim ? anim->get("animation") : nullptr;
        file_id = static_cast<int>(scjson::num(file, "id", 0));
        text = scjson::str(content->get("caption"), "text");
    } else if (content_type == "messageSticker") {
        media_type = "sticker";
        const Value* sticker = content->get("sticker");
        const Value* file = sticker ? sticker->get("sticker") : nullptr;
        file_id = static_cast<int>(scjson::num(file, "id", 0));
        sticker_emoji = scjson::str(sticker, "emoji");
        sticker_width = static_cast<int>(scjson::num(sticker, "width", 0));
        sticker_height = static_cast<int>(scjson::num(sticker, "height", 0));
        std::string sticker_format = scjson::str(sticker ? sticker->get("format") : nullptr, "@type");
        sticker_animated = (sticker_format == "stickerFormatTgs" ||
                            sticker_format == "stickerFormatWebm");
        // Animated stickers cannot be sent to a vision model directly, so grab
        // their static thumbnail as a description source.
        if (sticker_animated) {
            const Value* thumb = sticker ? sticker->get("thumbnail") : nullptr;
            const Value* tfile = thumb ? thumb->get("file") : nullptr;
            sticker_thumb_file_id = static_cast<int>(scjson::num(tfile, "id", 0));
        }
    } else if (content_type == "messageVoiceNote") {
        media_type = "voice";
        const Value* note = content->get("voice_note");
        const Value* file = note ? note->get("voice") : nullptr;
        file_id = static_cast<int>(scjson::num(file, "id", 0));
        text = scjson::str(content->get("caption"), "text");
    } else if (content_type == "messageAudio") {
        media_type = "audio";
        const Value* audio = content->get("audio");
        const Value* file = audio ? audio->get("audio") : nullptr;
        file_id = static_cast<int>(scjson::num(file, "id", 0));
        text = scjson::str(content->get("caption"), "text");
    } else if (content_type == "messageDocument") {
        media_type = "document";
        const Value* doc = content->get("document");
        const Value* file = doc ? doc->get("document") : nullptr;
        file_id = static_cast<int>(scjson::num(file, "id", 0));
        text = scjson::str(content->get("caption"), "text");
    } else if (content_type == "messageGame") {
        media_type = "game";
        const Value* game = content->get("game");
        text = scjson::str(game, "title");
        std::string description = scjson::str(game, "description");
        if (text.empty()) text = "game";
        if (!description.empty()) text += " — " + description;
    } else if (content_type == "messageVideoNote") {
        media_type = "video_msg";
        const Value* note = content->get("video_note");
        const Value* file = note ? note->get("video_note") : nullptr;
        file_id = static_cast<int>(scjson::num(file, "id", 0));
    } else if (content_type == "messagePoll") {
        // Poll vote counts change via updateMessageContent; re-running the model
        // on every vote (including our own) would loop, so only the first
        // delivery of the poll message is turned into an event.
        if (is_edit) return;
        media_type = "poll";
        const Value* poll = content->get("poll");
        const Value* question = poll ? poll->get("question") : nullptr;
        std::string question_text;
        if (question) {
            if (question->type == scjson::Type::String) {
                question_text = question->string;
            } else {
                question_text = scjson::str(question, "text");
            }
        }
        text = question_text.empty() ? "[poll]" : question_text;

        poll_json = scjson::make_object();
        poll_json.set("question", v_str(question_text));
        Value option_arr = scjson::make_array();
        const Value* options = poll ? poll->get("options") : nullptr;
        if (options && options->type == scjson::Type::Array) {
            int idx = 0;
            for (const auto& opt : options->array) {
                const Value* opt_text = opt.get("text");
                std::string option_text;
                if (opt_text) {
                    if (opt_text->type == scjson::Type::String) {
                        option_text = opt_text->string;
                    } else {
                        option_text = scjson::str(opt_text, "text");
                    }
                }
                Value item = scjson::make_object();
                item.set("index", v_int(idx));
                item.set("text", v_str(option_text));
                item.set("voter_count", v_int(scjson::num(&opt, "voter_count", 0)));
                item.set("is_chosen", v_bool(scjson::boolean(&opt, "is_chosen", false)));
                option_arr.push(item);
                idx++;
            }
        }
        poll_json.set("options", option_arr);
        poll_json.set("total_voter_count", v_int(scjson::num(poll, "total_voter_count", 0)));
        poll_json.set("is_anonymous", v_bool(scjson::boolean(poll, "is_anonymous", false)));
        const Value* poll_type = poll ? poll->get("type") : nullptr;
        std::string poll_type_name = scjson::str(poll_type, "@type");
        bool is_quiz = (poll_type_name == "pollTypeQuiz");
        bool multi = scjson::boolean(poll_type, "allow_multiple_answers", false) ||
                     scjson::boolean(poll_type, "allows_multiple_answers", false);
        poll_json.set("allows_multiple_answers", v_bool(!is_quiz && multi));
        poll_json.set("is_quiz", v_bool(is_quiz));
        poll_json.set("is_closed", v_bool(scjson::boolean(poll, "is_closed", false)));
        has_poll = true;
    } else {
        return;
    }

    if (text.empty() && media_type.empty()) return;

    long long sender_id = 0;
    bool sender_is_chat = false;
    const Value* sender = msg.get("sender_id");
    std::string sender_type = scjson::str(sender, "@type");
    if (sender_type == "messageSenderUser") {
        sender_id = scjson::num(sender, "user_id", 0);
    } else if (sender_type == "messageSenderChat") {
        sender_id = scjson::num(sender, "chat_id", 0);
        sender_is_chat = true;
    }

    std::string sender_name;
    std::string sender_username;
    if (sender_is_chat) {
        sender_name = sender_name_for(sender_id, true, nullptr);
    } else if (sender_id != 0) {
        fetch_user(sender_id, &sender_name, &sender_username);
    }

    long long reply_to_id = 0;
    bool reply_from_self = false;
    const Value* reply = msg.get("reply_to");
    if (reply && scjson::str(reply, "@type") == "messageReplyToMessage") {
        reply_to_id = scjson::num(reply, "message_id", 0);
        long long reply_chat_id = scjson::num(reply, "chat_id", 0);
        if (reply_chat_id == 0) reply_chat_id = chat_id;
        if (reply_to_id > 0) {
            reply_from_self = is_own(reply_chat_id, reply_to_id);
            if (!reply_from_self) {
                Value mreq = v_obj("getMessage");
                mreq.set("chat_id", v_int(reply_chat_id));
                mreq.set("message_id", v_int(reply_to_id));
                Value mresp;
                if (td_request(mreq, mresp, 2000)) {
                    if (scjson::boolean(&mresp, "is_outgoing", false)) {
                        reply_from_self = true;
                        remember_own(reply_chat_id, reply_to_id);
                    } else {
                        const Value* s = mresp.get("sender_id");
                        if (s && scjson::str(s, "@type") == "messageSenderUser") {
                            long long uid = scjson::num(s, "user_id", 0);
                            if (uid != 0 && uid == g_self_id.load()) {
                                reply_from_self = true;
                                remember_own(reply_chat_id, reply_to_id);
                            }
                        }
                    }
                }
            }
        }
    }

    int topic_id = 0;
    const Value* topic = msg.get("topic_id");
    if (topic) {
        std::string topic_type = scjson::str(topic, "@type");
        if (topic_type == "messageTopicForum") {
            topic_id = static_cast<int>(scjson::num(topic, "forum_topic_id", 0));
        } else if (topic_type == "messageTopicThread") {
            topic_id = static_cast<int>(scjson::num(topic, "message_thread_id", 0));
        }
    }
    bool forum = topic_id > 0 || chat_is_forum(chat_id);

    std::string photo_path;
    if (file_id > 0) {
        photo_path = download_file(file_id);
    }
    std::string sticker_thumb_path;
    if (sticker_animated && sticker_thumb_file_id > 0) {
        sticker_thumb_path = download_file(sticker_thumb_file_id);
    }

    Value ev = v_obj(is_edit ? "edit_message" : "new_message");
    ev.set("type", v_str(is_edit ? "edit_message" : "new_message"));
    ev.set("is_edit", v_bool(is_edit));
    ev.set("transport", v_str("telegram"));
    ev.set("chat_id", v_int(chat_id));
    ev.set("sender_id", v_int(sender_id));
    ev.set("sender_name", v_str(sender_name));
    ev.set("sender_username", v_str(sender_username));
    ev.set("message_id", v_int(message_id));
    ev.set("reply_to_id", v_int(reply_to_id));
    ev.set("reply_from_self", v_bool(reply_from_self));
    ev.set("self_username", v_str(g_self_username));
    ev.set("self_name", v_str(g_self_first_name));
    ev.set("self_id", v_int(g_self_id.load()));
    ev.set("topic_id", v_int(topic_id));
    ev.set("forum", v_bool(forum));
    ev.set("text", v_str(text));
    ev.set("media_type", v_str(media_type));
    ev.set("photo_path", v_str(photo_path));
    ev.set("date", v_int(date));

    // Messages sent through an inline bot carry via_bot_id: keep the
    // attribution so the model knows which bot produced the content.
    long long via_bot_id = scjson::num(&msg, "via_bot_id", 0);
    if (via_bot_id != 0) {
        std::string bot_name;
        std::string bot_username;
        fetch_user(via_bot_id, &bot_name, &bot_username);
        ev.set("via_bot_id", v_int(via_bot_id));
        ev.set("via_bot_name", v_str(bot_name));
        ev.set("via_bot_username", v_str(bot_username));
    }
    std::vector<button_info> buttons = message_buttons(&msg);
    if (!buttons.empty()) {
        ev.set("buttons", buttons_json(buttons));
    }

    if (!custom_emoji_ids.empty()) {
        Value ce_arr = scjson::make_array();
        for (long long cid : custom_emoji_ids) {
            ce_arr.push(v_int(cid));
        }
        ev.set("custom_emoji_ids", ce_arr);
    }
    if (has_poll) {
        ev.set("poll", poll_json);
    }
    if (media_type == "sticker") {
        ev.set("sticker_id", v_str(std::to_string(scjson::num(content->get("sticker"), "id", 0))));
        ev.set("sticker_emoji", v_str(sticker_emoji));
        ev.set("sticker_file_id", v_int(file_id));
        ev.set("sticker_animated", v_bool(sticker_animated));
        ev.set("sticker_width", v_int(sticker_width));
        ev.set("sticker_height", v_int(sticker_height));
        if (!sticker_thumb_path.empty()) {
            ev.set("sticker_thumb_path", v_str(sticker_thumb_path));
        }
    }

    log_msg(SC_LOG_DEBUG, "new message chat=" + std::to_string(chat_id) +
                              " id=" + std::to_string(message_id) +
                              " media=" + media_type);
    emit_ingest(scjson::dump(ev));
}

// ---------------------------------------------------------------------
// authorization
// ---------------------------------------------------------------------

static void emit_auth_event(const std::string& event_json) {
    if (!g_core || !g_core->syscall) return;
    char* out = nullptr;
    g_core->syscall("webui.auth_event", event_json.c_str(), &out);
    if (out && g_core->free) g_core->free(out);
}

static void emit_status() {
    if (!g_core || !g_core->transport_status) return;
    bool connected = g_ready.load() && g_net_ready.load();
    Value detail = scjson::make_object();
    detail.set("username", v_str(g_self_username));
    detail.set("user_id", v_int(g_self_id.load()));
    g_core->transport_status("telegram", connected ? 1 : 0, scjson::dump(detail).c_str());
}

static std::string ask_text(const char* question) {
    if (!g_core || !g_core->prompt_text) return {};
    char buf[512] = {0};
    if (g_core->prompt_text(question, "", buf, sizeof(buf)) <= 0) return {};
    return std::string(buf);
}

static std::string current_auth_state();
static std::atomic<bool> g_phone_worker{false};
static std::atomic<bool> g_code_worker{false};
static std::atomic<bool> g_password_worker{false};

// sleep_in_state waits while tdlib remains in expected auth state
static bool sleep_in_state(const char* expected, int seconds) {
    for (int i = 0; i < seconds; ++i) {
        if (!g_loaded.load() || g_stopping.load()) return false;
        if (current_auth_state() != expected) return false;
        std::this_thread::sleep_for(std::chrono::seconds(1));
    }
    return true;
}

static void request_tdlib_parameters() {
    Value req = v_obj("setTdlibParameters");
    req.set("use_test_dc", v_bool(false));
    req.set("database_directory", v_str(g_db_dir));
    req.set("files_directory", v_str(g_files_dir));
    req.set("database_encryption_key", v_str(g_database_encryption_key));
    req.set("use_file_database", v_bool(true));
    req.set("use_chat_info_database", v_bool(true));
    req.set("use_message_database", v_bool(true));
    req.set("use_secret_chats", v_bool(false));
    req.set("api_id", v_int(g_api_id));
    req.set("api_hash", v_str(g_api_hash));
    req.set("system_language_code", v_str("en"));
    req.set("device_model", v_str(g_device_model));
    req.set("system_version", v_str(g_system_version));
    req.set("application_version", v_str(g_app_version));
    Value out;
    if (!td_request(req, out)) {
        log_msg(SC_LOG_ERROR, "setTdlibParameters failed");
        return;
    }
    if (!g_database_encryption_key.empty()) {
        log_msg(SC_LOG_INFO, "tdlib database encryption enabled (SQLCipher AES-256)");
    }
    log_msg(SC_LOG_INFO, "tdlib parameters accepted");
}

static void phone_auth_worker(std::string phone) {
    if (g_phone_worker.exchange(true)) return;
    const char* expected = "authorizationStateWaitPhoneNumber";
    for (int attempt = 0; attempt < 120 && phone.empty(); ++attempt) {
        if (current_auth_state() != expected) break;
        if (phone.empty()) {
            phone = ask_text("Telegram phone number (international format)");
        }
        if (!phone.empty()) break;
        if (attempt == 0 || attempt % 12 == 0) {
            log_msg(SC_LOG_INFO, "waiting for the phone number via WebUI prompt");
        }
        if (!sleep_in_state(expected, 5)) break;
    }
    if (phone.empty()) {
        log_msg(SC_LOG_ERROR, "phone authorization cancelled: empty phone");
        g_phone_worker.store(false);
        return;
    }
    Value settings = v_obj("phoneNumberAuthenticationSettings");
    settings.set("allow_flash_call", v_bool(false));
    settings.set("allow_missed_call", v_bool(false));
    settings.set("is_current_phone_number", v_bool(true));
    settings.set("has_unknown_phone_number", v_bool(false));
    settings.set("allow_sms_retriever_api", v_bool(false));
    Value firebase = v_obj("firebaseAuthenticationSettingsAndroid");
    settings.set("firebase_authentication_settings", firebase);
    settings.set("authentication_tokens", scjson::make_array());

    Value req = v_obj("setAuthenticationPhoneNumber");
    req.set("phone_number", v_str(phone));
    req.set("settings", settings);
    Value out;
    if (!td_request(req, out)) {
        log_msg(SC_LOG_ERROR, "setAuthenticationPhoneNumber failed: " +
                                  scjson::str(&out, "message"));
    }
    g_phone_worker.store(false);
}

static void code_auth_worker() {
    if (g_code_worker.exchange(true)) return;
    const char* expected = "authorizationStateWaitCode";
    for (int attempt = 0; attempt < 10; ++attempt) {
        if (current_auth_state() != expected) break;
        std::string code = ask_text("Telegram login code");
        if (code.empty()) {
            if (!sleep_in_state(expected, 5)) break;
            continue;
        }
        Value req = v_obj("checkAuthenticationCode");
        req.set("code", v_str(code));
        Value out;
        if (td_request(req, out)) {
            for (int i = 0; i < 50; ++i) {
                if (current_auth_state() != expected) break;
                std::this_thread::sleep_for(std::chrono::milliseconds(100));
            }
            break;
        }
        log_msg(SC_LOG_ERROR, "checkAuthenticationCode failed: " +
                                  scjson::str(&out, "message"));
        std::this_thread::sleep_for(std::chrono::milliseconds(800));
    }
    g_code_worker.store(false);
}

static void password_auth_worker() {
    if (g_password_worker.exchange(true)) return;
    const char* expected = "authorizationStateWaitPassword";
    for (int attempt = 0; attempt < 10; ++attempt) {
        if (current_auth_state() != expected) break;
        std::string password = ask_text("Telegram 2FA password");
        if (password.empty()) {
            if (!sleep_in_state(expected, 5)) break;
            continue;
        }
        Value req = v_obj("checkAuthenticationPassword");
        req.set("password", v_str(password));
        Value out;
        if (td_request(req, out)) {
            for (int i = 0; i < 50; ++i) {
                if (current_auth_state() != expected) break;
                std::this_thread::sleep_for(std::chrono::milliseconds(100));
            }
            break;
        }
        log_msg(SC_LOG_ERROR, "checkAuthenticationPassword failed: " +
                                  scjson::str(&out, "message"));
        std::this_thread::sleep_for(std::chrono::milliseconds(800));
    }
    g_password_worker.store(false);
}

static void load_custom_emoji_packs() {
    Value req = v_obj("getInstalledStickerSets");
    req.set("sticker_type", v_obj("stickerTypeCustomEmoji"));
    Value out;
    if (!td_request(req, out, 15000)) return;
    const Value* sets = out.get("sets");
    if (!sets || sets->type != scjson::Type::Array) return;
    int loaded = 0;
    for (const auto& set_info : sets->array) {
        long long set_id = scjson::num(&set_info, "id", 0);
        std::string title = scjson::str(&set_info, "title");
        std::string name = scjson::str(&set_info, "name");
        if (set_id == 0) continue;

        Value sreq = v_obj("getStickerSet");
        sreq.set("set_id", v_int(set_id));
        Value sout;
        if (!td_request(sreq, sout, 10000)) continue;
        const Value* stickers = sout.get("stickers");
        if (!stickers || stickers->type != scjson::Type::Array) continue;
        for (const auto& st : stickers->array) {
            long long cid = 0;
            const Value* custom = st.get("custom_emoji_id");
            if (custom) {
                if (custom->type == scjson::Type::Number) cid = custom->integer;
                else if (custom->type == scjson::Type::String && !custom->string.empty()) {
                    try { cid = std::stoll(custom->string); } catch (...) {}
                }
            }
            if (cid == 0) {
                cid = scjson::num(&st, "custom_emoji_id", 0);
            }
            std::string emoji = scjson::str(&st, "emoji");
            if (cid != 0) {
                remember_custom_emoji(cid, emoji, title.empty() ? name : title);
                loaded++;
            }
        }
    }
    if (loaded > 0) {
        log_msg(SC_LOG_INFO, "loaded " + std::to_string(loaded) + " custom emojis from installed packs");
    }
}

static std::string download_custom_emoji(long long custom_emoji_id, std::string* out_fallback = nullptr, std::string* out_pack = nullptr) {
    if (custom_emoji_id <= 0) return {};
    Value req = v_obj("getCustomEmojiStickers");
    Value ids = scjson::make_array();
    ids.push(v_int(custom_emoji_id));
    req.set("custom_emoji_ids", ids);
    Value out;
    if (!td_request(req, out, 15000)) return {};
    const Value* stickers = out.get("stickers");
    if (!stickers || stickers->type != scjson::Type::Array || stickers->array.empty()) return {};

    const Value& st = stickers->array[0];
    if (out_fallback) {
        *out_fallback = scjson::str(&st, "emoji");
    }

    int file_id = 0;
    std::string format = scjson::str(st.get("format"), "@type");
    if (format == "stickerFormatWebp") {
        const Value* file = st.get("sticker");
        if (file) file_id = static_cast<int>(scjson::num(file, "id", 0));
    }
    if (file_id == 0) {
        const Value* thumb = st.get("thumbnail");
        if (thumb) {
            const Value* file = thumb->get("file");
            if (file) file_id = static_cast<int>(scjson::num(file, "id", 0));
        }
    }
    if (file_id == 0) {
        const Value* file = st.get("sticker");
        if (file) file_id = static_cast<int>(scjson::num(file, "id", 0));
    }

    if (out_pack) {
        long long set_id = scjson::num(&st, "set_id", 0);
        if (set_id != 0) {
            Value sreq = v_obj("getStickerSet");
            sreq.set("set_id", v_int(set_id));
            Value sout;
            if (td_request(sreq, sout, 5000)) {
                *out_pack = scjson::str(&sout, "title");
                if (out_pack->empty()) *out_pack = scjson::str(&sout, "name");
            }
        }
    }

    if (file_id > 0) {
        return download_file(file_id);
    }
    return {};
}

static void on_ready() {
    Value req = v_obj("getMe");
    Value out;
    if (td_request(req, out)) {
        g_self_id.store(scjson::num(&out, "id", 0));
        g_self_username = user_username(&out);
        g_self_first_name = scjson::str(&out, "first_name");
        std::string name = join_name(&out);
        log_msg(SC_LOG_INFO, "authorized as " + name +
                                  (g_self_username.empty() ? "" : " (@" + g_self_username + ")"));
    }
    g_ready.store(true);
    emit_status();
    Value auth_ev = v_obj("telegram_auth_state");
    auth_ev.set("type", v_str("telegram_auth_state"));
    Value data = v_obj("");
    data.set("state", v_str("connected"));
    data.set("username", v_str(g_self_username));
    data.set("first_name", v_str(g_self_first_name));
    data.set("user_id", v_int(g_self_id.load()));
    auth_ev.set("data", data);
    emit_auth_event(scjson::dump(auth_ev));

    std::thread([]() {
        std::this_thread::sleep_for(std::chrono::seconds(2));
        load_custom_emoji_packs();
    }).detach();
}

static std::string current_auth_state() {
    std::lock_guard<std::mutex> lock(g_state_mu);
    return g_auth_state;
}

static int parse_retry_after(const std::string& msg) {
    const std::string key = "retry after ";
    auto pos = msg.find(key);
    if (pos == std::string::npos) return 0;
    pos += key.size();
    int value = 0;
    while (pos < msg.size() && msg[pos] >= '0' && msg[pos] <= '9') {
        value = value * 10 + (msg[pos] - '0');
        ++pos;
    }
    return value;
}

// qr_auth_worker requests qr login url and handles flood wait
static void qr_auth_worker() {
    if (g_qr_worker.exchange(true)) return;
    // wait for connectionStateReady before requesting qr
    for (int i = 0; i < 90 && !g_net_ready.load(); ++i) {
        if (!g_loaded.load() || g_stopping.load() || g_ready.load()) {
            g_qr_worker.store(false);
            return;
        }
        if (i == 10) {
            if (g_proxy.empty()) {
                log_msg(SC_LOG_WARN,
                        "Telegram DC is unreachable; direct access is blocked, "
                        "start a local proxy or set [plugins.telegram] proxy");
            } else {
                log_msg(SC_LOG_WARN,
                        "Telegram DC is unreachable through proxy " + g_proxy +
                            "; check that the proxy is running");
            }
        }
        std::this_thread::sleep_for(std::chrono::seconds(1));
    }
    if (!g_net_ready.load()) {
        log_msg(SC_LOG_WARN, "QR login postponed: network is not ready");
        g_qr_worker.store(false);
        return;
    }
    for (int attempt = 0; attempt < 10; ++attempt) {
        if (!g_loaded.load() || g_stopping.load() || g_ready.load()) break;
        if (current_auth_state() != "authorizationStateWaitPhoneNumber") break;
        Value req = v_obj("requestQrCodeAuthentication");
        req.set("other_user_ids", scjson::make_array());
        Value out;
        log_msg(SC_LOG_INFO, "requesting QR code login");
        bool got = g_td.request(req, out, 60000);
        if (got && !td_error(out)) {
            log_msg(SC_LOG_INFO, "QR code login requested");
            break;
        }
        std::string msg = got ? scjson::str(&out, "message") : "no response (timeout)";
        if (got && msg.empty()) msg = scjson::dump(out);
        int wait = parse_retry_after(msg);
        if (wait <= 0) wait = 15;
        if (wait > 300) wait = 300;
        log_msg(SC_LOG_WARN, "requestQrCodeAuthentication failed: " +
                                 (msg.empty() ? "unknown error" : msg) +
                                 "; retry in " + std::to_string(wait) + "s");
        for (int slept = 0; slept < wait; ++slept) {
            if (!g_loaded.load() || g_stopping.load() || g_ready.load()) {
                g_qr_worker.store(false);
                return;
            }
            if (current_auth_state() != "authorizationStateWaitPhoneNumber") {
                g_qr_worker.store(false);
                return;
            }
            std::this_thread::sleep_for(std::chrono::seconds(1));
        }
    }
    g_qr_worker.store(false);
}

static void handle_auth_state(const Value& state) {
    std::string type = scjson::str(&state, "@type");
    {
        std::lock_guard<std::mutex> lock(g_state_mu);
        g_auth_state = type;
    }
    log_msg(SC_LOG_INFO, "auth state: " + type);

    if (type == "authorizationStateWaitTdlibParameters") {
        request_tdlib_parameters();
    } else if (type == "authorizationStateWaitEncryptionKey") {
        log_msg(SC_LOG_INFO, "authorizing encrypted TDLib database with encryption key...");
        Value req = v_obj("checkDatabaseEncryptionKey");
        req.set("encryption_key", v_str(g_database_encryption_key));
        Value out;
        if (!td_request(req, out)) {
            log_msg(SC_LOG_ERROR, "checkDatabaseEncryptionKey failed");
        } else {
            log_msg(SC_LOG_INFO, "checkDatabaseEncryptionKey accepted");
        }
    } else if (type == "authorizationStateWaitPhoneNumber") {
        if (g_login_mode == "qr") {
            std::thread(qr_auth_worker).detach();
        } else {
            std::thread(phone_auth_worker, g_phone).detach();
        }
    } else if (type == "authorizationStateWaitOtherDeviceConfirmation") {
        std::string link = scjson::str(&state, "link");
        Value data = scjson::make_object();
        data.set("link", v_str(link));
        data.set("qr", v_str(link));
        data.set("expires_at", v_int(now_seconds() + 30));
        Value ev = v_obj("telegram_qr");
        ev.set("type", v_str("telegram_qr"));
        ev.set("data", data);
        emit_auth_event(scjson::dump(ev));
        emit_auth_event(
            "{\"type\":\"telegram_auth_state\",\"data\":{\"state\":\"waiting_qr\"}}");
    } else if (type == "authorizationStateWaitCode") {
        std::thread(code_auth_worker).detach();
    } else if (type == "authorizationStateWaitPassword") {
        std::thread(password_auth_worker).detach();
    } else if (type == "authorizationStateReady") {
        on_ready();
    } else if (type == "authorizationStateLoggingOut" ||
               type == "authorizationStateClosing" ||
               type == "authorizationStateClosed") {
        g_ready.store(false);
        emit_status();
        emit_auth_event(
            "{\"type\":\"telegram_auth_state\",\"data\":{\"state\":\"disconnected\"}}");
    }
}

static void handle_connection_state(const Value& state) {
    std::string type = scjson::str(&state, "@type");
    log_msg(SC_LOG_INFO, "connection state: " + type);
    if (type == "connectionStateReady") {
        g_net_ready.store(true);
        emit_status();
        // restart qr authentication when network becomes ready
        if (g_login_mode == "qr" && !g_ready.load() &&
            current_auth_state() == "authorizationStateWaitPhoneNumber") {
            std::thread(qr_auth_worker).detach();
        }
    } else if (type == "connectionStateWaitingForNetwork" ||
               type == "connectionStateConnecting" ||
               type == "connectionStateConnectingToProxy" ||
               type == "connectionStateUpdating") {
        g_net_ready.store(false);
        emit_status();
    }
}

static void handle_callback_query(const Value& q) {
    long long callback_id = scjson::num(&q, "id", 0);
    long long chat_id = scjson::num(&q, "chat_id", 0);
    long long message_id = scjson::num(&q, "message_id", 0);
    if (chat_id == 0 || message_id == 0) return;

    long long sender_id = scjson::num(&q, "sender_user_id", 0);
    std::string data;
    const Value* payload = q.get("payload");
    if (payload) {
        std::string ptype = scjson::str(payload, "@type");
        if (ptype == "callbackQueryPayloadData" ||
            ptype == "callbackQueryPayloadDataWithPassword") {
            data = scjson::str(payload, "data");
        } else if (ptype == "callbackQueryPayloadGame") {
            data = scjson::str(payload, "game_short_name");
        }
    }
    std::string data_text = base64_decode(data);

    std::string sender_name;
    std::string sender_username;
    if (sender_id != 0) fetch_user(sender_id, &sender_name, &sender_username);

    std::string text;
    int topic_id = 0;
    std::vector<button_info> buttons;
    long long via_bot_id = 0;
    std::string via_bot_name;
    std::string via_bot_username;
    Value mreq = v_obj("getMessage");
    mreq.set("chat_id", v_int(chat_id));
    mreq.set("message_id", v_int(message_id));
    Value mresp;
    if (td_request(mreq, mresp, 3000)) {
        const Value* content = mresp.get("content");
        if (content) {
            text = scjson::str(content->get("text"), "text");
            if (text.empty()) text = scjson::str(content->get("caption"), "text");
        }
        topic_id = extract_topic_id(mresp);
        buttons = message_buttons(&mresp);
        via_bot_id = scjson::num(&mresp, "via_bot_id", 0);
        if (via_bot_id != 0) fetch_user(via_bot_id, &via_bot_name, &via_bot_username);
    }

    std::string button_text;
    for (const auto& b : buttons) {
        if (!data.empty() && b.data == data) {
            button_text = b.text;
            break;
        }
    }

    Value ev = v_obj("callback_query");
    ev.set("type", v_str("callback_query"));
    ev.set("transport", v_str("telegram"));
    ev.set("callback_query_id", v_int(callback_id));
    ev.set("callback_data", v_str(data));
    ev.set("callback_data_text", v_str(data_text));
    ev.set("button_text", v_str(button_text));
    ev.set("chat_id", v_int(chat_id));
    ev.set("message_id", v_int(message_id));
    ev.set("sender_id", v_int(sender_id));
    ev.set("sender_name", v_str(sender_name));
    ev.set("sender_username", v_str(sender_username));
    ev.set("self_username", v_str(g_self_username));
    ev.set("self_name", v_str(g_self_first_name));
    ev.set("self_id", v_int(g_self_id.load()));
    ev.set("topic_id", v_int(topic_id));
    ev.set("forum", v_bool(topic_id > 0 || chat_is_forum(chat_id)));
    ev.set("text", v_str(text));
    ev.set("date", v_int(now_seconds()));
    if (via_bot_id != 0) {
        ev.set("via_bot_id", v_int(via_bot_id));
        ev.set("via_bot_name", v_str(via_bot_name));
        ev.set("via_bot_username", v_str(via_bot_username));
    }
    if (!buttons.empty()) {
        ev.set("buttons", buttons_json(buttons));
    }

    log_msg(SC_LOG_DEBUG, "callback query chat=" + std::to_string(chat_id) +
                              " id=" + std::to_string(message_id) +
                              " button=" + button_text);
    emit_ingest(scjson::dump(ev));
}

static void handle_update(const Value& update) {
    std::string type = scjson::str(&update, "@type");
    if (type == "updateAuthorizationState") {
        const Value* state = update.get("authorization_state");
        if (state) handle_auth_state(*state);
    } else if (type == "updateConnectionState") {
        const Value* state = update.get("state");
        if (state) handle_connection_state(*state);
    } else if (type == "updateNewMessage") {
        const Value* msg = update.get("message");
        if (msg) handle_message(*msg, false);
    } else if (type == "updateNewCallbackQuery") {
        handle_callback_query(update);
    } else if (type == "updateMessageContent") {
        long long chat_id = scjson::num(&update, "chat_id", 0);
        long long message_id = scjson::num(&update, "message_id", 0);
        if (chat_id != 0 && message_id != 0) {
            Value mreq = v_obj("getMessage");
            mreq.set("chat_id", v_int(chat_id));
            mreq.set("message_id", v_int(message_id));
            Value mresp;
            if (td_request(mreq, mresp, 2000)) {
                handle_message(mresp, true);
            }
        }
    } else if (type == "updateMessageEdited") {
        long long chat_id = scjson::num(&update, "chat_id", 0);
        long long message_id = scjson::num(&update, "message_id", 0);
        if (chat_id != 0 && message_id != 0) {
            Value mreq = v_obj("getMessage");
            mreq.set("chat_id", v_int(chat_id));
            mreq.set("message_id", v_int(message_id));
            Value mresp;
            if (td_request(mreq, mresp, 2000)) {
                handle_message(mresp, true);
            }
        }
    } else if (type == "updateMessageSendSucceeded") {
        long long old_id = scjson::num(&update, "old_message_id", 0);
        const Value* msg = update.get("message");
        if (msg && old_id != 0) {
            long long new_id = scjson::num(msg, "id", 0);
            long long chat_id = scjson::num(msg, "chat_id", 0);
            if (new_id != 0) {
                record_message_id_mapping(old_id, new_id);
                remember_own(chat_id, new_id);
            }
        }
    } else if (type == "updateMessageSendFailed") {
        long long old_id = scjson::num(&update, "old_message_id", 0);
        std::string err = scjson::str(&update, "error_message");
        log_msg(SC_LOG_WARN, "message send failed for temp_id=" + std::to_string(old_id) + ": " + err);
    } else if (type == "updateUser") {
        const Value* user = update.get("user");
        if (user) {
            long long id = scjson::num(user, "id", 0);
            std::string name = join_name(user);
            std::string username = user_username(user);
            std::string first_name = scjson::str(user, "first_name");
            if (id != 0 && id == g_self_id.load()) {
                if (!username.empty()) g_self_username = username;
                if (!first_name.empty()) g_self_first_name = first_name;
            }
            std::lock_guard<std::mutex> lock(g_cache_mu);
            g_user_names[id] = name;
            g_user_usernames[id] = username;
        }
    }
}

// ---------------------------------------------------------------------
// start and stop tdlib
// ---------------------------------------------------------------------

static std::string choose_session_dir() {
    fs::path base;
    if (!g_session_path.empty()) {
        base = fs::path(g_session_path);
        if (base.is_relative()) base = fs::absolute(base);
        std::error_code st;
        if (fs::exists(base, st) && !fs::is_directory(base, st)) {
            // tdlib requires a directory for session storage
            log_msg(SC_LOG_WARN, "session_path is a file, ignoring it: " + base.string());
            base.clear();
        }
    }
    if (base.empty()) {
        base = fs::path(g_data_dir) / "tdlib";
    }
    g_db_dir = (base / "db").string();
    g_files_dir = (base / "files").string();
    std::error_code ec;
    fs::create_directories(g_db_dir, ec);
    fs::create_directories(g_files_dir, ec);
    return base.string();
}

// ---------------------------------------------------------------------
// proxy configuration
// ---------------------------------------------------------------------

struct proxy_cfg {
    std::string type = "socks5";
    std::string host;
    int port = 0;
    std::string user;
    std::string pass;
};

static bool port_open(const std::string& host, int port) {
    struct addrinfo hints {};
    hints.ai_family = AF_INET;
    hints.ai_socktype = SOCK_STREAM;
    struct addrinfo* res = nullptr;
    const std::string service = std::to_string(port);
    if (getaddrinfo(host.c_str(), service.c_str(), &hints, &res) != 0 || !res) {
        return false;
    }
    bool opened = false;
    for (auto* p = res; p && !opened; p = p->ai_next) {
#if defined(_WIN32)
        SOCKET s = socket(p->ai_family, p->ai_socktype, p->ai_protocol);
        if (s == INVALID_SOCKET) continue;
        opened = connect(s, p->ai_addr, static_cast<int>(p->ai_addrlen)) == 0;
        closesocket(s);
#else
        int s = socket(p->ai_family, p->ai_socktype, p->ai_protocol);
        if (s < 0) continue;
        opened = connect(s, p->ai_addr, p->ai_addrlen) == 0;
        close(s);
#endif
    }
    freeaddrinfo(res);
    return opened;
}

#if defined(_WIN32)
static void ensure_winsock() {
    static const bool ready = [] {
        WSADATA data;
        return WSAStartup(MAKEWORD(2, 2), &data) == 0;
    }();
    (void)ready;
}
#else
static void ensure_winsock() {}
#endif

static bool parse_proxy_url(const std::string& url, proxy_cfg& out) {
    if (url.rfind("tg://", 0) == 0 || url.find("t.me/") != std::string::npos) {
        auto get_param = [&](const std::string& key) -> std::string {
            auto pos = url.find(key + "=");
            if (pos == std::string::npos) return "";
            pos += key.length() + 1;
            auto end = url.find('&', pos);
            return url.substr(pos, end == std::string::npos ? std::string::npos : end - pos);
        };
        std::string srv = get_param("server");
        if (srv.empty()) srv = get_param("host");
        std::string prt = get_param("port");
        if (!srv.empty() && !prt.empty()) {
            out.host = srv;
            out.port = std::atoi(prt.c_str());
            out.pass = get_param("secret");
            if (!out.pass.empty()) {
                out.type = "mtproto";
            } else {
                out.user = get_param("user");
                if (out.user.empty()) out.user = get_param("username");
                out.pass = get_param("pass");
                if (out.pass.empty()) out.pass = get_param("password");
                out.type = "socks5";
            }
            return (out.port > 0 && out.port <= 65535);
        }
    }

    const auto scheme_end = url.find("://");
    if (scheme_end == std::string::npos) return false;
    std::string scheme = url.substr(0, scheme_end);
    std::transform(scheme.begin(), scheme.end(), scheme.begin(),
                   [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
    std::string rest = url.substr(scheme_end + 3);
    const auto at = rest.rfind('@');
    if (at != std::string::npos) {
        const std::string userinfo = rest.substr(0, at);
        rest = rest.substr(at + 1);
        const auto colon = userinfo.find(':');
        out.user = userinfo.substr(0, colon);
        if (colon != std::string::npos) out.pass = userinfo.substr(colon + 1);
    }
    const auto colon = rest.rfind(':');
    if (colon == std::string::npos) return false;
    out.host = rest.substr(0, colon);
    out.port = std::atoi(rest.c_str() + colon + 1);
    if (out.host.empty() || out.port <= 0 || out.port > 65535) return false;
    if (scheme == "http" || scheme == "https") {
        out.type = "http";
    } else if (scheme == "socks5" || scheme == "socks") {
        out.type = "socks5";
    } else if (scheme == "mtproto") {
        out.type = "mtproto";
    } else {
        return false;
    }
    return true;
}

// detect_local_proxy checks common local clash/mixed proxy ports
static bool detect_local_proxy(proxy_cfg& out) {
    ensure_winsock();
    static const int ports[] = {7890, 7891, 7897, 7893, 1080, 10808, 10809};
    for (int port : ports) {
        if (port_open("127.0.0.1", port)) {
            out.type = "socks5";
            out.host = "127.0.0.1";
            out.port = port;
            return true;
        }
    }
    return false;
}

static void apply_proxy() {
    if (g_proxy.empty()) {
        const char* env_p = getenv("TG_PROXY");
        if (!env_p || !env_p[0]) env_p = getenv("ALL_PROXY");
        if (env_p && env_p[0]) {
            g_proxy = env_p;
        }
    }
    proxy_cfg cfg;
    if (g_proxy == "none" || g_proxy == "off" || g_proxy == "direct") {
        log_msg(SC_LOG_INFO, "proxy disabled, connecting directly");
        return;
    }
    if (!g_proxy.empty()) {
        if (!parse_proxy_url(g_proxy, cfg)) {
            log_msg(SC_LOG_WARN, "invalid [plugins.telegram] proxy URL: " + g_proxy);
            return;
        }
    } else if (!detect_local_proxy(cfg)) {
        log_msg(SC_LOG_INFO, "proxy not configured and no local proxy port found");
        return;
    }

    Value type = v_obj(cfg.type == "http"     ? "proxyTypeHttp"
                       : cfg.type == "mtproto" ? "proxyTypeMtproto"
                                               : "proxyTypeSocks5");
    if (cfg.type == "mtproto") {
        type.set("secret", v_str(cfg.pass));
    } else {
        type.set("username", v_str(cfg.user));
        type.set("password", v_str(cfg.pass));
        if (cfg.type == "http") type.set("http_only", v_bool(false));
    }
    // format nested tdlib proxy object
    Value proxy = v_obj("proxy");
    proxy.set("server", v_str(cfg.host));
    proxy.set("port", v_int(cfg.port));
    proxy.set("type", type);
    Value req = v_obj("addProxy");
    req.set("proxy", proxy);
    req.set("enable", v_bool(true));
    req.set("comment", v_str("sperma"));
    Value out;
    if (td_request(req, out)) {
        log_msg(SC_LOG_INFO, "proxy enabled: " + cfg.type + "://" + cfg.host + ":" +
                                 std::to_string(cfg.port));
    } else {
        log_msg(SC_LOG_WARN, "addProxy failed: " + scjson::str(&out, "message"));
    }
}

static bool start_client() {
    if (g_loaded.load()) return true;
    if (g_api_id == 0 || g_api_hash.empty()) {
        log_msg(SC_LOG_WARN, "api_id/api_hash missing in config - Telegram is idle");
        return false;
    }
    if (g_data_dir.empty() && g_core && g_core->data_dir) {
        char* dir = (char*)g_core->data_dir();
        if (dir) {
            g_data_dir = dir;
            if (g_core->free) g_core->free(dir);
        }
    }
    if (g_data_dir.empty()) g_data_dir = "data/telegram";
    g_data_root = fs::path(g_data_dir).parent_path().string();
    fs::create_directories(g_data_dir);
    choose_session_dir();

    std::string module_dir = current_module_dir();
    std::vector<std::string> dirs;
    if (!module_dir.empty()) {
        dirs.push_back(module_dir);
        dirs.push_back((fs::path(module_dir) / "tdlib").string());
    }
    dirs.push_back(g_data_dir);
    dirs.push_back((fs::path(g_data_dir) / "tdlib").string());

    std::string error;
    bool loaded = false;
    for (const auto& dir : dirs) {
        if (g_td.load(dir, &error)) {
            loaded = true;
            log_msg(SC_LOG_INFO, "tdjson loaded from " + dir);
            break;
        }
    }
    if (!loaded) {
        log_msg(SC_LOG_WARN,
                "tdjson library not found (put tdjson.dll next to the module or in data/telegram); "
                "Telegram transport is offline");
        return false;
    }

    g_td.set_event_handler(handle_update);
    tdjson::g_log_sink = [](int level, const char* message) {
        if (!message) return;
        log_msg(level <= 1 ? SC_LOG_WARN : SC_LOG_INFO,
                std::string("tdlib: ") + message);
    };
    if (!g_td.create(&error)) {
        log_msg(SC_LOG_ERROR, "tdlib client create failed: " + error);
        g_td.close();
        return false;
    }
    g_loaded.store(true);
    g_stopping.store(false);
    apply_proxy();
    log_msg(SC_LOG_INFO, "tdlib client started");

    std::thread([]() {
        for (int i = 0; i < 15; ++i) {
            std::this_thread::sleep_for(std::chrono::seconds(1));
            if (!g_loaded.load() || g_stopping.load() || g_net_ready.load()) return;
        }
        if (g_loaded.load() && !g_stopping.load() && !g_net_ready.load()) {
            log_msg(SC_LOG_WARN,
                    "Telegram connection not established after 15s. Direct access may be blocked; "
                    "recommend enabling [bypass] tgwsproxy = true in config.toml");
        }
    }).detach();

    return true;
}

static void stop_client() {
    if (!g_loaded.exchange(false)) return;
    g_stopping.store(true);
    g_ready.store(false);
    g_net_ready.store(false);
    Value req = v_obj("close");
    g_td.post(req);
    g_td.close();
    emit_status();
    log_msg(SC_LOG_INFO, "tdlib client stopped");
}

// ---------------------------------------------------------------------
// transport implementation
// ---------------------------------------------------------------------

struct send_params {
    long long chat_id = 0;
    long long reply_to = 0;
    int topic_id = 0;
    int part_index = 0;
    std::string text;
    std::string caption;
    std::string path;
    std::string media;
    int media_type = 0;
    int duration = 0;
};

static send_params parse_send(const char* payload) {
    send_params p;
    Value v;
    if (!payload || !scjson::parse(payload, v)) return p;
    std::string chat = scjson::str(&v, "chat_id");
    if (!chat.empty()) p.chat_id = std::strtoll(chat.c_str(), nullptr, 10);
    std::string reply = scjson::str(&v, "reply_to");
    if (!reply.empty()) p.reply_to = std::strtoll(reply.c_str(), nullptr, 10);
    p.topic_id = static_cast<int>(scjson::num(&v, "topic_id", 0));
    p.part_index = static_cast<int>(scjson::num(&v, "part_index", 0));
    p.text = scjson::str(&v, "text");
    p.caption = scjson::str(&v, "caption");
    p.path = scjson::str(&v, "path");
    p.media = scjson::str(&v, "media_type");
    p.media_type = static_cast<int>(scjson::num(&v, "media_type", 0));
    p.duration = static_cast<int>(scjson::num(&v, "duration", 0));

    // reply-to policy is handled by transport implementation
    if (!g_reply_to_enabled || (g_reply_first_only && p.part_index > 0)) {
        p.reply_to = 0;
    }
    return p;
}

static sc_status_t transport_result(const std::string& json, char** out) {
    if (out) *out = dup_string(json);
    return SC_OK;
}

static sc_status_t transport_error(const std::string& reason, char** out) {
    Value v = scjson::make_object();
    v.set("status", v_str("error"));
    v.set("reason", v_str(reason));
    if (out) *out = dup_string(scjson::dump(v));
    return SC_ERR_INTERNAL;
}

static sc_status_t transport_send_text(const char* payload, char** out) {
    send_params p = parse_send(payload);
    if (p.chat_id == 0 || p.text.empty()) return transport_error("chat_id and text required", out);
    long long message_id = 0;
    std::string error;
    if (!send_text(p.chat_id, p.text, p.reply_to, p.topic_id, &message_id, &error)) {
        return transport_error(error, out);
    }
    Value v = scjson::make_object();
    v.set("status", v_str("ok"));
    v.set("message_id", v_str(std::to_string(message_id)));
    v.set("chat_id", v_str(std::to_string(p.chat_id)));
    return transport_result(scjson::dump(v), out);
}

static bool ends_with_ci(const std::string& str, const std::string& suffix) {
    if (str.size() < suffix.size()) return false;
    for (size_t i = 0; i < suffix.size(); ++i) {
        if (std::tolower(static_cast<unsigned char>(str[str.size() - suffix.size() + i])) !=
            std::tolower(static_cast<unsigned char>(suffix[i]))) {
            return false;
        }
    }
    return true;
}

static sc_status_t transport_send_media(const char* payload, char** out) {
    send_params p = parse_send(payload);
    if (p.chat_id == 0 && p.path.empty()) return transport_error("chat_id and path required", out);
    if (p.path.empty()) return transport_error("media path is empty", out);

    Value file = make_input_file(p.path);
    Value content;
    bool is_gif = (p.media == "gif" || ends_with_ci(p.path, ".gif"));
    if (is_gif) {
        // send gif as photo per user setting
        content = input_photo(file, p.caption);
    } else {
        switch (p.media_type) {
            case SC_MEDIA_PHOTO: content = input_photo(file, p.caption); break;
            case SC_MEDIA_VIDEO_MESSAGE: content = input_video_note(file); break;
            case SC_MEDIA_VOICE: content = input_voice(file, p.caption, p.duration); break;
            case SC_MEDIA_AUDIO: content = input_audio(file, p.caption, p.duration); break;
            case SC_MEDIA_STICKER:
            case SC_MEDIA_ANIMATED_STICKER: content = input_sticker(file, ""); break;
            case SC_MEDIA_DOCUMENT: content = input_document(file, p.caption); break;
            default:
                if (p.media == "photo") content = input_photo(file, p.caption);
                else if (p.media == "video") content = input_video(file, p.caption);
                else if (p.media == "voice") content = input_voice(file, p.caption, p.duration);
                else if (p.media == "audio") content = input_audio(file, p.caption, p.duration);
                else if (p.media == "sticker") content = input_sticker(file, "");
                else if (p.media == "video_msg") content = input_video_note(file);
                else content = input_document(file, p.caption);
                break;
        }
    }
    if (!p.caption.empty() && content.get("caption") == nullptr) {
        content.set("caption", formatted_text(p.caption));
    }

    long long message_id = 0;
    std::string error;
    if (!send_content(p.chat_id, content, p.reply_to, p.topic_id, &message_id, &error)) {
        return transport_error(error, out);
    }
    Value v = scjson::make_object();
    v.set("status", v_str("ok"));
    v.set("message_id", v_str(std::to_string(message_id)));
    v.set("chat_id", v_str(std::to_string(p.chat_id)));
    return transport_result(scjson::dump(v), out);
}

// make_reaction builds a TDLib reactionType from a user/model supplied value.
// Plain unicode emoji map to reactionTypeEmoji; Telegram Premium custom emoji
// ids map to reactionTypeCustomEmoji. Accepted custom forms:
//   "custom:ID", "id:ID", "[:ID:fallback:]" or a bare numeric id (7+ digits).
static Value make_reaction(const std::string& spec) {
    std::string emoji = spec;
    while (!emoji.empty() && std::isspace(static_cast<unsigned char>(emoji.front()))) emoji.erase(emoji.begin());
    while (!emoji.empty() && std::isspace(static_cast<unsigned char>(emoji.back()))) emoji.pop_back();
    long long custom_emoji_id = 0;
    if (emoji.rfind("custom:", 0) == 0) {
        try { custom_emoji_id = std::stoll(emoji.substr(7)); } catch (...) {}
    } else if (emoji.rfind("id:", 0) == 0) {
        try { custom_emoji_id = std::stoll(emoji.substr(3)); } catch (...) {}
    } else if (emoji.size() > 6 && std::all_of(emoji.begin(), emoji.end(), ::isdigit)) {
        try { custom_emoji_id = std::stoll(emoji); } catch (...) {}
    } else if (emoji.rfind("[:", 0) == 0) {
        size_t end = emoji.find(":]", 2);
        if (end != std::string::npos) {
            std::string inner = emoji.substr(2, end - 2);
            size_t col = inner.find(':');
            std::string id_str = (col == std::string::npos) ? inner : inner.substr(0, col);
            if (!id_str.empty() && std::all_of(id_str.begin(), id_str.end(), ::isdigit)) {
                try { custom_emoji_id = std::stoll(id_str); } catch (...) {}
            }
        }
    }

    Value reaction;
    if (custom_emoji_id != 0) {
        reaction = v_obj("reactionTypeCustomEmoji");
        reaction.set("custom_emoji_id", v_int(custom_emoji_id));
    } else {
        reaction = v_obj("reactionTypeEmoji");
        reaction.set("emoji", v_str(emoji));
    }
    return reaction;
}

static sc_status_t transport_react(const char* payload, char** out) {
    Value v;
    if (!payload || !scjson::parse(payload, v)) return transport_error("invalid payload", out);
    std::string chat = scjson::str(&v, "chat_id");
    std::string emoji = scjson::str(&v, "emoji");
    long long chat_id = std::strtoll(chat.c_str(), nullptr, 10);
    long long message_id = resolve_message_id(scjson::num(&v, "message_id", 0));
    if (chat_id == 0 || message_id == 0 || emoji.empty()) {
        return transport_error("chat_id, message_id and emoji required", out);
    }

    Value reaction = make_reaction(emoji);

    Value req = v_obj("addMessageReaction");
    req.set("chat_id", v_int(chat_id));
    req.set("message_id", v_int(message_id));
    req.set("reaction_type", reaction);
    req.set("is_big", v_bool(false));
    req.set("update_recent_reactions", v_bool(true));
    Value resp;
    if (!td_request(req, resp)) {
        std::string err_msg = scjson::str(&resp, "message");
        return transport_error("addMessageReaction failed" + (err_msg.empty() ? "" : ": " + err_msg), out);
    }
    return transport_result("{\"status\":\"ok\"}", out);
}

static sc_status_t transport_mark_read(const char* payload, char** out) {
    Value v;
    if (!payload || !scjson::parse(payload, v)) return transport_error("invalid payload", out);
    std::string chat = scjson::str(&v, "chat_id");
    long long chat_id = std::strtoll(chat.c_str(), nullptr, 10);
    long long message_id = resolve_message_id(scjson::num(&v, "message_id", 0));
    if (chat_id == 0 || message_id == 0) return transport_error("chat_id and message_id required", out);
    Value ids = scjson::make_array();
    ids.push(v_int(message_id));
    Value source = v_obj("messageSourceChatHistory");
    Value req = v_obj("viewMessages");
    req.set("chat_id", v_int(chat_id));
    req.set("message_ids", ids);
    req.set("source", source);
    req.set("force_read", v_bool(false));
    Value resp;
    if (!td_request(req, resp)) return transport_error("viewMessages failed", out);
    return transport_result("{\"status\":\"ok\"}", out);
}

static sc_status_t transport_typing(const char* payload, char** out) {
    Value v;
    if (!payload || !scjson::parse(payload, v)) return transport_error("invalid payload", out);
    std::string chat = scjson::str(&v, "chat_id");
    long long chat_id = std::strtoll(chat.c_str(), nullptr, 10);
    int topic_id = static_cast<int>(scjson::num(&v, "topic_id", 0));
    std::string action = scjson::str(&v, "action");
    if (chat_id == 0) return transport_error("chat_id required", out);
    Value req = v_obj("sendChatAction");
    req.set("chat_id", v_int(chat_id));
    req.set("topic_id", topic_id > 0 ? topic_forum(topic_id) : Value{});
    req.set("business_connection_id", v_str(""));
    if (action == "cancel") {
        req.set("action", v_obj("chatActionCancel"));
    } else if (action == "choosing_sticker") {
        req.set("action", v_obj("chatActionChoosingSticker"));
    } else if (action == "upload_photo" || action == "uploading_photo") {
        req.set("action", v_obj("chatActionUploadingPhoto"));
    } else if (action == "recording_voice") {
        req.set("action", v_obj("chatActionRecordingVoiceNote"));
    } else {
        req.set("action", v_obj("chatActionTyping"));
    }
    Value resp;
    g_td.post(req);
    return transport_result("{\"status\":\"ok\"}", out);
}

static sc_status_t transport_edit(const char* payload, char** out) {
    Value v;
    if (!payload || !scjson::parse(payload, v)) return transport_error("invalid payload", out);
    std::string chat = scjson::str(&v, "chat_id");
    long long chat_id = std::strtoll(chat.c_str(), nullptr, 10);
    if (chat_id == 0) chat_id = scjson::num(&v, "chat_id", 0);
    long long message_id = scjson::num(&v, "message_id", 0);
    std::string text = scjson::str(&v, "text");
    if (chat_id == 0 || message_id == 0) return transport_error("chat_id and message_id required", out);

    long long real_id = resolve_message_id(message_id);
    if (real_id == message_id) {
        real_id = wait_for_real_message_id(message_id, 1500);
    }

    Value req = v_obj("editMessageText");
    req.set("chat_id", v_int(chat_id));
    req.set("message_id", v_int(real_id));
    req.set("reply_markup", Value{});
    req.set("input_message_content", text_content(text));
    Value resp;
    if (!td_request(req, resp)) {
        std::string err_msg = scjson::str(&resp, "message");
        if (err_msg.find("MESSAGE_NOT_MODIFIED") != std::string::npos ||
            err_msg.find("message is not modified") != std::string::npos) {
            return transport_result("{\"status\":\"ok\"}", out);
        }
        long long retry_id = resolve_message_id(message_id);
        if (retry_id != real_id) {
            req.set("message_id", v_int(retry_id));
            if (td_request(req, resp)) {
                return transport_result("{\"status\":\"ok\"}", out);
            }
            err_msg = scjson::str(&resp, "message");
        }
        if (err_msg.empty()) err_msg = "unknown tdlib error";
        log_msg(SC_LOG_WARN, "editMessageText failed (chat=" + std::to_string(chat_id) +
                             " msg=" + std::to_string(real_id) + " orig=" + std::to_string(message_id) +
                             "): " + err_msg);
        return transport_error("editMessageText failed: " + err_msg, out);
    }
    return transport_result("{\"status\":\"ok\"}", out);
}

static sc_status_t transport_delete(const char* payload, char** out) {
    Value v;
    if (!payload || !scjson::parse(payload, v)) return transport_error("invalid payload", out);
    std::string chat = scjson::str(&v, "chat_id");
    long long chat_id = std::strtoll(chat.c_str(), nullptr, 10);
    if (chat_id == 0) chat_id = scjson::num(&v, "chat_id", 0);
    long long message_id = resolve_message_id(scjson::num(&v, "message_id", 0));
    if (chat_id == 0 || message_id == 0) return transport_error("chat_id and message_id required", out);
    Value ids = scjson::make_array();
    ids.push(v_int(message_id));
    Value req = v_obj("deleteMessages");
    req.set("chat_id", v_int(chat_id));
    req.set("message_ids", ids);
    req.set("revoke", v_bool(true));
    Value resp;
    if (!td_request(req, resp)) {
        std::string err_msg = scjson::str(&resp, "message");
        if (err_msg.empty()) err_msg = "unknown tdlib error";
        return transport_error("deleteMessages failed: " + err_msg, out);
    }
    return transport_result("{\"status\":\"ok\"}", out);
}

static sc_status_t transport_poll(const char* payload, char** out) {
    (void)payload;
    return transport_error("poll is not supported", out);
}

static const sc_transport_info_t* get_transport() {
    static const char* name = "telegram";
    static const char* display = "Telegram";
    static const char* description = "Telegram userbot transport (TDLib)";
    static const sc_transport_info_t info = {
        name,
        display,
        description,
        SC_TRANSPORT_CAP_TEXT | SC_TRANSPORT_CAP_MEDIA | SC_TRANSPORT_CAP_REACTIONS |
            SC_TRANSPORT_CAP_TYPING | SC_TRANSPORT_CAP_EDIT | SC_TRANSPORT_CAP_DELETE |
            SC_TRANSPORT_CAP_READ_RECEIPT | SC_TRANSPORT_CAP_THREADS,
        4096,
        0,
        {0, 0, 0},
        transport_send_text,
        transport_send_media,
        transport_react,
        transport_mark_read,
        transport_typing,
        transport_edit,
        transport_delete,
        transport_poll,
        {0, 0, 0, 0},
    };
    return &info;
}

// ---------------------------------------------------------------------
// stickers
// ---------------------------------------------------------------------

static std::string stickers_path() {
    if (!g_data_root.empty()) {
        fs::path p = fs::path(g_data_root) / "stickers.json";
        if (fs::exists(p)) return p.string();
    }
    if (!g_data_dir.empty()) {
        fs::path p = fs::path(g_data_dir) / "stickers.json";
        if (fs::exists(p)) return p.string();
    }
    return (fs::path(g_data_root.empty() ? "data" : g_data_root) / "stickers.json").string();
}

static bool load_stickers(std::vector<Value>& out) {
    std::string path = stickers_path();
    std::FILE* f = std::fopen(path.c_str(), "rb");
    if (!f) return false;
    std::string data;
    char buf[4096];
    size_t n = 0;
    while ((n = std::fread(buf, 1, sizeof(buf), f)) > 0) data.append(buf, n);
    std::fclose(f);
    Value root;
    if (!scjson::parse(data, root) || root.type != scjson::Type::Array) return false;
    out = std::move(root.array);
    return true;
}

static std::string emoji_path() {
    if (!g_data_root.empty()) {
        fs::path p = fs::path(g_data_root) / "emoji.json";
        if (fs::exists(p)) return p.string();
        p = fs::path(g_data_root) / "data" / "emoji.json";
        if (fs::exists(p)) return p.string();
    }
    if (!g_data_dir.empty()) {
        fs::path p = fs::path(g_data_dir) / "emoji.json";
        if (fs::exists(p)) return p.string();
    }
    return (fs::path(g_data_root.empty() ? "data" : g_data_root) / "emoji.json").string();
}

static bool load_emojis(std::vector<Value>& out) {
    std::string path = emoji_path();
    std::FILE* f = std::fopen(path.c_str(), "rb");
    if (!f) return false;
    std::string data;
    char buf[4096];
    size_t n = 0;
    while ((n = std::fread(buf, 1, sizeof(buf), f)) > 0) data.append(buf, n);
    std::fclose(f);
    Value root;
    if (!scjson::parse(data, root) || root.type != scjson::Type::Array) return false;
    out = std::move(root.array);
    return true;
}

static bool find_sticker(const std::string& sticker_id, Value& record) {
    std::vector<Value> items;
    if (!load_stickers(items)) return false;
    for (const auto& item : items) {
        if (scjson::str(&item, "id") == sticker_id) {
            record = item;
            return true;
        }
        std::string doc = std::to_string(scjson::num(&item, "document_id", 0));
        if (!doc.empty() && doc == sticker_id) {
            record = item;
            return true;
        }
    }
    return false;
}

static bool send_sticker(long long chat_id, const std::string& sticker_id, long long reply_to,
                         int topic_id, std::string* error) {
    Value rec;
    if (!find_sticker(sticker_id, rec)) {
        if (error) *error = "sticker not found in catalog";
        return false;
    }
    std::string emoji = scjson::str(&rec, "emoji");
    std::string local = scjson::str(&rec, "file");
    long long file_id = scjson::num(&rec, "file_id", 0);
    int width = static_cast<int>(scjson::num(&rec, "width", 0));
    int height = static_cast<int>(scjson::num(&rec, "height", 0));
    long long message_id = 0;
    std::string send_error;

    // Try the cached file id first, then fall back to the local copy. This makes
    // animated stickers (webm/tgs) resilient to stale file references.
    if (file_id > 0) {
        if (send_content(chat_id, input_sticker(file_known(static_cast<int>(file_id)), emoji, width, height),
                         reply_to, topic_id, &message_id, &send_error)) {
            return true;
        }
    }
    if (!local.empty() && fs::exists(local)) {
        if (send_content(chat_id, input_sticker(file_local(local), emoji, width, height),
                         reply_to, topic_id, &message_id, &send_error)) {
            return true;
        }
    }
    if (error) {
        if (send_error.empty()) {
            *error = "sticker file is no longer available";
        } else {
            *error = send_error;
        }
    }
    return false;
}

// ---------------------------------------------------------------------
// syscalls (tg.*)
// ---------------------------------------------------------------------

static std::string user_record_json(const Value& user, const std::string& bio) {
    Value rec = scjson::make_object();
    rec.set("id", v_int(scjson::num(&user, "id", 0)));
    rec.set("first_name", v_str(scjson::str(&user, "first_name")));
    rec.set("last_name", v_str(scjson::str(&user, "last_name")));
    rec.set("username", v_str(user_username(&user)));
    if (!bio.empty()) rec.set("bio", v_str(bio));
    rec.set("phone", v_str(scjson::str(&user, "phone_number")));
    const Value* type = user.get("type");
    std::string user_type = scjson::str(type, "@type");
    rec.set("bot", v_bool(user_type == "userTypeBot"));
    rec.set("deleted", v_bool(user_type == "userTypeDeleted"));
    return scjson::dump(rec);
}

static bool get_user_record(long long user_id, std::string* out_json, std::string* error) {
    Value req = v_obj("getUser");
    req.set("user_id", v_int(user_id));
    Value user;
    if (!td_request(req, user)) {
        if (error) *error = "getUser failed";
        return false;
    }
    std::string bio;
    Value full_req = v_obj("getUserFullInfo");
    full_req.set("user_id", v_int(user_id));
    Value full;
    if (td_request(full_req, full)) {
        bio = scjson::str(&full, "bio");
    }
    *out_json = user_record_json(user, bio);
    return true;
}

static bool resolve_username(const std::string& raw, long long* chat_id, long long* user_id,
                             std::string* title, std::string* error) {
    std::string username = raw;
    if (!username.empty() && username[0] == '@') username.erase(username.begin());
    Value req = v_obj("searchPublicChat");
    req.set("username", v_str(username));
    Value chat;
    if (!td_request(req, chat)) {
        if (error) *error = "searchPublicChat failed";
        return false;
    }
    *chat_id = scjson::num(&chat, "id", 0);
    *title = scjson::str(&chat, "title");
    const Value* type = chat.get("type");
    std::string type_name = scjson::str(type, "@type");
    if (type_name == "chatTypePrivate") {
        *user_id = scjson::num(type, "user_id", 0);
    } else {
        *user_id = 0;
    }
    return true;
}

// syscall/health ?????? ????? ? thread-local ??????: ???? ???????????
// ????????? ?????? ? ???????? ? SC_PLUGIN_FLAG_OWNS_TOOL_RESULT, ? ????
// ???? ??? ?? ?????. ???????????? ??????, ????????, malloc-??????.
static char* syscall_string(const std::string& s) {
    static SC_THREAD_LOCAL char buf[65536];
    std::snprintf(buf, sizeof(buf), "%s", s.c_str());
    return buf;
}

static sc_status_t syscall_handler(const char* method, const char* args_json, char** out) {
    if (!method) return SC_ERR_INVALID_ARG;
    if (out) *out = nullptr;
    std::string m(method);
    Value args;
    if (args_json) scjson::parse(args_json, args);

    if (m == "tg.is_connected" || m == "tg.status") {
        Value v = scjson::make_object();
        bool connected = g_ready.load() && g_net_ready.load();
        v.set("connected", v_bool(connected));
        v.set("username", v_str(g_self_username));
        v.set("user_id", v_int(g_self_id.load()));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.set_proxy") {
        std::string p = scjson::str(&args, "proxy");
        if (!p.empty()) {
            g_proxy = p;
            apply_proxy();
        }
        if (out) *out = syscall_string("{\"status\":\"ok\"}");
        return SC_OK;
    }
    if (m == "transport.start_qr" || m == "tg.start_qr" || m == "tg.start") {
        if (g_ready.load()) {
            emit_auth_event(
                "{\"type\":\"telegram_auth_state\",\"data\":{\"state\":\"connected\"}}");
        } else if (g_loaded.load()) {
            std::thread(qr_auth_worker).detach();
        }
        if (out) *out = syscall_string("{\"status\":\"ok\"}");
        return SC_OK;
    }
    if (m == "tg.stop") {
        stop_client();
        if (out) *out = syscall_string("{\"status\":\"ok\"}");
        return SC_OK;
    }
    if (m == "tg.press_button") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long requested_id = scjson::num(&args, "message_id", 0);
        long long message_id = resolve_message_id(requested_id);
        std::string want = scjson::str(&args, "button");
        std::string data = scjson::str(&args, "data");
        // Buttons may not be synced yet right after a message is sent (or
        // when the id is still a temporary one), so wait for them to appear
        // instead of failing on the first attempt.
        int wait_ms = static_cast<int>(scjson::num(&args, "wait_ms", 10000));
        if (wait_ms < 0) wait_ms = 0;
        if (wait_ms > 60000) wait_ms = 60000;
        Value v = scjson::make_object();
        if (chat_id == 0 || message_id == 0) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("chat_id and message_id required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }

        // Resolve temp -> permanent message id first (send may still be in
        // flight), then poll for the inline keyboard.
        long long real_id = wait_for_real_message_id(message_id, 2000);
        if (real_id != 0) message_id = real_id;

        auto started = std::chrono::steady_clock::now();
        std::vector<button_info> buttons;
        Value mresp;
        bool found = false;
        std::string last_reason = "message not found";
        for (;;) {
            message_id = resolve_message_id(message_id);
            Value mreq = v_obj("getMessage");
            mreq.set("chat_id", v_int(chat_id));
            mreq.set("message_id", v_int(message_id));
            mresp = Value{};
            if (td_request(mreq, mresp, 5000)) {
                buttons = message_buttons(&mresp);
                if (!buttons.empty()) {
                    found = true;
                    break;
                }
                last_reason = "message has no inline keyboard";
            } else {
                last_reason = "message not found";
            }
            auto elapsed = std::chrono::duration_cast<std::chrono::milliseconds>(
                               std::chrono::steady_clock::now() - started)
                               .count();
            if (elapsed >= wait_ms) break;
            std::this_thread::sleep_for(std::chrono::milliseconds(500));
        }
        if (!found) {
            v.set("status", v_str("error"));
            v.set("reason", v_str(last_reason + " (waited " +
                                  std::to_string(wait_ms) + " ms)"));
            v.set("message_id", v_int(message_id));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        const button_info* chosen = nullptr;
        if (!data.empty()) {
            for (const auto& b : buttons) {
                if (b.data == data) {
                    chosen = &b;
                    break;
                }
            }
        }
        bool numeric = !want.empty();
        for (char ch : want) {
            if (!std::isdigit(static_cast<unsigned char>(ch))) {
                numeric = false;
                break;
            }
        }
        if (!chosen && numeric) {
            int idx = std::atoi(want.c_str());
            for (const auto& b : buttons) {
                if (b.index == idx) {
                    chosen = &b;
                    break;
                }
            }
        }
        if (!chosen && !want.empty()) {
            std::string needle = lower_copy(want);
            for (const auto& b : buttons) {
                if (lower_copy(b.text) == needle) {
                    chosen = &b;
                    break;
                }
            }
            if (!chosen) {
                for (const auto& b : buttons) {
                    if (lower_copy(b.text).find(needle) != std::string::npos) {
                        chosen = &b;
                        break;
                    }
                }
            }
        }
        if (!chosen && want.empty() && data.empty() && buttons.size() == 1) {
            chosen = &buttons[0];
        }
        if (!chosen) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("button not found"));
            v.set("buttons", buttons_json(buttons));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        v.set("button", v_str(chosen->text));
        v.set("type", v_str(chosen->kind));
        if (chosen->kind == "url" || chosen->kind == "web_app" ||
            chosen->kind == "login_url") {
            v.set("status", v_str("ok"));
            v.set("url", v_str(chosen->url));
            v.set("note", v_str("this button opens a URL; nothing was pressed"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        if (chosen->kind == "copy_text") {
            v.set("status", v_str("ok"));
            v.set("text", v_str(chosen->data_text));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        if (chosen->data.empty()) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("button has no callback data (game or unsupported type)"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value payload = v_obj("callbackQueryPayloadData");
        payload.set("data", v_str(chosen->data));
        Value req = v_obj("getCallbackQueryAnswer");
        req.set("chat_id", v_int(chat_id));
        req.set("message_id", v_int(message_id));
        req.set("payload", payload);
        Value resp;
        bool ok = td_request(req, resp, 30000);
        v.set("status", v_str(ok ? "ok" : "error"));
        if (ok) {
            v.set("answer_text", v_str(scjson::str(&resp, "text")));
            v.set("show_alert", v_bool(scjson::boolean(&resp, "show_alert", false)));
            v.set("url", v_str(scjson::str(&resp, "url")));
        } else {
            v.set("reason", v_str("bot did not answer the callback (it may have timed out)"));
        }
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.vote_poll") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&args, "message_id", 0));
        std::vector<long long> option_ids;
        const Value* arr = args.get("option_ids");
        if (arr && arr->type == scjson::Type::Array) {
            for (const auto& item : arr->array) {
                long long oid = 0;
                if (item.type == scjson::Type::Number) {
                    oid = item.is_integer ? item.integer : static_cast<long long>(item.number);
                } else if (item.type == scjson::Type::String) {
                    try { oid = std::stoll(item.string); } catch (...) { oid = -1; }
                } else {
                    oid = -1;
                }
                if (oid >= 0) option_ids.push_back(oid);
            }
        }
        Value v = scjson::make_object();
        if (chat_id == 0 || message_id == 0 || option_ids.empty()) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("chat_id, message_id and at least one option_id are required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value opts = scjson::make_array();
        for (long long oid : option_ids) opts.push(v_int(oid));
        Value req = v_obj("setPollAnswer");
        req.set("chat_id", v_int(chat_id));
        req.set("message_id", v_int(message_id));
        req.set("option_ids", opts);
        Value resp;
        bool ok = td_request(req, resp, 15000);
        v.set("status", v_str(ok ? "ok" : "error"));
        if (ok) {
            Value ids = scjson::make_array();
            for (long long oid : option_ids) ids.push(v_int(oid));
            v.set("option_ids", ids);
        } else {
            std::string err = scjson::str(&resp, "message");
            v.set("reason", v_str(err.empty() ? "setPollAnswer failed" : err));
        }
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.answer_callback_query") {
        long long callback_id = scjson::num(&args, "callback_query_id", 0);
        std::string text = scjson::str(&args, "text");
        bool alert = scjson::boolean(&args, "alert", false);
        Value req = v_obj("answerCallbackQuery");
        req.set("callback_query_id", v_int(callback_id));
        req.set("text", v_str(text));
        req.set("show_alert", v_bool(alert));
        req.set("url", v_str(""));
        req.set("cache_time", v_int(0));
        Value resp;
        bool ok = callback_id != 0 && td_request(req, resp);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        if (!ok) v.set("reason", v_str("answerCallbackQuery failed"));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.get_callback_answer") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&args, "message_id", 0));
        std::string data = scjson::str(&args, "data");
        Value payload = v_obj("callbackQueryPayloadData");
        payload.set("data", v_str(data));
        Value req = v_obj("getCallbackQueryAnswer");
        req.set("chat_id", v_int(chat_id));
        req.set("message_id", v_int(message_id));
        req.set("payload", payload);
        Value resp;
        bool ok = chat_id != 0 && message_id != 0 && !data.empty() && td_request(req, resp);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        if (ok) {
            v.set("text", v_str(scjson::str(&resp, "text")));
            v.set("show_alert", v_bool(scjson::boolean(&resp, "show_alert", false)));
            v.set("url", v_str(scjson::str(&resp, "url")));
        } else {
            v.set("reason", v_str("callback answer failed (button may be gone or the bot timed out)"));
        }
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.inline_query") {
        std::string bot = scjson::str(&args, "bot");
        std::string query = scjson::str(&args, "query");
        std::string offset = scjson::str(&args, "offset");
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long bot_user_id = 0;
        std::string bot_username;
        std::string error;
        Value v = scjson::make_object();
        if (!resolve_bot_user(bot, &bot_user_id, &bot_username, &error)) {
            v.set("status", v_str("error"));
            v.set("reason", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        if (chat_id == 0) {
            // Results must be attributed to a real chat; open a private chat
            // with the bot and use it as the context.
            Value creq = v_obj("createPrivateChat");
            creq.set("user_id", v_int(bot_user_id));
            creq.set("force", v_bool(false));
            Value chat;
            if (td_request(creq, chat)) {
                chat_id = scjson::num(&chat, "id", 0);
            }
        }
        if (chat_id == 0) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("chat_id is required to query an inline bot"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value req = v_obj("getInlineQueryResults");
        req.set("bot_user_id", v_int(bot_user_id));
        req.set("chat_id", v_int(chat_id));
        req.set("query", v_str(query));
        req.set("offset", v_str(offset));
        req.set("location", Value{});
        Value resp;
        if (!td_request(req, resp, 30000)) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("inline bot did not answer the query"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        v.set("status", v_str("ok"));
        v.set("bot", v_str("@" + bot_username));
        v.set("query_id", v_int(scjson::num(&resp, "inline_query_id", 0)));
        v.set("next_offset", v_str(scjson::str(&resp, "next_offset")));
        Value items = scjson::make_array();
        const Value* results = resp.get("results");
        if (results && results->type == scjson::Type::Array) {
            int index = 1;
            for (const auto& r : results->array) {
                Value item = scjson::make_object();
                item.set("index", v_int(index++));
                item.set("id", v_str(scjson::str(&r, "id")));
                const Value* rtype = r.get("type");
                item.set("type", v_str(scjson::str(rtype, "@type")));
                item.set("title", v_str(scjson::str(&r, "title")));
                item.set("description", v_str(scjson::str(&r, "description")));
                std::string preview;
                const Value* content = r.get("input_message_content");
                if (content) {
                    std::string ctype = scjson::str(content, "@type");
                    if (ctype == "inputMessageText") {
                        preview = scjson::str(content->get("text"), "text");
                    } else if (ctype == "inputMessagePhoto") {
                        preview = scjson::str(content->get("caption"), "text");
                    } else if (ctype == "inputMessageVideo") {
                        preview = scjson::str(content->get("caption"), "text");
                    } else if (ctype == "inputMessageAnimation") {
                        preview = scjson::str(content->get("caption"), "text");
                    } else if (ctype == "inputMessageDocument") {
                        preview = scjson::str(content->get("caption"), "text");
                    }
                }
                if (preview.empty()) preview = scjson::str(&r, "caption");
                if (!preview.empty()) item.set("text", v_str(preview));
                items.push(item);
            }
        }
        v.set("results", items);
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.send_inline_result") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long query_id = scjson::num(&args, "query_id", 0);
        std::string result_id = scjson::str(&args, "result_id");
        long long reply = scjson::num(&args, "reply_to_id", 0);
        int topic_id = static_cast<int>(scjson::num(&args, "topic_id", 0));
        bool hide_via = scjson::boolean(&args, "hide_via", false);
        Value req = v_obj("sendInlineQueryResultMessage");
        req.set("chat_id", v_int(chat_id));
        req.set("topic_id", topic_id > 0 ? topic_forum(topic_id) : Value{});
        req.set("reply_to", reply > 0 ? reply_to_message(reply) : Value{});
        req.set("options", send_options());
        req.set("query_id", v_int(query_id));
        req.set("result_id", v_str(result_id));
        req.set("hide_via", v_bool(hide_via));
        Value resp;
        bool ok = chat_id != 0 && query_id != 0 && !result_id.empty() && td_request(req, resp, 30000);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        if (ok) {
            long long id = scjson::num(&resp, "id", 0);
            remember_own(chat_id, id);
            v.set("chat_id", v_int(chat_id));
            v.set("message_id", v_int(id));
        } else {
            v.set("reason", v_str("sendInlineQueryResultMessage failed"));
        }
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.check_invite") {
        std::string raw = scjson::str(&args, "link");
        std::string hash = invite_hash(raw);
        Value v = scjson::make_object();
        if (hash.empty()) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("not an invite link"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value req = v_obj("checkChatInviteLink");
        req.set("invite_link", v_str("https://t.me/+" + hash));
        Value resp;
        if (!td_request(req, resp, 30000)) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("invite link is invalid or expired"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        const Value* type = resp.get("type");
        v.set("status", v_str("ok"));
        v.set("chat_id", v_int(scjson::num(&resp, "chat_id", 0)));
        v.set("type", v_str(scjson::str(type, "@type")));
        v.set("title", v_str(scjson::str(&resp, "title")));
        v.set("description", v_str(scjson::str(&resp, "description")));
        v.set("member_count", v_int(scjson::num(&resp, "member_count", 0)));
        v.set("is_public_chat", v_bool(scjson::boolean(&resp, "is_public_chat", false)));
        v.set("accessible_for", v_int(scjson::num(&resp, "accessible_for", 0)));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.join_chat") {
        std::string target = scjson::str(&args, "link");
        if (target.empty()) target = scjson::str(&args, "username");
        Value resp;
        std::string error;
        Value v = scjson::make_object();
        if (!join_chat_target(target, resp, &error)) {
            v.set("status", v_str("error"));
            v.set("reason", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        v.set("status", v_str("ok"));
        v.set("chat_id", v_int(scjson::num(&resp, "id", 0)));
        v.set("title", v_str(scjson::str(&resp, "title")));
        const Value* type = resp.get("type");
        v.set("type", v_str(scjson::str(type, "@type")));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.leave_chat") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        Value req = v_obj("leaveChat");
        req.set("chat_id", v_int(chat_id));
        Value resp;
        bool ok = chat_id != 0 && td_request(req, resp, 30000);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        if (!ok) v.set("reason", v_str("leaveChat failed"));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.send_message" || m == "tg.send_message_get_id") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        int topic_id = static_cast<int>(scjson::num(&args, "topic_id", 0));
        long long reply = scjson::num(&args, "reply_to_id", 0);
        std::string text = scjson::str(&args, "text");
        long long id = 0;
        std::string error;
        if (!send_text(chat_id, text, reply, topic_id, &id, &error)) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value v = scjson::make_object();
        v.set("status", v_str("ok"));
        v.set("chat_id", v_int(chat_id));
        v.set("message_id", v_int(id));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.edit_message") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&args, "message_id", 0));
        std::string text = scjson::str(&args, "text");
        Value req = v_obj("editMessageText");
        req.set("chat_id", v_int(chat_id));
        req.set("message_id", v_int(message_id));
        req.set("reply_markup", Value{});
        req.set("input_message_content", text_content(text));
        Value resp;
        bool ok = td_request(req, resp);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        if (!ok) v.set("reason", v_str("editMessageText failed"));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.delete_message") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&args, "message_id", 0));
        Value ids = scjson::make_array();
        ids.push(v_int(message_id));
        Value req = v_obj("deleteMessages");
        req.set("chat_id", v_int(chat_id));
        req.set("message_ids", ids);
        req.set("revoke", v_bool(true));
        Value resp;
        bool ok = td_request(req, resp);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        if (!ok) v.set("reason", v_str("deleteMessages failed"));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.chat_action") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        int topic_id = static_cast<int>(scjson::num(&args, "topic_id", 0));
        std::string action = scjson::str(&args, "action");
        Value req = v_obj("sendChatAction");
        req.set("chat_id", v_int(chat_id));
        if (topic_id > 0) req.set("topic_id", topic_forum(topic_id));
        req.set("business_connection_id", v_str(""));
        if (action == "cancel") {
            req.set("action", v_obj("chatActionCancel"));
        } else if (action == "choosing_sticker") {
            req.set("action", v_obj("chatActionChoosingSticker"));
        } else if (action == "upload_photo" || action == "uploading_photo") {
            req.set("action", v_obj("chatActionUploadingPhoto"));
        } else if (action == "recording_voice") {
            req.set("action", v_obj("chatActionRecordingVoiceNote"));
        } else {
            req.set("action", v_obj("chatActionTyping"));
        }
        g_td.post(req);
        if (out) *out = syscall_string("{\"status\":\"ok\"}");
        return SC_OK;
    }
    if (m == "tg.mark_read") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long message_id = scjson::num(&args, "message_id", 0);
        Value ids = scjson::make_array();
        ids.push(v_int(message_id));
        Value req = v_obj("viewMessages");
        req.set("chat_id", v_int(chat_id));
        req.set("message_ids", ids);
        req.set("source", v_obj("messageSourceChatHistory"));
        req.set("force_read", v_bool(false));
        Value resp;
        g_td.post(req);
        if (out) *out = syscall_string("{\"status\":\"ok\"}");
        return SC_OK;
    }
    if (m == "tg.block_user" || m == "tg.block") {
        long long user_id = scjson::num(&args, "user_id", 0);
        if (user_id == 0) {
            long long chat_id = scjson::num(&args, "chat_id", 0);
            if (chat_id > 0) user_id = chat_id;
        }
        std::string username = scjson::str(&args, "username");
        if (user_id == 0 && !username.empty()) {
            if (username[0] == '@') username = username.substr(1);
            Value req = v_obj("searchPublicChat");
            req.set("username", v_str(username));
            Value resp;
            if (td_request(req, resp)) {
                long long cid = scjson::num(&resp, "id", 0);
                if (cid > 0) user_id = cid;
            }
        }
        if (user_id == 0) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str("user_id, chat_id or username required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value sender = v_obj("messageSenderUser");
        sender.set("user_id", v_int(user_id));
        Value req = v_obj("toggleMessageSenderIsBlocked");
        req.set("sender_id", sender);
        req.set("is_blocked", v_bool(true));
        Value resp;
        bool ok = td_request(req, resp);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        v.set("user_id", v_int(user_id));
        if (!ok) v.set("reason", v_str(scjson::str(&resp, "message")));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.unblock_user" || m == "tg.unblock") {
        long long user_id = scjson::num(&args, "user_id", 0);
        if (user_id == 0) {
            long long chat_id = scjson::num(&args, "chat_id", 0);
            if (chat_id > 0) user_id = chat_id;
        }
        std::string username = scjson::str(&args, "username");
        if (user_id == 0 && !username.empty()) {
            if (username[0] == '@') username = username.substr(1);
            Value req = v_obj("searchPublicChat");
            req.set("username", v_str(username));
            Value resp;
            if (td_request(req, resp)) {
                long long cid = scjson::num(&resp, "id", 0);
                if (cid > 0) user_id = cid;
            }
        }
        if (user_id == 0) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str("user_id, chat_id or username required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value sender = v_obj("messageSenderUser");
        sender.set("user_id", v_int(user_id));
        Value req = v_obj("toggleMessageSenderIsBlocked");
        req.set("sender_id", sender);
        req.set("is_blocked", v_bool(false));
        Value resp;
        bool ok = td_request(req, resp);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        v.set("user_id", v_int(user_id));
        if (!ok) v.set("reason", v_str(scjson::str(&resp, "message")));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.delete_chat" || m == "tg.delete_chat_history") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        if (chat_id == 0) {
            long long user_id = scjson::num(&args, "user_id", 0);
            if (user_id > 0) chat_id = user_id;
        }
        std::string username = scjson::str(&args, "username");
        if (chat_id == 0 && !username.empty()) {
            if (username[0] == '@') username = username.substr(1);
            Value req = v_obj("searchPublicChat");
            req.set("username", v_str(username));
            Value resp;
            if (td_request(req, resp)) {
                chat_id = scjson::num(&resp, "id", 0);
            }
        }
        if (chat_id == 0) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str("chat_id or username required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value req = v_obj("deleteChatHistory");
        req.set("chat_id", v_int(chat_id));
        req.set("remove_from_chat_list", v_bool(true));
        req.set("revoke", v_bool(true));
        Value resp;
        bool ok = td_request(req, resp);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        v.set("chat_id", v_int(chat_id));
        if (!ok) v.set("reason", v_str(scjson::str(&resp, "message")));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.set_reaction") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&args, "message_id", 0));
        std::string emoji = scjson::str(&args, "emoji");
        std::string custom = scjson::str(&args, "custom_emoji_id");
        if (!custom.empty()) {
            emoji = "custom:" + custom;
        }
        Value v = scjson::make_object();
        if (chat_id == 0 || message_id == 0 || emoji.empty()) {
            v.set("status", v_str("error"));
            v.set("reason", v_str("chat_id, message_id and emoji required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value reaction = make_reaction(emoji);
        Value req = v_obj("addMessageReaction");
        req.set("chat_id", v_int(chat_id));
        req.set("message_id", v_int(message_id));
        req.set("reaction_type", reaction);
        req.set("is_big", v_bool(false));
        req.set("update_recent_reactions", v_bool(true));
        Value resp;
        bool ok = td_request(req, resp);
        v.set("status", v_str(ok ? "ok" : "error"));
        if (ok) {
            v.set("emoji", v_str(emoji));
            if (scjson::str(&reaction, "@type") == "reactionTypeCustomEmoji") {
                v.set("custom_emoji_id", v_int(scjson::num(&reaction, "custom_emoji_id", 0)));
            }
        } else {
            v.set("reason", v_str(scjson::str(&resp, "message")));
        }
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.list_stickers") {
        std::vector<Value> items;
        Value list = scjson::make_array();
        if (load_stickers(items)) {
            for (const auto& item : items) {
                Value one = scjson::make_object();
                one.set("id", v_str(scjson::str(&item, "id")));
                one.set("description", v_str(scjson::str(&item, "description")));
                one.set("emoji", v_str(scjson::str(&item, "emoji")));
                list.push(one);
            }
        }
        if (out) *out = syscall_string(scjson::dump(list));
        return SC_OK;
    }
    if (m == "tg.send_sticker") {
        long long chat_id = scjson::num(&args, "chat_id", 0);
        long long reply = scjson::num(&args, "reply_to_id", 0);
        int topic_id = static_cast<int>(scjson::num(&args, "topic_id", 0));
        std::string sticker_id = scjson::str(&args, "sticker_id");
        std::string error;
        if (!send_sticker(chat_id, sticker_id, reply, topic_id, &error)) {
            Value v = scjson::make_object();
            v.set("error", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        if (out) *out = syscall_string("{\"status\":\"ok\"}");
        return SC_OK;
    }
    if (m == "tg.get_user") {
        long long user_id = scjson::num(&args, "user_id", 0);
        std::string json;
        std::string error;
        if (!get_user_record(user_id, &json, &error)) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        if (out) *out = syscall_string(json);
        return SC_OK;
    }
    if (m == "tg.resolve_username") {
        std::string username = scjson::str(&args, "username");
        long long chat_id = 0;
        long long user_id = 0;
        std::string title;
        std::string error;
        if (!resolve_username(username, &chat_id, &user_id, &title, &error)) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        std::string json;
        if (user_id > 0) {
            get_user_record(user_id, &json, &error);
        }
        if (json.empty()) {
            Value rec = scjson::make_object();
            rec.set("id", v_int(chat_id));
            rec.set("first_name", v_str(title));
            rec.set("last_name", v_str(""));
            rec.set("username", v_str(username[0] == '@' ? username.substr(1) : username));
            rec.set("bot", v_bool(false));
            rec.set("deleted", v_bool(false));
            json = scjson::dump(rec);
        }
        if (out) *out = syscall_string(json);
        return SC_OK;
    }
    if (m == "tg.send_to_username") {
        std::string username = scjson::str(&args, "username");
        std::string text = scjson::str(&args, "text");
        long long chat_id = 0;
        long long user_id = 0;
        std::string title;
        std::string error;
        if (!resolve_username(username, &chat_id, &user_id, &title, &error)) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        long long id = 0;
        if (!send_text(chat_id, text, 0, 0, &id, &error)) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str(error));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value v = scjson::make_object();
        v.set("status", v_str("ok"));
        v.set("chat_id", v_int(chat_id));
        v.set("message_id", v_int(id));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.stats") {
        long long count = 0;
        {
            std::lock_guard<std::mutex> lock(g_cache_mu);
            count = static_cast<long long>(g_own_messages.size());
        }
        Value v = scjson::make_object();
        v.set("own_messages", v_int(count));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.prune_own") {
        long long cutoff = scjson::num(&args, "cutoff", 0);
        long long removed = 0;
        {
            std::lock_guard<std::mutex> lock(g_cache_mu);
            for (auto it = g_own_messages.begin(); it != g_own_messages.end();) {
                if (cutoff > 0 && it->second < cutoff) {
                    it = g_own_messages.erase(it);
                    removed++;
                } else {
                    ++it;
                }
            }
        }
        Value v = scjson::make_object();
        v.set("removed", v_int(removed));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.send_photo") {
        long long chat_id = 0;
        std::string chat_str = scjson::str(&args, "chat_id");
        if (!chat_str.empty()) chat_id = std::strtoll(chat_str.c_str(), nullptr, 10);
        else chat_id = scjson::num(&args, "chat_id", 0);
        long long reply = scjson::num(&args, "reply_to", 0);
        int topic_id = static_cast<int>(scjson::num(&args, "topic_id", 0));
        std::string path = scjson::str(&args, "path");
        std::string caption = scjson::str(&args, "caption");
        if (chat_id == 0 || path.empty()) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str("chat_id and path required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }
        Value file = make_input_file(path);
        Value content = input_photo(file, caption);
        long long message_id = 0;
        std::string error;
        bool ok = send_content(chat_id, content, reply, topic_id, &message_id, &error);
        Value v = scjson::make_object();
        v.set("status", v_str(ok ? "ok" : "error"));
        if (ok) v.set("message_id", v_int(message_id));
        else v.set("reason", v_str(error));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.download_custom_emoji") {
        long long custom_emoji_id = 0;
        std::string id_str = scjson::str(&args, "id");
        if (!id_str.empty()) {
            try { custom_emoji_id = std::stoll(id_str); } catch (...) {}
        }
        if (custom_emoji_id == 0) {
            custom_emoji_id = scjson::num(&args, "custom_emoji_id", 0);
        }
        if (custom_emoji_id == 0) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str("custom_emoji_id required"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }

        std::string fallback;
        std::string pack;
        std::string path = download_custom_emoji(custom_emoji_id, &fallback, &pack);
        if (path.empty()) {
            Value v = scjson::make_object();
            v.set("status", v_str("error"));
            v.set("reason", v_str("could not download custom emoji"));
            if (out) *out = syscall_string(scjson::dump(v));
            return SC_OK;
        }

        Value v = scjson::make_object();
        v.set("status", v_str("ok"));
        v.set("id", v_str(std::to_string(custom_emoji_id)));
        v.set("custom_emoji_id", v_int(custom_emoji_id));
        v.set("emoji", v_str(fallback));
        v.set("pack", v_str(pack));
        v.set("file", v_str(path));
        if (out) *out = syscall_string(scjson::dump(v));
        return SC_OK;
    }
    if (m == "tg.list_custom_emojis") {
        std::string q = scjson::str(&args, "query");
        Value list = scjson::make_array();
        {
            std::lock_guard<std::mutex> lk(g_custom_emojis_mu);
            int count = 0;
            for (const auto& kv : g_custom_emojis) {
                if (count >= 200) break;
                if (!q.empty()) {
                    if (kv.second.pack.find(q) == std::string::npos &&
                        kv.second.fallback.find(q) == std::string::npos &&
                        std::to_string(kv.first).find(q) == std::string::npos) {
                        continue;
                    }
                }
                Value item = scjson::make_object();
                item.set("id", v_str(std::to_string(kv.second.id)));
                item.set("fallback", v_str(kv.second.fallback));
                item.set("pack", v_str(kv.second.pack));
                list.push(item);
                count++;
            }
        }
        if (out) *out = syscall_string(scjson::dump(list));
        return SC_OK;
    }
    return SC_ERR_NOT_FOUND;
}

static const sc_syscall_reg_t g_syscalls[] = {
    {"tg.", syscall_handler},
    {"transport.", syscall_handler},
};

// ---------------------------------------------------------------------
// tools
// ---------------------------------------------------------------------

static const char* tools_json =
    "["
    "{\"name\":\"send_message\","
    "\"description\":\"Send a Telegram message to a chat.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"text\":{\"type\":\"string\"},"
    "\"reply_to_id\":{\"type\":\"integer\",\"description\":\"Optional message ID to reply to\"},"
    "\"topic_id\":{\"type\":\"integer\",\"description\":\"Optional forum topic ID\"}"
    "},\"required\":[\"chat_id\",\"text\"]}},"
    "{\"name\":\"send_photo\","
    "\"description\":\"Send a photo (or GIF as photo) to a Telegram chat. Supports local paths, remote image URLs, or Telegram file IDs with optional caption.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"path\":{\"type\":\"string\",\"description\":\"File path, image URL or file ID\"},"
    "\"caption\":{\"type\":\"string\",\"description\":\"Optional caption for the photo\"},"
    "\"reply_to_id\":{\"type\":\"integer\"},"
    "\"topic_id\":{\"type\":\"integer\"}"
    "},\"required\":[\"chat_id\",\"path\"]}},"
    "{\"name\":\"react\","
    "\"description\":\"Set an emoji reaction on a Telegram message. Supports standard unicode emoji (e.g. '🔥', '👍') as well as Telegram Premium custom emoji: pass 'custom:<numeric id>' (e.g. custom:5368324170671202286) or 'id:<numeric id>'. Use INSTEAD of send_message for short emotional responses (emoji, 'ok', 'yes', 'haha'). NEVER send text that is only emoji - react instead.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"message_id\":{\"type\":\"integer\"},"
    "\"emoji\":{\"type\":\"string\",\"description\":\"Unicode emoji or 'custom:<id>' for a Premium custom emoji\"}"
    "},\"required\":[\"chat_id\",\"message_id\",\"emoji\"]}},"
    "{\"name\":\"list_custom_emojis\","
    "\"description\":\"List catalogued custom emojis with IDs, packs, and vision descriptions from emoji.json. Use this to find the right custom emoji ID for reactions or inline messages.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"query\":{\"type\":\"string\",\"description\":\"Optional search term for pack name or fallback emoji\"}"
    "},\"required\":[]}},"
    "{\"name\":\"send_sticker\","
    "\"description\":\"Send a Telegram sticker to a chat. Stickers are chosen only from data/stickers.json by its internal sticker_id assigned after vision analysis. Use only catalogued stickers.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"sticker_id\":{\"type\":\"string\",\"description\":\"Internal ID from stickers.json assigned after vision analysis\"},"
    "\"reply_to_id\":{\"type\":\"integer\"},\"topic_id\":{\"type\":\"integer\"}"
    "},\"required\":[\"chat_id\",\"sticker_id\"]}},"
    "{\"name\":\"list_stickers\","
    "\"description\":\"List catalogued stickers with IDs and descriptions from data/stickers.json.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}},"
    "{\"name\":\"send_to_username\","
    "\"description\":\"Send a Telegram message to a user by their @username. "
    "Use this when you want to write to someone whose chat_id you don't know "
    "but whose username you do. Resolves the username and sends in one step.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"username\":{\"type\":\"string\",\"description\":\"Username with or without @\"},"
    "\"text\":{\"type\":\"string\",\"description\":\"Message text\"}"
    "},\"required\":[\"username\",\"text\"]}},"
    "{\"name\":\"mark_read\","
    "\"description\":\"Mark a message as read in a chat.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"message_id\":{\"type\":\"integer\"}"
    "},\"required\":[\"chat_id\",\"message_id\"]}},"
    "{\"name\":\"edit_message\","
    "\"description\":\"Edit a previously sent message in a Telegram chat. Use this to correct a mistake, update information, or fix a typo.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"message_id\":{\"type\":\"integer\"},"
    "\"text\":{\"type\":\"string\"}"
    "},\"required\":[\"chat_id\",\"message_id\",\"text\"]}},"
    "{\"name\":\"delete_message\","
    "\"description\":\"Delete a message in a Telegram chat for everyone. Use this if asked to delete a message or to remove a mistake.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"message_id\":{\"type\":\"integer\"}"
    "},\"required\":[\"chat_id\",\"message_id\"]}},"
    "{\"name\":\"chat_action\","
    "\"description\":\"Send a chat status action (e.g. typing, choosing_sticker) to indicate what you are doing in the chat.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"action\":{\"type\":\"string\",\"description\":\"Action type: 'typing', 'choosing_sticker', 'upload_photo'\"}"
    "},\"required\":[\"chat_id\",\"action\"]}},"
    "{\"name\":\"press_button\","
    "\"description\":\"Press an inline keyboard button on a message, including messages sent by other bots (inline bot results). The button may not be attached yet right after sending (inline bot results arrive asynchronously), so the tool waits and retries until the keyboard appears. Use the button text, its 1-based index, or raw callback data. If you are not sure the keyboard is ready yet, just call it - it will wait up to wait_ms milliseconds.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"message_id\":{\"type\":\"integer\"},"
    "\"button\":{\"type\":\"string\",\"description\":\"Button text (or its substring) or 1-based index\"},"
    "\"data\":{\"type\":\"string\",\"description\":\"Raw base64 callback data from the buttons list; takes precedence\"},"
    "\"wait_ms\":{\"type\":\"integer\",\"description\":\"How long to wait for the keyboard to appear, default 10000, max 60000\"}"
    "},\"required\":[\"chat_id\",\"message_id\"]}},"
    "{\"name\":\"vote_poll\","
    "\"description\":\"Vote in a Telegram poll or quiz. The current message context lists the poll options with 0-based ids. Pass option_ids=[0] for a single-choice poll/quiz, or several ids for a poll that allows multiple answers. Do not vote on closed polls.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"message_id\":{\"type\":\"integer\"},"
    "\"option_ids\":{\"type\":\"array\",\"items\":{\"type\":\"integer\"},\"description\":\"0-based option ids shown in the poll context\"}"
    "},\"required\":[\"chat_id\",\"message_id\",\"option_ids\"]}},"
    "{\"name\":\"answer_callback\","
    "\"description\":\"Answer a callback query after a user pressed one of your inline buttons. Shows a toast or an alert and stops the loading spinner.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"callback_query_id\":{\"type\":\"integer\"},"
    "\"text\":{\"type\":\"string\"},"
    "\"alert\":{\"type\":\"boolean\",\"description\":\"Show a modal alert instead of a toast\"}"
    "},\"required\":[\"callback_query_id\"]}},"
    "{\"name\":\"inline_query\","
    "\"description\":\"Query an inline bot by username (e.g. @gif, @pic, @youtube) and get its result list. Returns a query_id and result ids for send_inline_result.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"bot\":{\"type\":\"string\",\"description\":\"Bot username, with or without @\"},"
    "\"query\":{\"type\":\"string\"},"
    "\"chat_id\":{\"type\":\"integer\",\"description\":\"Target chat context for the results\"},"
    "\"offset\":{\"type\":\"string\"}"
    "},\"required\":[\"bot\",\"query\"]}},"
    "{\"name\":\"send_inline_result\","
    "\"description\":\"Send one result returned by inline_query into a chat.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"},"
    "\"query_id\":{\"type\":\"integer\"},"
    "\"result_id\":{\"type\":\"string\"},"
    "\"reply_to_id\":{\"type\":\"integer\"},"
    "\"topic_id\":{\"type\":\"integer\"},"
    "\"hide_via\":{\"type\":\"boolean\"}"
    "},\"required\":[\"chat_id\",\"query_id\",\"result_id\"]}},"
    "{\"name\":\"check_invite\","
    "\"description\":\"Inspect a Telegram group/channel invite link (t.me/+..., t.me/joinchat/...) before deciding whether to join. Returns title, member count and whether it is public.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"link\":{\"type\":\"string\"}"
    "},\"required\":[\"link\"]}},"
    "{\"name\":\"join_chat\","
    "\"description\":\"Join a group or channel by invite link or public @username/link. Only join when it makes sense or the user asked you to.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"link\":{\"type\":\"string\",\"description\":\"Invite link, public t.me link or @username\"}"
    "},\"required\":[\"link\"]}},"
    "{\"name\":\"leave_chat\","
    "\"description\":\"Leave a group or channel by chat_id.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{"
    "\"chat_id\":{\"type\":\"integer\"}"
    "},\"required\":[\"chat_id\"]}},"
    "{\"name\":\"telegram_status\","
    "\"description\":\"Check if the Telegram client is connected.\","
    "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}}"
    "]";

static SC_THREAD_LOCAL char g_result[65536];

static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    *out = const_cast<char*>(tools_json);
    return SC_OK;
}

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;
    Value a;
    if (args) scjson::parse(args, a);

    // Inline, callback and invite tools reuse the tg.* syscall implementations.
    static const struct {
        const char* tool;
        const char* method;
    } kToolSyscalls[] = {
        {"press_button", "tg.press_button"},
        {"vote_poll", "tg.vote_poll"},
        {"answer_callback", "tg.answer_callback_query"},
        {"inline_query", "tg.inline_query"},
        {"send_inline_result", "tg.send_inline_result"},
        {"check_invite", "tg.check_invite"},
        {"join_chat", "tg.join_chat"},
        {"leave_chat", "tg.leave_chat"},
    };
    for (const auto& mapping : kToolSyscalls) {
        if (std::strcmp(name, mapping.tool) == 0) {
            char* tmp = nullptr;
            sc_status_t st = syscall_handler(mapping.method, args ? args : "{}", &tmp);
            if (st != SC_OK || !tmp) {
                std::snprintf(g_result, sizeof(g_result),
                              "{\"status\":\"error\",\"reason\":\"syscall failed\"}");
            } else {
                std::snprintf(g_result, sizeof(g_result), "%s", tmp);
            }
            *out = g_result;
            return SC_OK;
        }
    }

    if (std::strcmp(name, "send_message") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        long long reply = scjson::num(&a, "reply_to_id", 0);
        int topic_id = static_cast<int>(scjson::num(&a, "topic_id", 0));
        std::string text = scjson::str(&a, "text");
        if (chat_id == 0 || text.empty()) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"chat_id and text required\"}");
            *out = g_result;
            return SC_OK;
        }
        long long id = 0;
        std::string error;
        bool ok = send_text(chat_id, text, reply, topic_id, &id, &error);
        std::snprintf(g_result, sizeof(g_result),
                      "{\"status\":\"%s\",\"chat_id\":%lld%s%s%s}", ok ? "ok" : "error",
                      chat_id, ok ? ",\"message_id\":" : ",\"reason\":\"",
                      ok ? std::to_string(id).c_str() : error.c_str(),
                      ok ? "" : "\"");
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "send_photo") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        long long reply = scjson::num(&a, "reply_to_id", 0);
        int topic_id = static_cast<int>(scjson::num(&a, "topic_id", 0));
        std::string path = scjson::str(&a, "path");
        std::string caption = scjson::str(&a, "caption");
        if (chat_id == 0 || path.empty()) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"chat_id and path required\"}");
            *out = g_result;
            return SC_OK;
        }
        Value file = make_input_file(path);
        Value content = input_photo(file, caption);
        long long message_id = 0;
        std::string error;
        bool ok = send_content(chat_id, content, reply, topic_id, &message_id, &error);
        if (ok) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"ok\",\"message_id\":%lld}", message_id);
        } else {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"%s\"}", error.c_str());
        }
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "react") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&a, "message_id", 0));
        std::string emoji = scjson::str(&a, "emoji");
        if (chat_id == 0 || message_id == 0 || emoji.empty()) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"chat_id, message_id, emoji required\"}");
            *out = g_result;
            return SC_OK;
        }

        long long custom_emoji_id = 0;
        if (emoji.rfind("custom:", 0) == 0) {
            try { custom_emoji_id = std::stoll(emoji.substr(7)); } catch (...) {}
        } else if (emoji.rfind("id:", 0) == 0) {
            try { custom_emoji_id = std::stoll(emoji.substr(3)); } catch (...) {}
        } else if (emoji.size() > 6 && std::all_of(emoji.begin(), emoji.end(), ::isdigit)) {
            try { custom_emoji_id = std::stoll(emoji); } catch (...) {}
        }
        (void)custom_emoji_id;
        Value reaction = make_reaction(emoji);

        Value req = v_obj("addMessageReaction");
        req.set("chat_id", v_int(chat_id));
        req.set("message_id", v_int(message_id));
        req.set("reaction_type", reaction);
        req.set("is_big", v_bool(false));
        req.set("update_recent_reactions", v_bool(true));
        Value resp;
        bool ok = td_request(req, resp);
        std::string err_msg = ok ? "" : scjson::str(&resp, "message");
        if (ok) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"ok\",\"emoji\":\"%s\"}", emoji.c_str());
        } else {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"%s\"}",
                          err_msg.empty() ? "set_reaction failed" : err_msg.c_str());
        }
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "list_custom_emojis") == 0) {
        std::string q = scjson::str(&a, "query");
        Value list = scjson::make_array();
        std::unordered_set<std::string> seen;

        std::vector<Value> catalog;
        if (load_emojis(catalog)) {
            for (const auto& item : catalog) {
                std::string id = scjson::str(&item, "id");
                std::string emoji = scjson::str(&item, "emoji");
                std::string pack = scjson::str(&item, "pack");
                std::string desc = scjson::str(&item, "description");
                if (!q.empty()) {
                    if (id.find(q) == std::string::npos &&
                        emoji.find(q) == std::string::npos &&
                        pack.find(q) == std::string::npos &&
                        desc.find(q) == std::string::npos) {
                        continue;
                    }
                }
                seen.insert(id);
                list.push(item);
                if (list.array.size() >= 100) break;
            }
        }

        if (list.array.size() < 100) {
            std::lock_guard<std::mutex> lk(g_custom_emojis_mu);
            for (const auto& kv : g_custom_emojis) {
                if (list.array.size() >= 100) break;
                std::string id_str = std::to_string(kv.first);
                if (seen.find(id_str) != seen.end()) continue;
                if (!q.empty()) {
                    if (kv.second.pack.find(q) == std::string::npos &&
                        kv.second.fallback.find(q) == std::string::npos &&
                        id_str.find(q) == std::string::npos) {
                        continue;
                    }
                }
                Value item = scjson::make_object();
                item.set("id", v_str(id_str));
                item.set("emoji", v_str(kv.second.fallback));
                item.set("pack", v_str(kv.second.pack));
                item.set("description", v_str(""));
                list.push(item);
            }
        }
        std::string json = scjson::dump(list);
        std::snprintf(g_result, sizeof(g_result), "%s", json.c_str());
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "list_stickers") == 0) {
        std::vector<Value> items;
        Value list = scjson::make_array();
        if (load_stickers(items)) {
            for (const auto& item : items) {
                Value one = scjson::make_object();
                one.set("id", v_str(scjson::str(&item, "id")));
                one.set("description", v_str(scjson::str(&item, "description")));
                one.set("emoji", v_str(scjson::str(&item, "emoji")));
                list.push(one);
            }
        }
        std::string json = scjson::dump(list);
        std::snprintf(g_result, sizeof(g_result), "%s", json.c_str());
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "send_sticker") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        long long reply = scjson::num(&a, "reply_to_id", 0);
        int topic_id = static_cast<int>(scjson::num(&a, "topic_id", 0));
        std::string sticker_id = scjson::str(&a, "sticker_id");
        if (chat_id == 0 || sticker_id.empty()) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"chat_id and sticker_id required\"}");
            *out = g_result;
            return SC_OK;
        }
        Value act = v_obj("sendChatAction");
        act.set("chat_id", v_int(chat_id));
        if (topic_id > 0) act.set("topic_id", topic_forum(topic_id));
        act.set("business_connection_id", v_str(""));
        act.set("action", v_obj("chatActionChoosingSticker"));
        g_td.post(act);
        std::string error;
        bool ok = send_sticker(chat_id, sticker_id, reply, topic_id, &error);
        std::snprintf(g_result, sizeof(g_result),
                      ok ? "{\"status\":\"ok\",\"sticker_id\":\"%s\"}"
                         : "{\"status\":\"error\",\"reason\":\"%s\"}",
                      ok ? sticker_id.c_str() : error.c_str());
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "send_to_username") == 0) {
        std::string username = scjson::str(&a, "username");
        std::string text = scjson::str(&a, "text");
        if (username.empty() || text.empty()) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"username and text required\"}");
            *out = g_result;
            return SC_OK;
        }
        long long chat_id = 0;
        long long user_id = 0;
        std::string title;
        std::string error;
        if (!resolve_username(username, &chat_id, &user_id, &title, &error) ||
            !send_text(chat_id, text, 0, 0, nullptr, &error)) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"send failed\"}");
            *out = g_result;
            return SC_OK;
        }
        std::snprintf(g_result, sizeof(g_result),
                      "{\"status\":\"ok\",\"username\":\"%s\"}", username.c_str());
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "mark_read") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        long long message_id = scjson::num(&a, "message_id", 0);
        Value ids = scjson::make_array();
        ids.push(v_int(message_id));
        Value req = v_obj("viewMessages");
        req.set("chat_id", v_int(chat_id));
        req.set("message_ids", ids);
        req.set("source", v_obj("messageSourceChatHistory"));
        req.set("force_read", v_bool(false));
        g_td.post(req);
        std::snprintf(g_result, sizeof(g_result), "{\"status\":\"ok\"}");
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "edit_message") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&a, "message_id", 0));
        std::string text = scjson::str(&a, "text");
        if (chat_id == 0 || message_id == 0 || text.empty()) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"chat_id, message_id and text required\"}");
            *out = g_result;
            return SC_OK;
        }
        Value req = v_obj("editMessageText");
        req.set("chat_id", v_int(chat_id));
        req.set("message_id", v_int(message_id));
        req.set("reply_markup", Value{});
        req.set("input_message_content", text_content(text));
        Value resp;
        bool ok = td_request(req, resp);
        std::snprintf(g_result, sizeof(g_result),
                      ok ? "{\"status\":\"ok\"}" : "{\"status\":\"error\",\"reason\":\"editMessageText failed\"}");
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "delete_message") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        long long message_id = resolve_message_id(scjson::num(&a, "message_id", 0));
        if (chat_id == 0 || message_id == 0) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"chat_id and message_id required\"}");
            *out = g_result;
            return SC_OK;
        }
        Value ids = scjson::make_array();
        ids.push(v_int(message_id));
        Value req = v_obj("deleteMessages");
        req.set("chat_id", v_int(chat_id));
        req.set("message_ids", ids);
        req.set("revoke", v_bool(true));
        Value resp;
        bool ok = td_request(req, resp);
        std::snprintf(g_result, sizeof(g_result),
                      ok ? "{\"status\":\"ok\"}" : "{\"status\":\"error\",\"reason\":\"deleteMessages failed\"}");
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "chat_action") == 0) {
        long long chat_id = scjson::num(&a, "chat_id", 0);
        int topic_id = static_cast<int>(scjson::num(&a, "topic_id", 0));
        std::string action = scjson::str(&a, "action");
        if (chat_id == 0 || action.empty()) {
            std::snprintf(g_result, sizeof(g_result),
                          "{\"status\":\"error\",\"reason\":\"chat_id and action required\"}");
            *out = g_result;
            return SC_OK;
        }
        Value req = v_obj("sendChatAction");
        req.set("chat_id", v_int(chat_id));
        if (topic_id > 0) req.set("topic_id", topic_forum(topic_id));
        req.set("business_connection_id", v_str(""));
        if (action == "cancel") {
            req.set("action", v_obj("chatActionCancel"));
        } else if (action == "choosing_sticker") {
            req.set("action", v_obj("chatActionChoosingSticker"));
        } else if (action == "upload_photo" || action == "uploading_photo") {
            req.set("action", v_obj("chatActionUploadingPhoto"));
        } else if (action == "recording_voice") {
            req.set("action", v_obj("chatActionRecordingVoiceNote"));
        } else {
            req.set("action", v_obj("chatActionTyping"));
        }
        g_td.post(req);
        std::snprintf(g_result, sizeof(g_result), "{\"status\":\"ok\"}");
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "telegram_status") == 0) {
        bool connected = g_ready.load() && g_net_ready.load();
        std::snprintf(g_result, sizeof(g_result), "{\"connected\":%s}",
                      connected ? "true" : "false");
        *out = g_result;
        return SC_OK;
    }
    if (std::strcmp(name, "on_telegram_event") == 0) {
        if (g_core && g_core->emit_event && args) g_core->emit_event(args);
        std::snprintf(g_result, sizeof(g_result), "{\"status\":\"ok\"}");
        *out = g_result;
        return SC_OK;
    }
    return SC_ERR_NOT_FOUND;
}

// ---------------------------------------------------------------------
// plugin lifecycle
// ---------------------------------------------------------------------

static const sc_plugin_info_t* get_info() {
    static const sc_plugin_info_t info = {
        "telegram",
        "2.0.0",
        "Telegram userbot transport on TDLib (dynamically loaded)",
        nullptr,
        SC_ABI_MAJOR,
        SC_ABI_MINOR,
        SC_PLUGIN_FLAG_THREAD_SAFE | SC_PLUGIN_FLAG_TRANSPORT |
            SC_PLUGIN_FLAG_REQUIRES_NETWORK | SC_PLUGIN_FLAG_NO_HOT_RELOAD,
        0,
        nullptr,
        0,
        5000,
        nullptr,
        nullptr,
        "telegram",
        nullptr,
        SC_ABI_PATCH,
        {0, 0, 0},
    };
    return &info;
}

static void read_config(const char* cfg) {
    if (!cfg || !cfg[0]) return;
    Value v;
    if (!scjson::parse(cfg, v)) return;
    int api_id = static_cast<int>(scjson::num(&v, "api_id", 0));
    if (api_id <= 0) {
        std::string s = scjson::str(&v, "api_id");
        if (!s.empty()) {
            try {
                api_id = std::stoi(s);
            } catch (...) {}
        }
    }
    if (api_id <= 0) {
        const char* env_api_id = getenv("SPERMACORE_TELEGRAM_API_ID");
        if (env_api_id && env_api_id[0]) {
            try {
                api_id = std::stoi(env_api_id);
            } catch (...) {}
        }
    }
    if (api_id > 0) g_api_id = api_id;
    std::string hash = scjson::str(&v, "api_hash");
    if (!hash.empty()) g_api_hash = hash;
    else {
        const char* env_hash = getenv("SPERMACORE_TELEGRAM_API_HASH");
        if (env_hash && env_hash[0]) g_api_hash = env_hash;
    }
    std::string phone = scjson::str(&v, "phone");
    if (!phone.empty()) g_phone = phone;
    else {
        const char* env_phone = getenv("SPERMACORE_TELEGRAM_PHONE");
        if (env_phone && env_phone[0]) g_phone = env_phone;
    }
    std::string mode = scjson::str(&v, "login_mode");
    if (!mode.empty()) g_login_mode = mode;
    std::string model = scjson::str(&v, "device_model");
    if (!model.empty()) g_device_model = model;
    std::string version = scjson::str(&v, "app_version");
    if (!version.empty()) g_app_version = version;
    std::string session = scjson::str(&v, "session_path");
    if (!session.empty()) g_session_path = session;
    std::string proxy = scjson::str(&v, "proxy");
    if (!proxy.empty()) g_proxy = proxy;
    std::string enc_key = scjson::str(&v, "database_encryption_key");
    if (!enc_key.empty()) g_database_encryption_key = enc_key;
    const char* env_key = getenv("SPERMACORE_TELEGRAM_DB_KEY");
    if (env_key && env_key[0]) {
        g_database_encryption_key = env_key;
    }
    if (v.get("reply_to")) g_reply_to_enabled = scjson::boolean(&v, "reply_to", true);
    if (v.get("reply_first_only")) {
        g_reply_first_only = scjson::boolean(&v, "reply_first_only", true);
    }
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    if (!api) return SC_ERR_INVALID_ARG;
    g_core = api;
    read_config(cfg);
    if (g_core->data_dir) {
        char* dir = (char*)g_core->data_dir();
        if (dir && dir[0]) g_data_dir = dir;
        if (dir && g_core->free) g_core->free(dir);
    }
    if (g_data_dir.empty()) g_data_dir = "data/telegram";
    g_data_root = fs::path(g_data_dir).parent_path().string();
    std::error_code ec;
    fs::create_directories(g_data_dir, ec);

    if (g_api_id == 0 || g_api_hash.empty()) {
        log_msg(SC_LOG_WARN, "api_id/api_hash missing in config - Telegram is idle");
        return SC_OK;
    }
    start_client();
    return SC_OK;
}

static sc_status_t deinit() {
    stop_client();
    return SC_OK;
}

static sc_status_t on_config_reload(const char* cfg_json) {
    read_config(cfg_json);
    return SC_OK;
}

static sc_status_t on_shutdown() {
    stop_client();
    return SC_OK;
}

static sc_status_t health(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    Value v = scjson::make_object();
    bool connected = g_ready.load() && g_net_ready.load();
    v.set("connected", v_bool(connected));
    v.set("ready", v_bool(g_ready.load()));
    v.set("library_loaded", v_bool(g_loaded.load()));
    v.set("username", v_str(g_self_username));
    v.set("user_id", v_int(g_self_id.load()));
    *out = syscall_string(scjson::dump(v));
    return SC_OK;
}

static sc_status_t get_syscalls(const sc_syscall_reg_t** out, size_t* count) {
    if (out) *out = g_syscalls;
    if (count) *count = sizeof(g_syscalls) / sizeof(g_syscalls[0]);
    return SC_OK;
}

static sc_plugin_api_t g_api = {
    SC_ABI_MAJOR,
    SC_ABI_MINOR,
    sizeof(sc_plugin_api_t),
    get_info,
    init,
    deinit,
    nullptr,  // get_hooks
    get_tools,
    call_tool,
    nullptr,  // on_startup
    nullptr,  // get_system_prompt
    health,
    on_config_reload,
    nullptr,  // health_check_enhanced
    on_shutdown,
    nullptr,  // config_version_sync
    get_syscalls,
    nullptr,  // get_widgets
    get_transport,
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}