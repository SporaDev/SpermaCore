/* tdjson.hpp - dynamic loader for TDLib JSON API.
 *
 * the plugin loads tdjson dynamically without compile-time linking:
 * tdjson.dll / libtdjson.so is resolved at runtime during init().
 */
#ifndef SPERMACORE_TELEGRAM_TDJSON_HPP
#define SPERMACORE_TELEGRAM_TDJSON_HPP

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <map>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#include "json.hpp"

#if defined(_WIN32)
#include <windows.h>
#else
#include <dlfcn.h>
#endif

namespace tdjson {

using create_fn = void* (*)();
using send_fn = void (*)(void*, const char*);
using receive_fn = const char* (*)(void*, double);
using execute_fn = const char* (*)(void*, const char*);
using destroy_fn = void (*)(void*);
using set_log_verbosity_fn = void (*)(int);
using log_message_cb_fn = void (*)(int, const char*);
using set_log_cb_fn = void (*)(log_message_cb_fn);

// forward tdlib log events to core logger
inline std::function<void(int, const char*)> g_log_sink;
inline void log_bridge(int level, const char* message) {
    if (g_log_sink) g_log_sink(level, message);
}

class Client {
public:
    using EventFn = std::function<void(const scjson::Value&)>;

    Client() = default;
    ~Client() { close(); }
    Client(const Client&) = delete;
    Client& operator=(const Client&) = delete;

    bool load(const std::string& module_dir, std::string* error) {
        std::vector<std::string> candidates;
#if defined(_WIN32)
        const char* names[] = {"tdjson.dll", "libtdjson.dll"};
#elif defined(__APPLE__)
        const char* names[] = {"libtdjson.dylib", "tdjson.dylib"};
#else
        const char* names[] = {"libtdjson.so", "tdjson.so"};
#endif
        for (const char* n : names) {
            if (!module_dir.empty()) {
                candidates.push_back(module_dir + "/" + n);
                candidates.push_back(module_dir + "/../" + n);
                candidates.push_back(module_dir + "/tdlib/" + n);
            }
            candidates.push_back(std::string("data/telegram/") + n);
            candidates.push_back(std::string("./data/telegram/") + n);
            candidates.push_back(std::string("../data/telegram/") + n);
            candidates.emplace_back(n);
        }

        for (const auto& path : candidates) {
#if defined(_WIN32)
            HMODULE h = nullptr;
            if (path.find('/') != std::string::npos || path.find('\\') != std::string::npos) {
                h = LoadLibraryExA(path.c_str(), nullptr, LOAD_WITH_ALTERED_SEARCH_PATH);
            } else {
                h = LoadLibraryA(path.c_str());
            }
            if (h) {
                handle_ = reinterpret_cast<void*>(h);
                break;
            }
#else
            void* h = dlopen(path.c_str(), RTLD_NOW | RTLD_LOCAL);
            if (h) {
                handle_ = h;
                break;
            }
#endif
        }
        if (!handle_) {
            if (error) *error = "tdjson library not found";
            return false;
        }

        create_ = reinterpret_cast<create_fn>(sym("td_json_client_create"));
        send_ = reinterpret_cast<send_fn>(sym("td_json_client_send"));
        receive_ = reinterpret_cast<receive_fn>(sym("td_json_client_receive"));
        execute_ = reinterpret_cast<execute_fn>(sym("td_json_client_execute"));
        destroy_ = reinterpret_cast<destroy_fn>(sym("td_json_client_destroy"));
        auto set_log = reinterpret_cast<set_log_verbosity_fn>(sym("td_set_log_verbosity_level"));
        auto set_log_cb = reinterpret_cast<set_log_cb_fn>(sym("td_set_log_message_callback"));
        if (set_log_cb) set_log_cb(&log_bridge);
        if (set_log) set_log(1);

        if (!create_ || !send_ || !receive_ || !execute_ || !destroy_) {
            if (error) *error = "tdjson symbols missing";
            unload();
            return false;
        }
        return true;
    }

    bool create(std::string* error) {
        if (!create_) {
            if (error) *error = "tdjson not loaded";
            return false;
        }
        client_ = create_();
        if (!client_) {
            if (error) *error = "td_json_client_create failed";
            return false;
        }
        running_.store(true);
        receiver_ = std::thread([this] { receiver_loop(); });
        dispatcher_ = std::thread([this] { dispatch_loop(); });
        return true;
    }

    void close() {
        running_.store(false);
        if (receiver_.joinable()) receiver_.join();
        {
            std::lock_guard<std::mutex> lock(queue_mu_);
            queue_cv_.notify_all();
        }
        if (dispatcher_.joinable()) dispatcher_.join();
        {
            std::lock_guard<std::mutex> lock(mu_);
            pending_.clear();
        }
        if (client_ && destroy_) {
            destroy_(client_);
            client_ = nullptr;
        }
        unload();
    }

