/* agentloop.cpp - SpermaCore ABI 1.6.1 / 1.7: Autonomous agent loop module.
 *
 * Fully decoupled from core:
 *   1. WebUI widget "agentloop.cycle" (countdown, live status, trigger and pause).
 *   2. LLM tools: agent_cycle_status, agent_cycle_trigger, agent_cycle_toggle.
 *   3. Initiative syscalls table: "initiative.status", "initiative.trigger",
 *      "initiative.toggle", "initiative.set_interval", "initiative.note_activity".
 *   4. Autonomous background thread running the initiative loop, candidate selection,
 *      username resolution, history retrieval, LLM chat invocation, and message dispatch.
 */
#include "plugin_api.h"
#include "json.hpp"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

static const sc_core_api_t* g_core = nullptr;

/* ---------------------------------------------------------------------
 * state and synchronization
 * ------------------------------------------------------------------- */

static std::mutex g_state_mu;
static std::condition_variable g_cv;
static std::thread* g_worker_thread = nullptr;
static std::atomic<bool> g_stop_requested{false};
static std::atomic<bool> g_manual_trigger{false};

static std::atomic<bool> g_enabled{true};
static std::atomic<bool> g_paused{false};
static std::atomic<bool> g_running{false};
static std::atomic<int>  g_interval_min{15};
static std::atomic<int>  g_active_hours_start{9};
static std::atomic<int>  g_active_hours_end{23};
static std::atomic<int>  g_max_per_day{10};
static std::atomic<int>  g_sent_today{0};
static std::atomic<int64_t> g_next_run_ts{0};
static std::atomic<int64_t> g_last_run_ts{0};

static std::string g_sent_today_date;
static std::string g_last_candidate;
static std::string g_last_result = "waiting for first run";
static std::vector<std::string> g_friends;

static void get_current_date_str(char* buf, size_t sz) {
    time_t now = time(nullptr);
    struct tm tm_buf;
#ifdef _WIN32
    localtime_s(&tm_buf, &now);
#else
    localtime_r(&now, &tm_buf);
#endif
    strftime(buf, sz, "%Y-%m-%d", &tm_buf);
}

static int get_current_hour(void) {
    time_t now = time(nullptr);
    struct tm tm_buf;
#ifdef _WIN32
    localtime_s(&tm_buf, &now);
#else
    localtime_r(&now, &tm_buf);
#endif
    return tm_buf.tm_hour;
}

static bool is_in_active_hours(void) {
    int start = g_active_hours_start.load();
    int end = g_active_hours_end.load();
    if (start < 0 || end < 0) return true;
    int hour = get_current_hour();
    if (start <= end) {
        return hour >= start && hour < end;
    } else {
        return hour >= start || hour < end;
    }
}

/* ---------------------------------------------------------------------
 * status generation
 * ------------------------------------------------------------------- */

static std::string build_status_json_string(void) {
    std::lock_guard<std::mutex> lock(g_state_mu);

    time_t now = time(nullptr);
    int64_t next_run = g_next_run_ts.load();
    int64_t diff = next_run - now;
    if (diff < 0) diff = 0;

    int64_t last_run = g_last_run_ts.load();
    int64_t last_ago = (last_run > 0) ? (now - last_run) : 0;
    if (last_ago < 0) last_ago = 0;

    scjson::Value v = scjson::make_object();
    v.set("enabled", scjson::make_bool(g_enabled.load()));
    v.set("paused", scjson::make_bool(g_paused.load()));
    v.set("running", scjson::make_bool(g_running.load()));
    v.set("interval_min", scjson::make_int(g_interval_min.load()));
    v.set("next_run_in_sec", scjson::make_int(diff));
    v.set("next_run_ts", scjson::make_int(next_run));
    v.set("last_run_ts", scjson::make_int(last_run));
    v.set("last_run_ago_sec", scjson::make_int(last_ago));
    v.set("last_candidate", scjson::make_string(g_last_candidate));
    v.set("last_result", scjson::make_string(g_last_result));
    v.set("sent_today", scjson::make_int(g_sent_today.load()));
    v.set("max_per_day", scjson::make_int(g_max_per_day.load()));
    v.set("active_hours_start", scjson::make_int(g_active_hours_start.load()));
    v.set("active_hours_end", scjson::make_int(g_active_hours_end.load()));
    v.set("in_active_hours", scjson::make_bool(is_in_active_hours()));
    v.set("friends_count", scjson::make_int((long long)g_friends.size()));

    return scjson::dump(v);
}

