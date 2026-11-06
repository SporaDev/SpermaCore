#ifndef SPERMACORE_PLUGIN_API_H
#define SPERMACORE_PLUGIN_API_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#if defined(_WIN32)
   #define SC_EXPORT __declspec(dllexport)
   #define SC_CALL    __cdecl
#elif defined(__GNUC__)
   #define SC_EXPORT __attribute__((visibility("default")))
   #define SC_CALL
#else
   #define SC_EXPORT
   #define SC_CALL
#endif

// portable thread-local storage specifier
#ifndef SC_THREAD_LOCAL
   #ifdef __cplusplus
      #define SC_THREAD_LOCAL thread_local
   #else
      #define SC_THREAD_LOCAL _Thread_local
   #endif
#endif

/* abi version history:
 *   1.0 - base contract
 *   1.1 - prompt_*, on_startup
 *   1.2 - get_system_prompt
 *   1.3 - config_get_*
 *   1.4 - self-config, flags, health, on_config_reload, config_ensure, now_ms, log_json
 *   1.5 - media handling, health check, graceful shutdown, config versioning, async tools, event filtering
 *   1.6 - syscall registry, transport registry, pipeline hooks
 *   1.6.1 - webui widgets
 *   1.7 - pluggable transports
 *   1.7.1 - c++20 support
 */
#define SC_ABI_MAJOR 1u
#define SC_ABI_MINOR 7u
#define SC_ABI_PATCH 1u

// status codes
typedef enum sc_status {
    SC_OK                   =  0,
    SC_ERR_GENERIC          = -1,
    SC_ERR_INVALID_ARG      = -2,
    SC_ERR_NOT_FOUND        = -3,
    SC_ERR_DISABLED         = -4,
    SC_ERR_INTERNAL         = -5,
    SC_ERR_NO_MEMORY        = -6,
    SC_ERR_NOT_IMPLEMENTED  = -7,
    SC_ERR_TIMEOUT          = -8,
    SC_ERR_FORBIDDEN        = -9,
    SC_ERR_SHUTDOWN         = -10,
} sc_status_t;

typedef enum sc_log_level {
    SC_LOG_DEBUG = 0,
    SC_LOG_INFO  = 1,
    SC_LOG_TOOL  = 2,
    SC_LOG_WARN  = 3,
    SC_LOG_ERROR = 4,
} sc_log_level_t;

typedef enum sc_health_severity {
    SC_HEALTH_OK         = 0,
    SC_HEALTH_DEGRADED   = 1,
    SC_HEALTH_CRITICAL   = 2,
    SC_HEALTH_UNKNOWN    = 3,
} sc_health_severity_t;

typedef enum sc_media_type {
    SC_MEDIA_NONE              = 0,
    SC_MEDIA_TEXT              = 1,
    SC_MEDIA_PHOTO             = 2,
    SC_MEDIA_VIDEO_MESSAGE     = 3,
    SC_MEDIA_VOICE             = 4,
    SC_MEDIA_AUDIO             = 5,
    SC_MEDIA_STICKER           = 6,
    SC_MEDIA_ANIMATED_STICKER  = 7,
    SC_MEDIA_DOCUMENT          = 8,
    SC_MEDIA_LINK_PREVIEW      = 9,
    SC_MEDIA_LOCATION          = 10,
} sc_media_type_t;

// webui widget descriptor
typedef struct sc_widget_info {
    const char* id;          // unique id e.g. "weather.main"
    const char* title;       // widget title
    const char* icon;        // emoji icon or null
    uint32_t    default_w;   // width in pixels, 0 = 300
    uint32_t    default_h;   // height in pixels, 0 = 200

    // render initial html/js content
    sc_status_t (SC_CALL *render_fn)(char** out_html);

    // periodic update callback returning json
    sc_status_t (SC_CALL *update_fn)(char** out_json);

    // optional click handler
    sc_status_t (SC_CALL *click_handler)(const char* payload_json, char** out_json);

    uint32_t    _reserved[4];
} sc_widget_info_t;