    void set_event_handler(EventFn fn) {
        std::lock_guard<std::mutex> lock(handler_mu_);
        handler_ = std::move(fn);
    }

    // request executes a request and waits for matching @extra response
    bool request(const scjson::Value& req, scjson::Value& out, int timeout_ms = 10000) {
        long long extra = next_extra_.fetch_add(1) + 1;
        scjson::Value copy = req;
        copy.set("@extra", scjson::make_int(extra));
        {
            std::lock_guard<std::mutex> lock(mu_);
            pending_[extra] = Pending{};
        }
        if (!post(copy)) {
            std::lock_guard<std::mutex> lock(mu_);
            pending_.erase(extra);
            return false;
        }
        std::unique_lock<std::mutex> lock(mu_);
        bool ready = cv_.wait_for(lock, std::chrono::milliseconds(timeout_ms), [&] {
            auto it = pending_.find(extra);
            return it == pending_.end() || it->second.done;
        });
        if (!ready) {
            pending_.erase(extra);
            return false;
        }
        auto it = pending_.find(extra);
        if (it == pending_.end()) return false;
        out = std::move(it->second.value);
        pending_.erase(it);
        return true;
    }

    bool post(const scjson::Value& req) {
        if (!client_ || !send_) return false;
        const std::string data = scjson::dump(req);
        send_(client_, data.c_str());
        return true;
    }

    bool execute(const scjson::Value& req, scjson::Value& out) {
        if (!execute_) return false;
        const std::string data = scjson::dump(req);
        const char* raw = execute_(client_, data.c_str());
        if (!raw) return false;
        return scjson::parse(raw, out);
    }

    bool loaded() const { return handle_ != nullptr; }

private:
    struct Pending {
        bool done = false;
        scjson::Value value;
    };

    void receiver_loop() {
        while (running_.load()) {
            const char* raw = receive_(client_, 0.2);
            if (!raw) continue;
            scjson::Value value;
            if (!scjson::parse(raw, value)) continue;
            const scjson::Value* extra = value.get("@extra");
            if (extra && extra->type == scjson::Type::Number && extra->is_integer) {
                long long id = extra->integer;
                {
                    std::lock_guard<std::mutex> lock(mu_);
                    auto it = pending_.find(id);
                    if (it != pending_.end()) {
                        it->second.value = std::move(value);
                        it->second.done = true;
                    }
                }
                cv_.notify_all();
                continue;
            }
            EventFn handler;
            {
                std::lock_guard<std::mutex> lock(handler_mu_);
                handler = handler_;
            }
            if (handler) {
                std::lock_guard<std::mutex> lock(queue_mu_);
                queue_.push_back(std::move(value));
                queue_cv_.notify_one();
            }
        }
    }

    void dispatch_loop() {
        while (running_.load()) {
            scjson::Value value;
            {
                std::unique_lock<std::mutex> lock(queue_mu_);
                queue_cv_.wait_for(lock, std::chrono::milliseconds(200), [&] {
                    return !queue_.empty() || !running_.load();
                });
                if (queue_.empty()) continue;
                value = std::move(queue_.front());
                queue_.pop_front();
            }
            EventFn handler;
            {
                std::lock_guard<std::mutex> lock(handler_mu_);
                handler = handler_;
            }
            if (handler) handler(value);
        }
    }

    void* sym(const char* name) {
#if defined(_WIN32)
        return reinterpret_cast<void*>(GetProcAddress(reinterpret_cast<HMODULE>(handle_), name));
#else
        return dlsym(handle_, name);
#endif
    }

    void unload() {
        if (!handle_) return;
#if defined(_WIN32)
        FreeLibrary(reinterpret_cast<HMODULE>(handle_));
#else
        dlclose(handle_);
#endif
        handle_ = nullptr;
    }

    void* handle_ = nullptr;
    void* client_ = nullptr;
    create_fn create_ = nullptr;
    send_fn send_ = nullptr;
    receive_fn receive_ = nullptr;
    execute_fn execute_ = nullptr;
    destroy_fn destroy_ = nullptr;

    std::atomic<bool> running_{false};
    std::atomic<long long> next_extra_{0};
    std::thread receiver_;
    std::thread dispatcher_;

    std::mutex mu_;
    std::condition_variable cv_;
    std::map<long long, Pending> pending_;

    std::mutex queue_mu_;
    std::condition_variable queue_cv_;
    std::deque<scjson::Value> queue_;

    std::mutex handler_mu_;
    EventFn handler_;
};

}  // namespace tdjson

#endif  // SPERMACORE_TELEGRAM_TDJSON_HPP