/* ---------------------------------------------------------------------
 * widget html render
 * ------------------------------------------------------------------- */

static SC_THREAD_LOCAL char g_html_buf[4096];
static SC_THREAD_LOCAL char g_json_buf[8192];

static void build_html(void) {
    bool enabled = g_enabled.load();
    bool paused = g_paused.load();
    bool running = g_running.load();
    int interval_min = g_interval_min.load();
    int sent_today = g_sent_today.load();
    int max_per_day = g_max_per_day.load();
    int start_hour = g_active_hours_start.load();
    int end_hour = g_active_hours_end.load();
    bool in_hours = is_in_active_hours();

    time_t now = time(nullptr);
    int64_t next_run_ts = g_next_run_ts.load();
    int64_t next_run_in_sec = next_run_ts - now;
    if (next_run_in_sec < 0) next_run_in_sec = 0;

    std::string last_res;
    {
        std::lock_guard<std::mutex> lock(g_state_mu);
        last_res = g_last_result;
    }

    char badge[256];
    if (!enabled) {
        snprintf(badge, sizeof(badge),
            "<span style='color:#ef4444; background:rgba(239,68,68,0.15); padding:2px 8px; border-radius:10px; font-weight:700; font-size:10px;'>● disabled</span>");
    } else if (running) {
        snprintf(badge, sizeof(badge),
            "<span style='color:#38bdf8; background:rgba(56,189,248,0.15); padding:2px 8px; border-radius:10px; font-weight:700; font-size:10px;'>⚡ analyzing...</span>");
    } else if (paused) {
        snprintf(badge, sizeof(badge),
            "<span style='color:#eab308; background:rgba(234,179,8,0.15); padding:2px 8px; border-radius:10px; font-weight:700; font-size:10px;'>❚❚ paused</span>");
    } else if (!in_hours) {
        snprintf(badge, sizeof(badge),
            "<span style='color:#a855f7; background:rgba(168,85,247,0.15); padding:2px 8px; border-radius:10px; font-weight:700; font-size:10px;'>🌙 night</span>");
    } else {
        snprintf(badge, sizeof(badge),
            "<span style='color:#22c55e; background:rgba(34,197,94,0.15); padding:2px 8px; border-radius:10px; font-weight:700; font-size:10px;'>● active</span>");
    }

    char timer_block[512];
    if (running) {
        snprintf(timer_block, sizeof(timer_block),
            "<div class='loop-timer' style='font-size:32px; font-weight:800; color:#38bdf8; line-height:1.1;'>⚡ running</div>"
            "<div style='font-size:10px; color:var(--text-3); margin-top:4px;'>llm selecting recipient and generating message</div>");
    } else if (paused) {
        snprintf(timer_block, sizeof(timer_block),
            "<div class='loop-timer' style='font-size:32px; font-weight:800; color:#eab308; line-height:1.1;'>❚❚ paused</div>"
            "<div style='font-size:10px; color:var(--text-3); margin-top:4px;'>loop paused (click resume)</div>");
    } else {
        long long min = next_run_in_sec / 60;
        long long sec = next_run_in_sec % 60;
        if (min < 0) min = 0;
        if (sec < 0) sec = 0;
        snprintf(timer_block, sizeof(timer_block),
            "<div class='loop-timer' data-countdown-ts='%lld' style='font-size:36px; font-weight:800; color:var(--accent); line-height:1.1; font-variant-numeric:tabular-nums;'>%02lld:%02lld</div>"
            "<div style='font-size:10px; color:var(--text-3); margin-top:4px;'>until next cycle (every %d min)</div>",
            (long long)next_run_ts, min, sec, interval_min);
    }

    char toggle_btn[256];
    if (paused) {
        snprintf(toggle_btn, sizeof(toggle_btn),
            "<button onclick=\"widgetClick('agentloop.cycle', {act:'toggle'})\" "
            "style='flex:1; padding:6px 12px; background:rgba(34,197,94,0.15); border:1px solid rgba(34,197,94,0.3); color:#22c55e; border-radius:6px; font-size:11px; font-weight:700; cursor:pointer;'>▶ resume</button>");
    } else {
        snprintf(toggle_btn, sizeof(toggle_btn),
            "<button onclick=\"widgetClick('agentloop.cycle', {act:'toggle'})\" "
            "style='flex:1; padding:6px 12px; background:rgba(234,179,8,0.15); border:1px solid rgba(234,179,8,0.3); color:#eab308; border-radius:6px; font-size:11px; font-weight:700; cursor:pointer;'>❚❚ pause</button>");
    }

    snprintf(g_html_buf, sizeof(g_html_buf),
        "<div style='font-family:var(--font, system-ui); display:flex; flex-direction:column; gap:10px;'>"
          "<div style='display:flex; justify-content:space-between; align-items:center;'>"
            "<span style='font-size:11px; font-weight:700; text-transform:uppercase; letter-spacing:0.5px; color:var(--text-2);'>autonomous agent loop</span>"
            "%s"
          "</div>"
          "<div style='background:var(--bg-2, rgba(255,255,255,0.03)); border:1px solid var(--border-color, rgba(255,255,255,0.06)); border-radius:10px; padding:12px; text-align:center;'>"
            "%s"
          "</div>"
          "<div style='display:grid; grid-template-columns:1fr 1fr; gap:6px; font-size:11px;'>"
            "<div style='background:var(--bg-2, rgba(255,255,255,0.02)); border:1px solid var(--border-color, rgba(255,255,255,0.05)); border-radius:6px; padding:6px 8px;'>"
              "<div style='color:var(--text-3); font-size:9px; text-transform:uppercase;'>today quota</div>"
              "<div style='font-weight:700; color:var(--text-1); margin-top:2px;'>%d / %d</div>"
            "</div>"
            "<div style='background:var(--bg-2, rgba(255,255,255,0.02)); border:1px solid var(--border-color, rgba(255,255,255,0.05)); border-radius:6px; padding:6px 8px;'>"
              "<div style='color:var(--text-3); font-size:9px; text-transform:uppercase;'>active hours</div>"
              "<div style='font-weight:700; color:var(--text-1); margin-top:2px;'>%02d:00 - %02d:00</div>"
            "</div>"
          "</div>"
          "<div style='background:var(--bg-2, rgba(255,255,255,0.02)); border:1px solid var(--border-color, rgba(255,255,255,0.05)); border-radius:6px; padding:6px 8px; font-size:10px; color:var(--text-3); overflow:hidden; text-overflow:ellipsis; white-space:nowrap;'>"
            "<span style='color:var(--text-2); font-weight:600;'>last:</span> %s"
          "</div>"
          "<div style='display:flex; gap:6px; margin-top:2px;'>"
            "<button onclick=\"widgetClick('agentloop.cycle', {act:'trigger'})\" "
              "style='flex:1; padding:6px 12px; background:var(--accent, #6366f1); border:none; color:#fff; border-radius:6px; font-size:11px; font-weight:700; cursor:pointer;'>⚡ run now</button>"
            "%s"
          "</div>"
        "</div>",
        badge,
        timer_block,
        sent_today, max_per_day,
        start_hour, end_hour,
        last_res.c_str(),
        toggle_btn
    );
}