// hook names
#define SC_HOOK_BEFORE_EVENT          "before_event"
#define SC_HOOK_BEFORE_LLM_CALL       "before_llm_call"
#define SC_HOOK_AFTER_LLM_RESPONSE    "after_llm_response"
#define SC_HOOK_ON_TOOL_CALL          "on_tool_call"
#define SC_HOOK_ON_TOOL_RESULT        "on_tool_result"
#define SC_HOOK_ON_SLEEP              "on_sleep"
#define SC_HOOK_ON_WAKE               "on_wake"
#define SC_HOOK_ON_MESSAGE_SENT       "on_message_sent"
#define SC_HOOK_ON_AUTO_TRIGGER       "on_auto_trigger"
#define SC_HOOK_BEFORE_SEND           "before_send"
#define SC_HOOK_AFTER_SEND            "after_send"
#define SC_HOOK_SHUTDOWN              "on_shutdown"
#define SC_HOOK_ASYNC_COMPLETED       "async_tool_completed"
#define SC_HOOK_ON_MEDIA              "on_media"
#define SC_HOOK_ON_CUSTOM_EVENT       "on_custom_event"
#define SC_HOOK_ON_TRANSPORT_SEND     "on_transport_send"

// plugin flags
#define SC_PLUGIN_FLAG_THREAD_SAFE          (1u << 0)  // thread-safe; if not set, core serializes calls
#define SC_PLUGIN_FLAG_OWNS_TOOL_RESULT     (1u << 1)  // out_json is malloced and owned by core
#define SC_PLUGIN_FLAG_NO_HOT_RELOAD        (1u << 2)
#define SC_PLUGIN_FLAG_REQUIRES_NETWORK     (1u << 3)
#define SC_PLUGIN_FLAG_MEDIA_HANDLING       (1u << 4)
#define SC_PLUGIN_FLAG_ASYNC_TOOLS          (1u << 5)
#define SC_PLUGIN_FLAG_EVENT_FILTERING      (1u << 6)
#define SC_PLUGIN_FLAG_HAS_WIDGETS          (1u << 7)  // plugin provides webui widgets
#define SC_PLUGIN_FLAG_TRANSPORT            (1u << 8)  // plugin provides a message transport

// transport capabilities
#define SC_TRANSPORT_CAP_TEXT           (1u << 0)  // send text
#define SC_TRANSPORT_CAP_MEDIA          (1u << 1)  // send media
#define SC_TRANSPORT_CAP_REACTIONS      (1u << 2)  // reactions
#define SC_TRANSPORT_CAP_TYPING         (1u << 3)  // typing indicator
#define SC_TRANSPORT_CAP_EDIT           (1u << 4)  // edit message
#define SC_TRANSPORT_CAP_DELETE         (1u << 5)  // delete message
#define SC_TRANSPORT_CAP_READ_RECEIPT   (1u << 6)  // read receipts
#define SC_TRANSPORT_CAP_THREADS        (1u << 7)  // topics and threads
#define SC_TRANSPORT_CAP_VOICE          (1u << 8)  // voice channels

// transport descriptor
typedef struct sc_transport_info {
    const char* name;          // matching ev.transport
    const char* display_name;  // label for dashboard
    const char* description;   // optional description
    uint32_t    capabilities;  // SC_TRANSPORT_CAP_*
    uint32_t    max_text_len;  // 0 = unlimited, core splits longer text
    uint32_t    flags;         // reserved, 0
    uint32_t    _reserved[3];

    sc_status_t (SC_CALL *send_text)(const char* payload_json, char** out_json);
    sc_status_t (SC_CALL *send_media)(const char* payload_json, char** out_json);
    sc_status_t (SC_CALL *react)(const char* payload_json, char** out_json);
    sc_status_t (SC_CALL *mark_read)(const char* payload_json, char** out_json);
    sc_status_t (SC_CALL *typing)(const char* payload_json, char** out_json);
    sc_status_t (SC_CALL *edit_text)(const char* payload_json, char** out_json);
    sc_status_t (SC_CALL *delete_message)(const char* payload_json, char** out_json);
    sc_status_t (SC_CALL *poll)(const char* payload_json, char** out_json);

    uint32_t _reserved2[4];
} sc_transport_info_t;