static void wrap_json_html(void) {
    scjson::Value v = scjson::make_object();
    v.set("html", scjson::make_string(g_html_buf));
    v.set("__html", scjson::make_string(g_html_buf));
    std::string s = scjson::dump(v);
    snprintf(g_json_buf, sizeof(g_json_buf), "%s", s.c_str());
}

static sc_status_t widget_render(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    build_html();
    *out = g_html_buf;
    return SC_OK;
}

static sc_status_t widget_update(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    build_html();
    wrap_json_html();
    *out = g_json_buf;
    return SC_OK;
}

static void trigger_cycle(void) {
    g_manual_trigger = true;
    g_cv.notify_all();
}

static bool toggle_pause(void) {
    bool p = !g_paused.load();
    g_paused.store(p);
    g_cv.notify_all();
    return p;
}

static sc_status_t widget_click(const char* payload_json, char** out) {
    if (!out) return SC_ERR_INVALID_ARG;

    int is_trigger = 0;
    int is_toggle = 0;

    if (payload_json) {
        scjson::Value pval;
        if (scjson::parse(payload_json, pval)) {
            std::string act = scjson::str(&pval, "act");
            if (act == "trigger" || act == "run") {
                is_trigger = 1;
            } else if (act == "toggle" || act == "pause" || act == "resume") {
                is_toggle = 1;
            }
        }
    }

    if (is_trigger) {
        trigger_cycle();
        if (g_core && g_core->widget_notify) {
            g_core->widget_notify("agentloop.cycle", 2, "starting initiative cycle...", 2000);
        }
    } else if (is_toggle) {
        bool now_paused = toggle_pause();
        if (g_core && g_core->widget_notify) {
            g_core->widget_notify("agentloop.cycle", 3,
                now_paused ? "agent loop paused" : "agent loop resumed", 2000);
        }
    }

    return widget_update(out);
}

/* ---------------------------------------------------------------------
 * autonomous initiative cycle execution
 * ------------------------------------------------------------------- */

static void do_initiative_tick(void) {
    if (!g_enabled.load()) return;

    time_t now = time(nullptr);
    g_last_run_ts.store(now);
    g_running.store(true);

    char today_str[32] = {0};
    get_current_date_str(today_str, sizeof(today_str));

    {
        std::lock_guard<std::mutex> lock(g_state_mu);
        if (g_sent_today_date != today_str) {
            g_sent_today_date = today_str;
            g_sent_today.store(0);
        }
    }

    /* 1. active hours check */
    if (!is_in_active_hours()) {
        std::lock_guard<std::mutex> lock(g_state_mu);
        char buf[128];
        snprintf(buf, sizeof(buf), "outside active hours (%02d:00 - %02d:00)",
            g_active_hours_start.load(), g_active_hours_end.load());
        g_last_result = buf;
        g_running.store(false);
        return;
    }

    /* 2. daily quota check */
    if (g_sent_today.load() >= g_max_per_day.load()) {
        std::lock_guard<std::mutex> lock(g_state_mu);
        g_last_result = "daily message limit reached";
        g_running.store(false);
        return;
    }

    /* 3. pick candidate */
    std::string candidate;
    {
        std::lock_guard<std::mutex> lock(g_state_mu);
        if (!g_friends.empty()) {
            candidate = g_friends[0];
        }
    }

    if (candidate.empty() && g_core && g_core->syscall) {
        char* users_out = nullptr;
        if (g_core->syscall("users.list", "{}", &users_out) == SC_OK && users_out) {
            scjson::Value uval;
            if (scjson::parse(users_out, uval)) {
                if (const auto* uarr = uval.get("users")) {
                    if (uarr->is_array()) {
                        for (const auto& u : uarr->array) {
                            if (u.type == scjson::Type::String && !u.string.empty()) {
                                if (u.string != "unknown" && u.string != "dummy" &&
                                    u.string != "friend" && u.string != "human") {
                                    candidate = u.string;
                                    break;
                                }
                            }
                        }
                    }
                }
            }
            if (g_core->free) g_core->free(users_out);
        }
    }

    if (candidate.empty()) {
        std::lock_guard<std::mutex> lock(g_state_mu);
        g_last_result = "no available dialogs";
        g_running.store(false);
        return;
    }

    /* resolve candidate to numeric chat_id */
    long long target_chat_id = 0;
    bool is_numeric = true;
    size_t start_idx = (candidate[0] == '-') ? 1 : 0;
    if (start_idx >= candidate.size()) is_numeric = false;
    for (size_t i = start_idx; i < candidate.size(); i++) {
        if (candidate[i] < '0' || candidate[i] > '9') {
            is_numeric = false;
            break;
        }
    }

    if (is_numeric) {
        try {
            target_chat_id = std::stoll(candidate);
        } catch (...) {
            target_chat_id = 0;
        }
    } else if (g_core && g_core->syscall) {
        std::string uname = (candidate[0] == '@') ? candidate.substr(1) : candidate;
        scjson::Value req = scjson::make_object();
        req.set("username", scjson::make_string(uname));
        char* res_out = nullptr;
        if (g_core->syscall("tg.resolve_username", scjson::dump(req).c_str(), &res_out) == SC_OK && res_out) {
            scjson::Value rval;
            if (scjson::parse(res_out, rval)) {
                target_chat_id = scjson::num(&rval, "id", 0);
                if (target_chat_id == 0) {
                    target_chat_id = scjson::num(&rval, "chat_id", 0);
                }
            }
            if (g_core->free) g_core->free(res_out);
        }
    }

    {
        std::lock_guard<std::mutex> lock(g_state_mu);
        g_last_candidate = candidate;
        g_last_result = "generating message for @" + candidate + "...";
    }

    if (g_core && g_core->log) {
        char log_msg[256];
        snprintf(log_msg, sizeof(log_msg), "agent loop: analyzing conversation with @%s (chat_id=%lld)",
                 candidate.c_str(), target_chat_id);
        g_core->log(SC_LOG_INFO, "agentloop", log_msg);
    }

    /* 4. fetch recent history for target chat */
    std::string history_transcript;
    if (g_core && g_core->syscall) {
        scjson::Value hreq = scjson::make_object();
        hreq.set("chat_id", scjson::make_int(target_chat_id));
        char* hist_out = nullptr;
        if (g_core->syscall("context.recent", scjson::dump(hreq).c_str(), &hist_out) == SC_OK && hist_out) {
            scjson::Value hval;
            if (scjson::parse(hist_out, hval)) {
                history_transcript = scjson::str(&hval, "transcript");
            }
            if (g_core->free) g_core->free(hist_out);
        }
    }

    /* 5. invoke llm.chat syscall */
    std::string generated_text;
    if (g_core && g_core->syscall) {
        scjson::Value root = scjson::make_object();
        scjson::Value msgs = scjson::make_array();

        std::string sys_prompt = "you are in an autonomous agent initiative loop. some time has passed and you decide to message someone in telegram.\n"
                                 "recipient: @" + candidate + "\n";
        if (!history_transcript.empty()) {
            sys_prompt += "recent context:\n" + history_transcript + "\n";
        }
        sys_prompt += "your task: write a natural first message like a real friend or bro (1-2 short sentences). if right now is not the right time or you have nothing to say, reply strictly with the single word SKIP.";

        scjson::Value sys_msg = scjson::make_object();
        sys_msg.set("role", scjson::make_string("system"));
        sys_msg.set("content", scjson::make_string(sys_prompt));
        msgs.push(std::move(sys_msg));

        scjson::Value user_msg = scjson::make_object();
        user_msg.set("role", scjson::make_string("user"));
        user_msg.set("content", scjson::make_string("decide what to write to @" + candidate + ", or reply SKIP if not the right time."));
        msgs.push(std::move(user_msg));

        root.set("messages", std::move(msgs));

        char* llm_out = nullptr;
        if (g_core->syscall("llm.chat", scjson::dump(root).c_str(), &llm_out) == SC_OK && llm_out) {
            scjson::Value lval;
            if (scjson::parse(llm_out, lval)) {
                generated_text = scjson::str(&lval, "content");
            }
            if (g_core->free) g_core->free(llm_out);
        }
    }

    /* 6. send message if not SKIP */
    if (generated_text.empty() || generated_text == "SKIP" ||
        generated_text.find("SKIP") != std::string::npos) {
        std::lock_guard<std::mutex> lock(g_state_mu);
        g_last_result = "skipped (SKIP) for @" + candidate;
        g_running.store(false);
        return;
    }

    /* send message via transport.send or tg.send_to_username */
    bool sent = false;
    if (g_core && g_core->syscall) {
        if (target_chat_id != 0) {
            scjson::Value sreq = scjson::make_object();
            sreq.set("chat_id", scjson::make_int(target_chat_id));
            sreq.set("text", scjson::make_string(generated_text));
            char* send_out = nullptr;
            if (g_core->syscall("transport.send", scjson::dump(sreq).c_str(), &send_out) == SC_OK && send_out) {
                scjson::Value resp;
                if (scjson::parse(send_out, resp)) {
                    if (scjson::str(&resp, "status") == "ok" || scjson::num(&resp, "message_id", 0) > 0) {
                        sent = true;
                    }
                }
                if (g_core->free) g_core->free(send_out);
            }
        }

        /* fallback via tg.send_to_username if candidate is username and transport.send wasn't sent */
        if (!sent && !is_numeric) {
            std::string uname = (candidate[0] == '@') ? candidate.substr(1) : candidate;
            scjson::Value ureq = scjson::make_object();
            ureq.set("username", scjson::make_string(uname));
            ureq.set("text", scjson::make_string(generated_text));
            char* send_out = nullptr;
            if (g_core->syscall("tg.send_to_username", scjson::dump(ureq).c_str(), &send_out) == SC_OK && send_out) {
                scjson::Value resp;
                if (scjson::parse(send_out, resp)) {
                    if (scjson::str(&resp, "status") == "ok") {
                        sent = true;
                    }
                }
                if (g_core->free) g_core->free(send_out);
            }
        }
    }

    if (sent) {
        g_sent_today.fetch_add(1);
        std::lock_guard<std::mutex> lock(g_state_mu);
        g_last_result = "sent @" + candidate + ": " + generated_text.substr(0, 30);
        if (g_core && g_core->log) {
            char log_buf[512];
            snprintf(log_buf, sizeof(log_buf), "agent loop: message sent successfully to @%s", candidate.c_str());
            g_core->log(SC_LOG_INFO, "agentloop", log_buf);
        }
        if (g_core && g_core->widget_notify) {
            std::string notif = "initiative: sent to @" + candidate;
            g_core->widget_notify("agentloop.cycle", 2, notif.c_str(), 3000);
        }
    } else {
        std::lock_guard<std::mutex> lock(g_state_mu);
        g_last_result = "send error @" + candidate;
    }

    g_running.store(false);
}