// config_ensure result codes
#define SC_CONFIG_EXISTS        0
#define SC_CONFIG_CREATED       1
#define SC_CONFIG_APPENDED      2
#define SC_CONFIG_ERR          -1

// forward declarations
typedef struct sc_core_api   sc_core_api_t;
typedef struct sc_plugin_api sc_plugin_api_t;

typedef sc_status_t (SC_CALL *sc_hook_fn)(
    void*       userdata,
    const char* name,
    const char* payload_json,
    char**      out_json);

typedef struct sc_hook_reg {
    const char* name;
    sc_hook_fn  fn;
    void*       userdata;
    uint32_t    priority;
    uint32_t    _reserved;
} sc_hook_reg_t;

typedef struct sc_syscall_reg {
    const char* prefix;
    sc_status_t (SC_CALL *fn)(const char* method, const char* args_json, char** out_json);
} sc_syscall_reg_t;

typedef struct sc_health_result {
    sc_status_t status;
    uint32_t    severity;
    const char* message;
} sc_health_result_t;

// core api (host -> plugin)
struct sc_core_api {
    // abi 1.0
    uint32_t abi_major;
    uint32_t abi_minor;
    uint32_t struct_size;

    void* (SC_CALL *alloc)(size_t size);
    void   (SC_CALL *free)(void* ptr);

    void   (SC_CALL *log)(int level, const char* name, const char* msg);

    sc_status_t (SC_CALL *emit_event)(const char* event_json);

    uint64_t     (SC_CALL *set_alarm)(uint32_t after_seconds, const char* payload_json);
    sc_status_t  (SC_CALL *cancel_alarm)(uint64_t handle);

    sc_status_t  (SC_CALL *call_tool)(const char* name, const char* args_json, char** out_json);
    sc_status_t  (SC_CALL *syscall)(const char* method, const char* args_json, char** out_json);

    // abi 1.1
    int (SC_CALL *prompt_yes_no)(const char* question, int default_value);
    int (SC_CALL *prompt_text)(const char* question, const char* default_value,
                               char* out, size_t outsz);
    int (SC_CALL *prompt_select)(const char* question, const char* const* options,
                                 int count, int default_index);

    // abi 1.3
    const char* (SC_CALL *config_get_string)(const char* key, const char* default_value);
    int          (SC_CALL *config_get_int)(const char* key, int default_value);
    int          (SC_CALL *config_get_bool)(const char* key, int default_value);
    double       (SC_CALL *config_get_double)(const char* key, double default_value);

    // abi 1.4
    int      (SC_CALL *config_ensure)(const char* name, const char* defaults_json);
    int      (SC_CALL *is_plugin_loaded)(const char* name);
    uint64_t (SC_CALL *now_ms)(void);
    void     (SC_CALL *log_json)(int level, const char* name, const char* msg_json);

    // abi 1.5
    sc_status_t  (SC_CALL *emit_media)(uint32_t media_type, const char* payload_json);
    uint32_t     (SC_CALL *config_version)(void);
    int          (SC_CALL *is_shutdown_requested)(void);
    uint64_t     (SC_CALL *call_tool_async)(const char* tool_name, const char* args_json,
                                            sc_hook_fn callback_fn, void* callback_userdata);
    void         (SC_CALL *health_check)(uint32_t severity_level, const char* message);
    const char*  (SC_CALL *media_mime_type)(uint32_t media_type);
    sc_status_t  (SC_CALL *emit_media_ext)(uint32_t media_type, const char* format,
                                           const char* payload_json, uint32_t* out_event_id);
    sc_status_t  (SC_CALL *emit_custom_event)(const char* event_name, const char* payload_json);
    sc_status_t  (SC_CALL *register_media_format)(const char* format, const char* mime,
                                                  const char* description);
    const char*  (SC_CALL *config_section_json)(void);

    // abi 1.6
    sc_status_t (SC_CALL *ingest_event)(const char* event_json);
    uint32_t    (SC_CALL *config_reload)(void);
    const char* (SC_CALL *data_dir)(void);

    // abi 1.6.1: webui widgets
    sc_status_t (SC_CALL *widget_emit)(const char* id, const char* data_json);
    sc_status_t (SC_CALL *widget_notify)(const char* id, int level,
                                          const char* msg, uint32_t duration_ms);
    sc_status_t (SC_CALL *widget_state_set)(const char* id, const char* state_json);
    const char* (SC_CALL *widget_state_get)(const char* id);

    uint32_t    _reserved_1_6_1[6];

    // abi 1.7: transport status
    sc_status_t (SC_CALL *transport_status)(const char* name, int connected,
                                             const char* detail_json);

    uint32_t    _reserved_1_7[7];
};

// plugin descriptor
typedef struct sc_plugin_info {
    // abi 1.0
    const char* name;
    const char* version;
    const char* description;
    const char* capabilities;

    // abi 1.4
    uint32_t min_abi_major;
    uint32_t min_abi_minor;
    uint32_t flags;
    uint32_t _reserved;
    const char* requires_plugins;  // comma-separated required plugins

    // abi 1.5
    uint32_t plugin_config_version;
    uint32_t shutdown_timeout_ms;
    void (*health_update_fn)(const char* detail_json);
    void (*on_config_version_change)(void* userdata, uint32_t new_version);

    // abi 1.6
    const char* transport_name;
    void* on_config_version_change_ud;

    // abi 1.6.1
    uint32_t abi_patch;
    uint32_t _reserved_1_6_1[3];
} sc_plugin_info_t;

// plugin api (plugin -> host)
struct sc_plugin_api {
    // abi 1.0
    uint32_t abi_major;
    uint32_t abi_minor;
    uint32_t struct_size;

    const sc_plugin_info_t* (SC_CALL *get_info)(void);
    sc_status_t              (SC_CALL *init)(const sc_core_api_t* core, const char* config_json);
    sc_status_t              (SC_CALL *deinit)(void);
    sc_status_t              (SC_CALL *get_hooks)(const sc_hook_reg_t** out_hooks, size_t* count);
    sc_status_t              (SC_CALL *get_tools)(char** out_json);
    sc_status_t              (SC_CALL *call_tool)(const char* tool_name, const char* args_json, char** out_json);

    // abi 1.1
    sc_status_t (SC_CALL *on_startup)(void);

    // abi 1.2
    sc_status_t (SC_CALL *get_system_prompt)(char** out_text);

    // abi 1.4
    sc_status_t (SC_CALL *health)(char** out_json);
    sc_status_t (SC_CALL *on_config_reload)(const char* cfg_json);

    // abi 1.5
    sc_status_t (SC_CALL *health_check_enhanced)(char** out_json);
    sc_status_t (SC_CALL *on_shutdown)(void);
    sc_status_t (SC_CALL *config_version_sync)(const char* cfg_json);

    // abi 1.6
    sc_status_t (SC_CALL *get_syscalls)(const sc_syscall_reg_t** out, size_t* count);

    // abi 1.6.1: webui widgets
    sc_status_t (SC_CALL *get_widgets)(const sc_widget_info_t** out, size_t* count);

    // abi 1.7: pluggable transport
    const sc_transport_info_t* (SC_CALL *get_transport)(void);
};

// entrypoint
SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void);

typedef sc_core_api_t    core_api_t;
typedef sc_plugin_info_t plugin_info_t;
typedef sc_plugin_api_t  plugin_api_t;

#ifdef __cplusplus
}
#endif

#endif // SPERMACORE_PLUGIN_API_H