static void worker_loop(void) {
    while (!g_stop_requested.load()) {
        int interval_m = g_interval_min.load();
        if (interval_m <= 0) interval_m = 15;

        time_t now = time(nullptr);
        g_next_run_ts.store(now + (int64_t)interval_m * 60);

        std::unique_lock<std::mutex> lock(g_state_mu);
        auto wait_time = std::chrono::seconds(interval_m * 60);
        g_cv.wait_for(lock, wait_time, [] {
            return g_stop_requested.load() || g_manual_trigger.load();
        });

        if (g_stop_requested.load()) break;

        bool is_manual = g_manual_trigger.exchange(false);
        lock.unlock();

        if (is_manual || (!g_paused.load() && g_enabled.load())) {
            do_initiative_tick();
        }

        /* update widget */
        if (g_core && g_core->widget_emit) {
            build_html();
            wrap_json_html();
            g_core->widget_emit("agentloop.cycle", g_json_buf);
        }
    }
}

/* ---------------------------------------------------------------------
 * initiative syscall handler
 * ------------------------------------------------------------------- */

static SC_THREAD_LOCAL std::string g_syscall_out;

static sc_status_t SC_CALL handle_initiative_syscall(const char* method, const char* args_json, char** out_json) {
    if (!method || !out_json) return SC_ERR_INVALID_ARG;
    *out_json = nullptr;

    if (strcmp(method, "initiative.status") == 0) {
        g_syscall_out = build_status_json_string();
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "initiative.trigger") == 0) {
        trigger_cycle();
        g_syscall_out = "{\"status\":\"triggered\"}";
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "initiative.toggle") == 0) {
        bool p = toggle_pause();
        scjson::Value resp = scjson::make_object();
        resp.set("status", scjson::make_string("ok"));
        resp.set("paused", scjson::make_bool(p));
        g_syscall_out = scjson::dump(resp);
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "initiative.set_interval") == 0) {
        scjson::Value a;
        scjson::parse(args_json ? args_json : "{}", a);
        long long iv = scjson::num(&a, "interval_min", 15);
        if (iv > 0 && iv <= 1440) {
            g_interval_min.store((int)iv);
            time_t now = time(nullptr);
            g_next_run_ts.store(now + iv * 60);
        }
        scjson::Value resp = scjson::make_object();
        resp.set("status", scjson::make_string("ok"));
        resp.set("interval_min", scjson::make_int(g_interval_min.load()));
        g_syscall_out = scjson::dump(resp);
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    if (strcmp(method, "initiative.note_activity") == 0) {
        g_syscall_out = "{\"status\":\"ok\"}";
        *out_json = (char*)g_syscall_out.c_str();
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

static const sc_syscall_reg_t g_syscalls[] = {
    {
        .prefix = "initiative",
        .fn = handle_initiative_syscall,
    },
};

static sc_status_t get_syscalls(const sc_syscall_reg_t** out, size_t* count) {
    if (!out || !count) return SC_ERR_INVALID_ARG;
    *out = g_syscalls;
    *count = sizeof(g_syscalls) / sizeof(g_syscalls[0]);
    return SC_OK;
}

/* ---------------------------------------------------------------------
 * widget registry (abi 1.6.1)
 * ------------------------------------------------------------------- */

static const sc_widget_info_t g_widgets[] = {
    {
        .id = "agentloop.cycle",
        .title = "Agent Loop",
        .icon = "⚡",
        .default_w = 300,
        .default_h = 240,
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
 * llm tools
 * ------------------------------------------------------------------- */

static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "[{\"name\":\"agent_cycle_status\","
        "\"description\":\"Get status of autonomous agent initiative loop, countdown to next cycle, active hours and daily quota.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}},"
        "{\"name\":\"agent_cycle_trigger\","
        "\"description\":\"Trigger an immediate check and message in the autonomous agent initiative loop.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}},"
        "{\"name\":\"agent_cycle_toggle\","
        "\"description\":\"Toggle pause state of the autonomous agent initiative loop.\","
        "\"parameters\":{\"type\":\"object\",\"properties\":{},\"required\":[]}}]";
    *out = (char*)tools;
    return SC_OK;
}

static SC_THREAD_LOCAL std::string g_tool_out;

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    (void)args;
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;

    if (strcmp(name, "agent_cycle_status") == 0) {
        g_tool_out = build_status_json_string();
        *out = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "agent_cycle_trigger") == 0) {
        trigger_cycle();
        g_tool_out = "{\"status\":\"triggered\"}";
        *out = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    if (strcmp(name, "agent_cycle_toggle") == 0) {
        bool p = toggle_pause();
        scjson::Value resp = scjson::make_object();
        resp.set("status", scjson::make_string("ok"));
        resp.set("paused", scjson::make_bool(p));
        g_tool_out = scjson::dump(resp);
        *out = (char*)g_tool_out.c_str();
        return SC_OK;
    }

    return SC_ERR_NOT_FOUND;
}

/* ---------------------------------------------------------------------
 * plugin metadata & lifecycle
 * ------------------------------------------------------------------- */

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name        = "agentloop",
        .version     = "1.2.0",
        .description = "Autonomous agent loop controls, countdown timer and status",
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
        .on_config_version_change_ud = nullptr,

        .abi_patch = 1,
        ._reserved_1_6_1 = {0, 0, 0},
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    if (!g_core) return SC_ERR_INVALID_ARG;

    if (cfg) {
        scjson::Value cval;
        if (scjson::parse(cfg, cval)) {
            if (cval.get("enabled")) {
                g_enabled.store(scjson::boolean(&cval, "enabled", true));
            }

            long long iv = scjson::num(&cval, "interval_min", 0);
            if (iv <= 0) iv = scjson::num(&cval, "check_interval_min", 15);
            if (iv > 0) g_interval_min.store((int)iv);

            long long hs = scjson::num(&cval, "active_hours_start", -1);
            if (hs >= 0 && hs < 24) g_active_hours_start.store((int)hs);

            long long he = scjson::num(&cval, "active_hours_end", -1);
            if (he >= 0 && he <= 24) g_active_hours_end.store((int)he);

            long long max_d = scjson::num(&cval, "max_per_day", 0);
            if (max_d > 0) g_max_per_day.store((int)max_d);

            if (const auto* farr = cval.get("friends")) {
                if (farr->is_array()) {
                    std::lock_guard<std::mutex> lock(g_state_mu);
                    g_friends.clear();
                    for (const auto& item : farr->array) {
                        if (item.type == scjson::Type::String && !item.string.empty()) {
                            g_friends.push_back(item.string);
                        }
                    }
                }
            }
        }
    }

    time_t now = time(nullptr);
    g_next_run_ts.store(now + (int64_t)g_interval_min.load() * 60);

    if (g_worker_thread) {
        g_stop_requested = true;
        g_cv.notify_all();
        if (g_worker_thread->joinable()) {
            g_worker_thread->join();
        }
        delete g_worker_thread;
        g_worker_thread = nullptr;
    }
    g_stop_requested = false;
    g_manual_trigger = false;
    g_worker_thread = new std::thread(worker_loop);

    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "agentloop", "agent loop worker thread started");
    }
    return SC_OK;
}

static sc_status_t deinit(void) {
    g_stop_requested = true;
    g_cv.notify_all();
    if (g_worker_thread) {
        if (g_worker_thread->joinable()) {
            g_worker_thread->join();
        }
        delete g_worker_thread;
        g_worker_thread = nullptr;
    }
    g_core = nullptr;
    return SC_OK;
}

static sc_status_t get_hooks(const sc_hook_reg_t** out_hooks, size_t* count) {
    if (out_hooks) *out_hooks = nullptr;
    if (count)     *count = 0;
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
