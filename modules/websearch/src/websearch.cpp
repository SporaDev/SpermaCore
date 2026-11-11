/* websearch.cpp - SpermaCore addon: web search for the model.
 *
 * Tools:
 *   web_search(query, max_results) - DuckDuckGo HTML search.
 *   web_fetch(url, max_chars)      - page → readable text.
 *
 * Transport:
 *   Windows: WinHTTP (built-in).
 *   POSIX:   curl via popen().
 *
 * v3: DDG keeps changing class names. We still try the class-based
 * parsers (result__a / result-link) first - they give the cleanest
 * output - but if they return 0, we fall back to a semantic parser:
 * scan all <a href> with external URLs, skip navigation/service
 * links, use anchor text as title and nearby text as snippet.
 *
 * Both attempts dump their raw HTML into <data_dir>/last_search_*.html
 * so the upstream layout can be inspected offline if parsing breaks
 * again.
 */

#include "plugin_api.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
  #ifndef WIN32_LEAN_AND_MEAN
  #define WIN32_LEAN_AND_MEAN
  #endif
  #include <windows.h>
  #include <winhttp.h>
  #define sc_strcasecmp  _stricmp
  #define sc_strncasecmp _strnicmp
#else
  #include <strings.h>
  #define sc_strcasecmp  strcasecmp
  #define sc_strncasecmp strncasecmp
#endif

/* ================================================================= */
/* Tunables                                                           */
/* ================================================================= */

#define DEFAULT_SEARCH_RESULTS   5
#define MAX_SEARCH_RESULTS       20
#define DEFAULT_FETCH_CHARS      8000
#define MAX_FETCH_CHARS          100000
#define HTTP_TIMEOUT_MS          20000
#define MAX_URL_LEN              4096
#define MIN_TITLE_LEN            8
#define MAX_DUMP_BYTES           400000

#define SC_UA \
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " \
    "(KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

static const sc_core_api_t* g_core = nullptr;

/* ================================================================= */
/* URL encode / decode                                                */
/* ================================================================= */

static void url_encode(const char* in, char* out, size_t outsz) {
    static const char* hex = "0123456789ABCDEF";
    size_t j = 0;
    if (!in || outsz == 0) { if (outsz) out[0] = 0; return; }
    for (size_t i = 0; in[i] && j + 4 < outsz; i++) {
        unsigned char c = (unsigned char)in[i];
        if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
            (c >= '0' && c <= '9') ||
            c == '-' || c == '_' || c == '.' || c == '~') {
            out[j++] = (char)c;
        } else if (c == ' ') {
            out[j++] = '+';
        } else {
            out[j++] = '%';
            out[j++] = hex[c >> 4];
            out[j++] = hex[c & 0x0F];
        }
    }
    out[j] = 0;
}

static void url_decode(const char* in, char* out, size_t outsz) {
    size_t j = 0;
    if (!in || outsz == 0) { if (outsz) out[0] = 0; return; }
    for (size_t i = 0; in[i] && j + 1 < outsz; i++) {
        if (in[i] == '%' && in[i+1] && in[i+2]) {
            int hi = -1, lo = -1;
            char a = in[i+1], b = in[i+2];
            if      (a >= '0' && a <= '9') hi = a - '0';
            else if (a >= 'a' && a <= 'f') hi = a - 'a' + 10;
            else if (a >= 'A' && a <= 'F') hi = a - 'A' + 10;
            if      (b >= '0' && b <= '9') lo = b - '0';
            else if (b >= 'a' && b <= 'f') lo = b - 'a' + 10;
            else if (b >= 'A' && b <= 'F') lo = b - 'A' + 10;
            if (hi >= 0 && lo >= 0) {
                out[j++] = (char)((hi << 4) | lo);
                i += 2;
                continue;
            }
        }
        out[j++] = (in[i] == '+') ? ' ' : in[i];
    }
    out[j] = 0;
}

/* ================================================================= */
/* JSON helpers                                                       */
/* ================================================================= */

static int json_get_string(const char* json, const char* field,
                           char* out, size_t outsz) {
    if (!json || !field || !out || outsz == 0) return 0;
    out[0] = 0;
    char pattern[128];
    snprintf(pattern, sizeof(pattern), "\"%s\"", field);
    const char* p = strstr(json, pattern);
    if (!p) return 0;
    p += strlen(pattern);
    while (*p && *p != ':') p++;
    if (*p != ':') return 0;
    p++;
    while (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n') p++;
    if (*p != '"') return 0;
    p++;
    size_t j = 0;
    while (*p && *p != '"' && j + 1 < outsz) {
        if (*p == '\\' && p[1]) {
            p++;
            switch (*p) {
                case 'n':  out[j++] = '\n'; break;
                case 'r':  out[j++] = '\r'; break;
                case 't':  out[j++] = '\t'; break;
                case '"':  out[j++] = '"';  break;
                case '\\': out[j++] = '\\'; break;
                case '/':  out[j++] = '/';  break;
                case 'b':  out[j++] = '\b'; break;
                case 'f':  out[j++] = '\f'; break;
                case 'u': {
                    /* \uXXXX - decode 4 hex digits, encode as UTF-8. */
                    if (p[1] && p[2] && p[3] && p[4]) {
                        unsigned cp = 0;
                        int ok = 1;
                        for (int k = 1; k <= 4; k++) {
                            char h = p[k];
                            unsigned d;
                            if      (h >= '0' && h <= '9') d = (unsigned)(h - '0');
                            else if (h >= 'a' && h <= 'f') d = (unsigned)(h - 'a' + 10);
                            else if (h >= 'A' && h <= 'F') d = (unsigned)(h - 'A' + 10);
                            else { ok = 0; break; }
                            cp = (cp << 4) | d;
                        }
                        if (ok) {
                            /* UTF-8 encode (BMP only, no surrogates). */
                            if (cp < 0x80) {
                                out[j++] = (char)cp;
                            } else if (cp < 0x800) {
                                if (j + 2 < outsz) {
                                    out[j++] = (char)(0xC0 | (cp >> 6));
                                    out[j++] = (char)(0x80 | (cp & 0x3F));
                                }
                            } else {
                                if (j + 3 < outsz) {
                                    out[j++] = (char)(0xE0 | (cp >> 12));
                                    out[j++] = (char)(0x80 | ((cp >> 6) & 0x3F));
                                    out[j++] = (char)(0x80 | (cp & 0x3F));
                                }
                            }
                            p += 4;
                            break;
                        }
                    }
                    /* Malformed - keep as-is. */
                    out[j++] = 'u';
                    break;
                }
                default:   out[j++] = *p;   break;
            }
            p++;
        } else {
            out[j++] = *p++;
        }
    }
    out[j] = 0;
    return 1;
}

static int json_get_int(const char* json, const char* field, int default_value) {
    if (!json || !field) return default_value;
    char pattern[128];
    snprintf(pattern, sizeof(pattern), "\"%s\"", field);
    const char* p = strstr(json, pattern);
    if (!p) return default_value;
    p += strlen(pattern);
    while (*p && *p != ':') p++;
    if (*p != ':') return default_value;
    p++;
    while (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n') p++;
    if (*p == '"') p++;
    return atoi(p);
}

/* ================================================================= */
/* Growable string buffer                                             */
/* ================================================================= */

typedef struct {
    char*  data;
    size_t len;
    size_t cap;
} strbuf_t;

static int sb_init(strbuf_t* sb, size_t initial) {
    if (initial < 256) initial = 256;
    sb->data = (char*)malloc(initial);
    if (!sb->data) return 0;
    sb->data[0] = 0;
    sb->len = 0;
    sb->cap = initial;
    return 1;
}

static int sb_reserve(strbuf_t* sb, size_t extra) {
    if (sb->len + extra + 1 <= sb->cap) return 1;
    size_t nc = sb->cap ? sb->cap : 256;
    while (nc < sb->len + extra + 1) nc *= 2;
    char* nb = (char*)realloc(sb->data, nc);
    if (!nb) return 0;
    sb->data = nb;
    sb->cap = nc;
    return 1;
}

static int sb_append(strbuf_t* sb, const char* s, size_t n) {
    if (n == 0) return 1;
    if (!sb_reserve(sb, n)) return 0;
    memcpy(sb->data + sb->len, s, n);
    sb->len += n;
    sb->data[sb->len] = 0;
    return 1;
}

static int sb_puts(strbuf_t* sb, const char* s) {
    return sb_append(sb, s, strlen(s));
}

static void sb_reset(strbuf_t* sb) {
    sb->len = 0;
    if (sb->data) sb->data[0] = 0;
}

static void sb_free(strbuf_t* sb) {
    free(sb->data);
    sb->data = nullptr;
    sb->len = sb->cap = 0;
}

/* sb_json_string appends properly escaped quoted string to strbuf */
static int sb_json_string(strbuf_t* sb, const char* s, size_t len) {
    if (!s) { s = ""; len = 0; }
    if (!sb_append(sb, "\"", 1)) return 0;
    for (size_t i = 0; i < len; i++) {
        unsigned char c = (unsigned char)s[i];
        char tmp[8];
        const char* esc;
        size_t n;
        switch (c) {
            case '"':  esc = "\\\""; n = 2; break;
            case '\\': esc = "\\\\"; n = 2; break;
            case '\n': esc = "\\n";  n = 2; break;
            case '\r': esc = "\\r";  n = 2; break;
            case '\t': esc = "\\t";  n = 2; break;
            case '\b': esc = "\\b";  n = 2; break;
            case '\f': esc = "\\f";  n = 2; break;
            default:
                if (c < 0x20) {
                    n = (size_t)snprintf(tmp, sizeof(tmp), "\\u%04x", c);
                    esc = tmp;
                } else {
                    tmp[0] = (char)c;
                    n = 1;
                    esc = tmp;
                }
        }
        if (!sb_append(sb, esc, n)) return 0;
    }
    return sb_append(sb, "\"", 1);
}

/* ================================================================= */
/* HTTP                                                               */
/* ================================================================= */

#ifdef _WIN32

static wchar_t* to_wide_n(const char* s, int len) {
    int wlen = MultiByteToWideChar(CP_UTF8, 0, s, len, nullptr, 0);
    if (wlen <= 0) return nullptr;
    wchar_t* w = (wchar_t*)malloc((size_t)(wlen + 1) * sizeof(wchar_t));
    if (!w) return nullptr;
    MultiByteToWideChar(CP_UTF8, 0, s, len, w, wlen);
    w[wlen] = 0;
    return w;
}

static char* http_request(const char* url, const char* method,
                          const char* body, size_t* out_len) {
    if (out_len) *out_len = 0;
    if (!url) return nullptr;

    /* support both https and http schemes */
    int secure;
    const char* host_start;
    if (strncmp(url, "https://", 8) == 0) {
        secure = 1;
        host_start = url + 8;
    } else if (strncmp(url, "http://", 7) == 0) {
        secure = 0;
        host_start = url + 7;
    } else {
        return nullptr;
    }
    const char* path_start = strchr(host_start, '/');
    if (!path_start) path_start = host_start + strlen(host_start);

    size_t host_len = (size_t)(path_start - host_start);
    if (host_len == 0) return nullptr;

    wchar_t* w_host   = to_wide_n(host_start, (int)host_len);
    wchar_t* w_path   = to_wide_n(*path_start ? path_start : "/", -1);
    wchar_t* w_method = to_wide_n(method ? method : "GET", -1);
    if (!w_host || !w_path || !w_method) {
        free(w_host); free(w_path); free(w_method);
        return nullptr;
    }

    HINTERNET hSession = WinHttpOpen(L"SpermaCore-websearch/3.0",
        WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
        WINHTTP_NO_PROXY_NAME, WINHTTP_NO_PROXY_BYPASS, 0);
    if (!hSession) { free(w_host); free(w_path); free(w_method); return nullptr; }

    WinHttpSetTimeouts(hSession, HTTP_TIMEOUT_MS, HTTP_TIMEOUT_MS,
                       HTTP_TIMEOUT_MS, HTTP_TIMEOUT_MS);

    HINTERNET hConnect = WinHttpConnect(hSession, w_host,
        secure ? INTERNET_DEFAULT_HTTPS_PORT : INTERNET_DEFAULT_HTTP_PORT, 0);
    HINTERNET hRequest = hConnect
        ? WinHttpOpenRequest(hConnect, w_method, w_path, nullptr,
                             WINHTTP_NO_REFERER, WINHTTP_DEFAULT_ACCEPT_TYPES,
                             secure ? WINHTTP_FLAG_SECURE : 0)
        : nullptr;

    free(w_host); free(w_path); free(w_method);

    if (!hRequest) {
        if (hConnect) WinHttpCloseHandle(hConnect);
        WinHttpCloseHandle(hSession);
        return nullptr;
    }

    DWORD policy = WINHTTP_OPTION_REDIRECT_POLICY_ALWAYS;
    WinHttpSetOption(hRequest, WINHTTP_OPTION_REDIRECT_POLICY,
                     &policy, sizeof(policy));

    wchar_t hdrs[1024];
    if (body) {
        _snwprintf(hdrs, 1024,
            L"User-Agent: %hs\r\n"
            L"Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8\r\n"
            L"Accept-Language: en-US,en;q=0.9,ru;q=0.8\r\n"
            L"Referer: https://duckduckgo.com/\r\n"
            L"Content-Type: application/x-www-form-urlencoded\r\n",
            SC_UA);
    } else {
        _snwprintf(hdrs, 1024,
            L"User-Agent: %hs\r\n"
            L"Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8\r\n"
            L"Accept-Language: en-US,en;q=0.9,ru;q=0.8\r\n",
            SC_UA);
    }
    /* ensure null termination on truncation */
    hdrs[1023] = 0;
    WinHttpAddRequestHeaders(hRequest, hdrs, (DWORD)-1,
        WINHTTP_ADDREQ_FLAG_ADD | WINHTTP_ADDREQ_FLAG_REPLACE);

    char* result = nullptr;
    BOOL ok;
    if (body) {
        ok = WinHttpSendRequest(hRequest, WINHTTP_NO_ADDITIONAL_HEADERS, 0,
                                (LPVOID)body, (DWORD)strlen(body),
                                (DWORD)strlen(body), 0);
    } else {
        ok = WinHttpSendRequest(hRequest, WINHTTP_NO_ADDITIONAL_HEADERS, 0,
                                WINHTTP_NO_REQUEST_DATA, 0, 0, 0);
    }
    if (ok) ok = WinHttpReceiveResponse(hRequest, nullptr);

    if (ok) {
        size_t cap = 16384, len = 0;
        char* buf = (char*)malloc(cap);
        if (buf) {
            DWORD avail = 0;
            while (WinHttpQueryDataAvailable(hRequest, &avail) && avail > 0) {
                if (len + avail + 1 > cap) {
                    size_t nc = cap * 2;
                    while (nc < len + avail + 1) nc *= 2;
                    char* nb = (char*)realloc(buf, nc);
                    if (!nb) { free(buf); buf = nullptr; break; }
                    buf = nb;
                    cap = nc;
                }
                DWORD got = 0;
                if (!WinHttpReadData(hRequest, buf + len, avail, &got)) break;
                len += got;
            }
            if (buf) {
                buf[len] = 0;
                result = buf;
                if (out_len) *out_len = len;
            }
        }
    }

    WinHttpCloseHandle(hRequest);
    WinHttpCloseHandle(hConnect);
    WinHttpCloseHandle(hSession);
    return result;
}

#else

/* verify curl availability on posix platforms during init */
static int curl_available(void) {
    FILE* fp = popen("curl --version >/dev/null 2>&1", "r");
    if (!fp) return 0;
    return pclose(fp) == 0;
}

/* sh_quote wraps string in single quotes and escapes single quotes for popen */
static char* sh_quote(const char* s) {
    if (!s) return nullptr;
    size_t n = 2;
    for (const char* p = s; *p; p++) n += (*p == '\'') ? 4 : 1;
    char* out = (char*)malloc(n + 1);
    if (!out) return nullptr;
    char* o = out;
    *o++ = '\'';
    for (const char* p = s; *p; p++) {
        if (*p == '\'') {
            memcpy(o, "'\\''", 4);
            o += 4;
        } else {
            *o++ = *p;
        }
    }
    *o++ = '\'';
    *o = 0;
    return out;
}

static char* http_request(const char* url, const char* method,
                          const char* body, size_t* out_len) {
    (void)method;
    if (out_len) *out_len = 0;
    if (!url) return nullptr;

    char* qurl = sh_quote(url);
    char* qbody = body ? sh_quote(body) : nullptr;
    if (!qurl || (body && !qbody)) {
        free(qurl);
        free(qbody);
        return nullptr;
    }

    size_t need = strlen(qurl) + (qbody ? strlen(qbody) : 0) + 1024;
    char* cmd = (char*)malloc(need);
    if (!cmd) {
        free(qurl);
        free(qbody);
        return nullptr;
    }

    if (qbody) {
        snprintf(cmd, need,
            "curl -sS -L --compressed --max-time 20 "
            "-A '%s' "
            "-H 'Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8' "
            "-H 'Accept-Language: en-US,en;q=0.9,ru;q=0.8' "
            "-H 'Referer: https://duckduckgo.com/' "
            "-H 'Content-Type: application/x-www-form-urlencoded' "
            "--data-raw %s %s 2>/dev/null",
            SC_UA, qbody, qurl);
    } else {
        snprintf(cmd, need,
            "curl -sS -L --compressed --max-time 20 "
            "-A '%s' "
            "-H 'Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8' "
            "-H 'Accept-Language: en-US,en;q=0.9,ru;q=0.8' "
            "%s 2>/dev/null",
            SC_UA, qurl);
    }
    free(qurl);
    free(qbody);

    FILE* fp = popen(cmd, "r");
    free(cmd);
    if (!fp) return nullptr;

    size_t cap = 16384, len = 0;
    char* buf = (char*)malloc(cap);
    if (!buf) { pclose(fp); return nullptr; }

    size_t n;
    while ((n = fread(buf + len, 1, cap - len - 1, fp)) > 0) {
        len += n;
        if (len + 1 >= cap) {
            size_t nc = cap * 2;
            char* nb = (char*)realloc(buf, nc);
            if (!nb) { free(buf); pclose(fp); return nullptr; }
            buf = nb;
            cap = nc;
        }
    }
    buf[len] = 0;
    int status = pclose(fp);
    /* non-zero exit code indicates failure, discard partial output */
    if (status != 0) { free(buf); return nullptr; }
    if (out_len) *out_len = len;
    return buf;
}

#endif

static char* http_get(const char* url, size_t* out_len) {
    return http_request(url, "GET", nullptr, out_len);
}

static char* http_post(const char* url, const char* body, size_t* out_len) {
    return http_request(url, "POST", body, out_len);
}

/* ================================================================= */
/* Diagnostics: log head, dump raw HTML                               */
/* ================================================================= */

static void log_response_head(const char* tag, const char* body, size_t len) {
    if (!g_core || !g_core->log) return;
    char* dbg = (char*)malloc(1024);
    if (!dbg) return;
    char head[420];
    size_t hn = len < 400 ? len : 400;
    for (size_t i = 0; i < hn; i++) {
        char c = body ? body[i] : 0;
        head[i] = (c == '\n' || c == '\r' || c == '\t') ? ' ' : c;
    }
    head[hn] = 0;
    snprintf(dbg, 1024, "[%s] len=%llu head=%s", tag, (unsigned long long)len, head);
    g_core->log(SC_LOG_WARN, "websearch", dbg);
    free(dbg);
}

/* Save raw HTML into <data_dir>/<file>. Uses core->data_dir which is
 * available for the plugin throughout its lifetime.
 *
 * rotation: dump files use fixed slots and get overwritten on failure.
 * housekeeping purges stale dumps after 24h.
 */
static void dump_raw(const char* filename, const char* body, size_t len) {
    if (!g_core || !g_core->data_dir) return;
    /* free allocated data_dir path */
    char* dir_raw = (char*)g_core->data_dir();
    if (!dir_raw) return;

    size_t need = strlen(dir_raw) + strlen(filename) + 4;
    char* path = (char*)malloc(need);
    if (!path) {
        if (g_core->free) g_core->free(dir_raw);
        return;
    }
#ifdef _WIN32
    snprintf(path, need, "%s\\%s", dir_raw, filename);
#else
    snprintf(path, need, "%s/%s", dir_raw, filename);
#endif
    if (g_core->free) g_core->free(dir_raw);
    FILE* f = fopen(path, "wb");
    free(path);
    if (!f) return;

    size_t w = len < MAX_DUMP_BYTES ? len : MAX_DUMP_BYTES;
    fwrite(body, 1, w, f);
    fclose(f);
}

/* ================================================================= */
/* URL safety                                                         */
/* ================================================================= */

static int url_is_safe(const char* url) {
    const char* p = url;
    if      (strncmp(p, "http://",  7) == 0) p += 7;
    else if (strncmp(p, "https://", 8) == 0) p += 8;
    else return 0;

    const char* end = p;
    while (*end && *end != '/' && *end != ':' && *end != '?' && *end != '#') end++;
    size_t hlen = (size_t)(end - p);
    if (hlen == 0 || hlen > 255) return 0;

    char host[256];
    memcpy(host, p, hlen);
    host[hlen] = 0;

    /* strip trailing dot in fqdn hostnames */
    size_t hl = strlen(host);
    while (hl > 0 && host[hl - 1] == '.') host[--hl] = 0;

    if (sc_strcasecmp(host, "localhost") == 0) return 0;
    if (strcmp(host, "0.0.0.0")   == 0) return 0;
    if (strcmp(host, "::1")       == 0) return 0;
    if (strcmp(host, "[::1]")     == 0) return 0;

    /* reject private and loopback ipv6 literals */
    if (host[0] == '[') {
        if (sc_strncasecmp(host, "[fe80:", 6) == 0) return 0;
        if (sc_strncasecmp(host, "[fc", 3) == 0) return 0;
        if (sc_strncasecmp(host, "[fd", 3) == 0) return 0;
        if (strstr(host, "::ffff:127.")) return 0;
        if (strstr(host, "::1]")) return 0;
    }

    /* reject numeric decimal ip addresses (e.g. 2130706433) */
    int digits_only = 1;
    for (size_t i = 0; i < hl; i++) {
        if (host[i] < '0' || host[i] > '9') { digits_only = 0; break; }
    }
    if (digits_only && hl > 0) return 0;

    unsigned int a=0,b=0,c=0,d=0;
    if (sscanf(host, "%u.%u.%u.%u", &a,&b,&c,&d) == 4) {
        if (a == 0 || a == 127)                    return 0;
        if (a == 10)                               return 0;
        if (a == 172 && b >= 16 && b <= 31)        return 0;
        if (a == 192 && b == 168)                  return 0;
        if (a == 169 && b == 254)                  return 0;
    }
    return 1;
}

/* ================================================================= */
/* HTML helpers                                                       */
/* ================================================================= */

typedef struct {
    char title[512];
    char url[MAX_URL_LEN];
    char snippet[1024];
} search_result_t;

static void strip_html(const char* in, size_t in_len,
                       char* out, size_t outsz) {
    size_t j = 0;
    int in_tag = 0;
    for (size_t i = 0; i < in_len && j + 1 < outsz; i++) {
        char c = in[i];
        if (in_tag) { if (c == '>') in_tag = 0; continue; }
        if (c == '<') { in_tag = 1; continue; }
        if (c == '&') {
            if (i + 5 <= in_len && strncmp(in+i, "&amp;",  5) == 0) { out[j++]='&';  i+=4; continue; }
            if (i + 4 <= in_len && strncmp(in+i, "&lt;",   4) == 0) { out[j++]='<';  i+=3; continue; }
            if (i + 4 <= in_len && strncmp(in+i, "&gt;",   4) == 0) { out[j++]='>';  i+=3; continue; }
            if (i + 6 <= in_len && strncmp(in+i, "&quot;", 6) == 0) { out[j++]='"';  i+=5; continue; }
            if (i + 6 <= in_len && strncmp(in+i, "&#x27;", 6) == 0) { out[j++]='\''; i+=5; continue; }
            if (i + 5 <= in_len && strncmp(in+i, "&#39;",  5) == 0) { out[j++]='\''; i+=4; continue; }
            if (i + 6 <= in_len && strncmp(in+i, "&nbsp;", 6) == 0) { out[j++]=' ';  i+=5; continue; }
        }
        if (c == '\n' || c == '\r' || c == '\t') c = ' ';
        out[j++] = c;
    }
    while (j > 0 && out[j-1] == ' ') j--;
    out[j] = 0;
    size_t s = 0;
    while (s < j && out[s] == ' ') s++;
    if (s > 0) memmove(out, out + s, j - s + 1);
}

static void decode_ddg_url(const char* raw, char* out, size_t outsz) {
    /* DDG redirect: //duckduckgo.com/l/?uddg=<enc>&rut=… */
    const char* uddg = strstr(raw, "uddg=");
    if (!uddg) {
        /* Might be plain http(s) URL. */
        snprintf(out, outsz, "%s", raw);
        return;
    }
    uddg += 5;
    const char* amp = strchr(uddg, '&');
    size_t elen = amp ? (size_t)(amp - uddg) : strlen(uddg);
    char enc[4096];
    if (elen >= sizeof(enc)) elen = sizeof(enc) - 1;
    memcpy(enc, uddg, elen);
    enc[elen] = 0;
    url_decode(enc, out, outsz);
}

/* ================================================================= */
/* Semantic parser (fallback)                                         */
/* ================================================================= */

/* Not-DDG, not-navigation URLs that are obviously external content. */
static int is_content_url(const char* url) {
    if (!url || !*url) return 0;
    if (strncmp(url, "http://",  7) != 0 &&
        strncmp(url, "https://", 8) != 0) return 0;
    /* Skip obvious DDG self-references (search links, ads, y.js). */
    if (strstr(url, "duckduckgo.com/y.js")) return 0;
    if (strstr(url, "duckduckgo.com/settings")) return 0;
    if (strstr(url, "duckduckgo.com/params")) return 0;
    if (strstr(url, "duckduckgo.com/?q=")) return 0;
    if (strstr(url, "duckduckgo.com/html/?q=")) return 0;
    if (strstr(url, "duckduckgo.com/about")) return 0;
    if (strstr(url, "duckduckgo.com/privacy")) return 0;
    if (strstr(url, "duckduckgo.com/feedback")) return 0;
    return 1;
}

/* Extract href="…" from an <a …> tag. Returns 1 on success. */
static int extract_href(const char* a_start, const char* tag_close,
                        char* out, size_t outsz) {
    const char* href = strstr(a_start, "href=");
    if (!href || href > tag_close) return 0;
    href += 5;
    while (*href == ' ') href++;
    char q = *href;
    if (q != '"' && q != '\'') return 0;
    href++;
    const char* end = strchr(href, q);
    if (!end || end > tag_close) return 0;

    size_t len = (size_t)(end - href);
    if (len >= outsz) len = outsz - 1;
    memcpy(out, href, len);
    out[len] = 0;
    return 1;
}

/* Try to find a snippet block near `t_end` (the closing </a>).
 * Looks for common snippet containers first; if none, takes next
 * chunk of text between tags. */
static void find_snippet_near(const char* t_end, const char* html_end,
                              char* out, size_t outsz) {
    out[0] = 0;

    /* Look for common snippet containers within next 4000 chars. */
    static const char* classes[] = {
        "result__snippet",
        "result-snippet",
        "result__body",
        nullptr
    };
    const char* window = t_end;
    size_t wmax = 4000;
    size_t wlen = (size_t)(html_end - window);
    if (wlen > wmax) wlen = wmax;

    for (int k = 0; classes[k]; k++) {
        const char* hit = strstr(window, classes[k]);
        if (hit && (size_t)(hit - window) < wlen) {
            const char* s_close = strchr(hit, '>');
            if (s_close) {
                const char* s_text = s_close + 1;
                /* End at next closing of the containing element. */
                const char* s_end = strstr(s_text, "</a>");
                const char* p1 = strstr(s_text, "</td>");
                const char* p2 = strstr(s_text, "</div>");
                const char* p3 = strstr(s_text, "</span>");
                const char* best = s_end;
                if (p1 && (!best || p1 < best)) best = p1;
                if (p2 && (!best || p2 < best)) best = p2;
                if (p3 && (!best || p3 < best)) best = p3;
                if (best && best > s_text && (size_t)(best - s_text) < 2000) {
                    strip_html(s_text, (size_t)(best - s_text), out, outsz);
                    if (out[0]) return;
                }
            }
        }
    }

    /* Fallback: take next plain text run after </a> up to ~300 chars. */
    const char* p = t_end;
    size_t cap = 300;
    if ((size_t)(html_end - p) < cap) cap = (size_t)(html_end - p);
    /* Skip leading tags/whitespace. */
    while (p < html_end && (size_t)(p - t_end) < 600) {
        if (*p == '<') {
            /* Skip the whole tag. */
            const char* gt = strchr(p, '>');
            if (!gt) break;
            p = gt + 1;
        } else if (*p == ' ' || *p == '\n' || *p == '\r' || *p == '\t') {
            p++;
        } else {
            break;
        }
    }
    if (p < html_end) {
        const char* run_end = p;
        size_t taken = 0;
        while (run_end < html_end && taken < cap && *run_end != '<') {
            run_end++;
            taken++;
        }
        if (run_end > p) {
            strip_html(p, (size_t)(run_end - p), out, outsz);
        }
    }
}

/* Semantic parser: walk <a> tags, keep external ones whose anchor
 * text looks like a title. */
static int parse_generic(const char* html, size_t html_len,
                         search_result_t* results, int max_results) {
    int count = 0;
    const char* p = html;
    const char* html_end = html + html_len;

    while (count < max_results && p < html_end) {
        const char* a = strstr(p, "<a ");
        if (!a) break;
        const char* tag_close = strchr(a, '>');
        if (!tag_close) break;

        char raw_href[4096];
        if (!extract_href(a, tag_close, raw_href, sizeof(raw_href))) {
            p = tag_close + 1;
            continue;
        }

        char url[MAX_URL_LEN];
        if (strstr(raw_href, "uddg=")) {
            decode_ddg_url(raw_href, url, sizeof(url));
        } else {
            snprintf(url, sizeof(url), "%s", raw_href);
        }

        if (!is_content_url(url)) {
            p = tag_close + 1;
            continue;
        }

        /* Anchor text = title. */
        const char* t_start = tag_close + 1;
        const char* t_end = strstr(t_start, "</a>");
        if (!t_end) { p = tag_close + 1; continue; }

        char title[512];
        strip_html(t_start, (size_t)(t_end - t_start), title, sizeof(title));
        if (strlen(title) < MIN_TITLE_LEN) {
            p = t_end + 4;
            continue;
        }

        /* Skip clearly navigation-ish titles. */
        if (sc_strcasecmp(title, "More results") == 0 ||
            sc_strcasecmp(title, "Next")          == 0 ||
            sc_strcasecmp(title, "Previous")      == 0 ||
            sc_strcasecmp(title, "Settings")      == 0 ||
            sc_strcasecmp(title, "Feedback")      == 0) {
            p = t_end + 4;
            continue;
        }

        snprintf(results[count].title, sizeof(results[count].title),
                 "%s", title);
        snprintf(results[count].url, sizeof(results[count].url),
                 "%s", url);
        find_snippet_near(t_end + 4, html_end,
                          results[count].snippet,
                          sizeof(results[count].snippet));

        count++;
        p = t_end + 4;
    }
    return count;
}

/* ================================================================= */
/* Class-based parsers (primary, fast path)                           */
/* ================================================================= */

static int parse_class_based(const char* html,
                             const char* class_marker,
                             const char* snippet_marker,
                             search_result_t* results, int max_results) {
    int count = 0;
    const char* p = html;

    while (count < max_results && p && *p) {
        const char* ra = strstr(p, class_marker);
        if (!ra) break;

        const char* a = ra;
        while (a > html && a[-1] != '<') a--;
        if (a[0] != '<' || (a[1] != 'a' && a[1] != 'A')) {
            p = ra + strlen(class_marker);
            continue;
        }
        const char* tag_close = strchr(a, '>');
        if (!tag_close) break;

        char raw_href[4096];
        if (!extract_href(a, tag_close, raw_href, sizeof(raw_href))) {
            p = tag_close + 1; continue;
        }

        char url[MAX_URL_LEN];
        if (strstr(raw_href, "uddg=")) {
            decode_ddg_url(raw_href, url, sizeof(url));
        } else {
            snprintf(url, sizeof(url), "%s", raw_href);
        }

        const char* t_start = tag_close + 1;
        const char* t_end = strstr(t_start, "</a>");
        if (!t_end) { p = tag_close + 1; continue; }

        strip_html(t_start, (size_t)(t_end - t_start),
                   results[count].title, sizeof(results[count].title));
        snprintf(results[count].url, sizeof(results[count].url),
                 "%s", url);

        results[count].snippet[0] = 0;
        if (snippet_marker) {
            const char* sn = strstr(t_end, snippet_marker);
            if (sn) {
                const char* s_close = strchr(sn, '>');
                if (s_close) {
                    const char* s_text = s_close + 1;
                    const char* s_end = strstr(s_text, "</a>");
                    const char* p1 = strstr(s_text, "</td>");
                    const char* p2 = strstr(s_text, "</div>");
                    const char* best = s_end;
                    if (p1 && (!best || p1 < best)) best = p1;
                    if (p2 && (!best || p2 < best)) best = p2;
                    if (best && best > s_text) {
                        strip_html(s_text, (size_t)(best - s_text),
                                   results[count].snippet,
                                   sizeof(results[count].snippet));
                    }
                }
            }
        }

        count++;
        p = t_end + 4;
    }
    return count;
}

/* Dedup by URL, returns new count. */
static int dedup_results(search_result_t* r, int n) {
    for (int i = 0; i < n; i++) {
        if (!r[i].url[0]) continue;
        for (int j = i + 1; j < n; ) {
            if (strcmp(r[i].url, r[j].url) == 0) {
                memmove(&r[j], &r[j+1],
                        (size_t)(n - j - 1) * sizeof(search_result_t));
                memset(&r[n-1], 0, sizeof(search_result_t));
                n--;
            } else j++;
        }
    }
    /* Compact out empties. */
    int k = 0;
    for (int i = 0; i < n; i++) {
        if (r[i].url[0]) {
            if (k != i) r[k] = r[i];
            k++;
        }
    }
    return k;
}

/* ================================================================= */
/* Search orchestration                                               */
/* ================================================================= */

static int try_parse(const char* tag, const char* html, size_t len,
                     search_result_t* results, int max_results,
                     const char* dump_name) {
    /* 1) Class-based, HTML full version. */
    int n = parse_class_based(html, "result__a", "result__snippet",
                              results, max_results);
    if (n == 0) {
        /* 2) Class-based, lite version. */
        n = parse_class_based(html, "result-link", "result-snippet",
                              results, max_results);
    }
    if (n == 0) {
        /* 3) Generic semantic parser. */
        n = parse_generic(html, len, results, max_results);
    }
    if (n > 0) n = dedup_results(results, n);

    if (n == 0) {
        log_response_head(tag, html, len);
        if (dump_name) dump_raw(dump_name, html, len);
    }
    return n;
}

static int do_search(const char* query, int max_results,
                     search_result_t* results) {
    if (!query || !*query) return 0;

    char enc[2048];
    url_encode(query, enc, sizeof(enc));

    int n = 0;

    /* ---- 1) POST html.duckduckgo.com/html/ ---- */
    {
        char body[2200];
        snprintf(body, sizeof(body), "q=%s", enc);
        size_t len = 0;
        char* html = http_post("https://html.duckduckgo.com/html/",
                               body, &len);
        if (html) {
            n = try_parse("ddg-html-post", html, len, results, max_results,
                          "last_search_html_post.html");
            free(html);
            if (n > 0) return n;
        } else if (g_core && g_core->log) {
            g_core->log(SC_LOG_WARN, "websearch",
                        "ddg-html-post: HTTP request failed");
        }
    }

    /* ---- 2) GET lite.duckduckgo.com/lite/ ---- */
    {
        char url[2400];
        snprintf(url, sizeof(url),
                 "https://lite.duckduckgo.com/lite/?q=%s", enc);
        size_t len = 0;
        char* html = http_get(url, &len);
        if (html) {
            n = try_parse("ddg-lite-get", html, len, results, max_results,
                          "last_search_lite_get.html");
            free(html);
            if (n > 0) return n;
        } else if (g_core && g_core->log) {
            g_core->log(SC_LOG_WARN, "websearch",
                        "ddg-lite-get: HTTP request failed");
        }
    }

    return -1;
}

/* ================================================================= */
/* HTML → readable text (for web_fetch)                               */
/* ================================================================= */

static void html_to_text(const char* html, size_t html_len,
                         strbuf_t* sb, size_t max_chars) {
    size_t i = 0;
    int in_tag = 0, in_script = 0, in_style = 0;

    while (i < html_len && sb->len < max_chars) {
        if (in_script) {
            if (i + 8 <= html_len && sc_strncasecmp(html + i, "</script", 8) == 0) {
                in_script = 0;
                while (i < html_len && html[i] != '>') i++;
                if (i < html_len) i++;
            } else i++;
            continue;
        }
        if (in_style) {
            if (i + 7 <= html_len && sc_strncasecmp(html + i, "</style", 7) == 0) {
                in_style = 0;
                while (i < html_len && html[i] != '>') i++;
                if (i < html_len) i++;
            } else i++;
            continue;
        }
        if (in_tag) {
            if (html[i] == '>') in_tag = 0;
            i++;
            continue;
        }
        if (html[i] == '<') {
            if (i + 7 <= html_len && sc_strncasecmp(html + i, "<script", 7) == 0) {
                in_script = 1; i += 7; continue;
            }
            if (i + 6 <= html_len && sc_strncasecmp(html + i, "<style", 6) == 0) {
                in_style = 1; i += 6; continue;
            }
            in_tag = 1; i++; continue;
        }
        if (html[i] == '&') {
            struct { const char* e; const char* r; } ents[] = {
                {"&amp;","&"}, {"&lt;","<"}, {"&gt;",">"},
                {"&quot;","\""}, {"&#39;","'"}, {"&nbsp;"," "},
                {nullptr,nullptr}
            };
            int matched = 0;
            for (int k = 0; ents[k].e; k++) {
                size_t el = strlen(ents[k].e);
                if (i + el <= html_len && strncmp(html + i, ents[k].e, el) == 0) {
                    sb_puts(sb, ents[k].r);
                    i += el;
                    matched = 1;
                    break;
                }
            }
            if (matched) continue;
        }
        char c = html[i];
        if (c == '\n' || c == '\r' || c == '\t' || c == ' ') {
            if (sb->len > 0 && sb->data[sb->len-1] != ' ') sb_puts(sb, " ");
            i++;
            continue;
        }
        sb_append(sb, &c, 1);
        i++;
    }
}

/* ================================================================= */
/* Plugin ABI                                                         */
/* ================================================================= */

static SC_THREAD_LOCAL strbuf_t g_out;
static SC_THREAD_LOCAL int       g_out_ready = 0;

static strbuf_t* out_reset(void) {
    if (!g_out_ready) {
        if (!sb_init(&g_out, 8192)) return nullptr;
        g_out_ready = 1;
    }
    sb_reset(&g_out);
    return &g_out;
}

static const sc_plugin_info_t* get_info(void) {
    static const sc_plugin_info_t info = {
        .name = "websearch",
        .version = "3.0.0",
        .description = "Web search (DuckDuckGo) and page fetch for the model",
        .capabilities = nullptr,
    };
    return &info;
}

static sc_status_t init(const sc_core_api_t* api, const char* cfg) {
    g_core = api;
    (void)cfg;
    if (!g_core) return SC_ERR_INVALID_ARG;
#ifndef _WIN32
    if (!curl_available()) {
        if (g_core->log) {
            g_core->log(SC_LOG_ERROR, "websearch",
                        "POSIX transport requires curl; install curl or use the WinHTTP build");
        }
        return SC_ERR_NOT_IMPLEMENTED;
    }
#endif
    if (g_core->log) {
        g_core->log(SC_LOG_INFO, "websearch", "websearch plugin initialized");
    }
    return SC_OK;
}

static sc_status_t deinit(void) {
    g_core = nullptr;
    return SC_OK;
}

static sc_status_t get_hooks(const sc_hook_reg_t** out_hooks, size_t* count) {
    if (out_hooks) *out_hooks = nullptr;
    if (count)     *count = 0;
    return SC_OK;
}

static sc_status_t get_tools(char** out) {
    if (!out) return SC_ERR_INVALID_ARG;
    static const char* tools =
        "["
          "{\"name\":\"web_search\","
          "\"description\":\"Search the web via DuckDuckGo. Returns a list of "
          "results, each with title, url and snippet. Use this whenever you need "
          "up-to-date information, current events, or facts you are unsure about.\","
          "\"parameters\":{\"type\":\"object\","
            "\"properties\":{"
              "\"query\":{\"type\":\"string\","
                "\"description\":\"Natural language search query\"},"
              "\"max_results\":{\"type\":\"integer\","
                "\"description\":\"How many results to return (1-20, default 5)\"}"
            "},"
            "\"required\":[\"query\"]"
          "}},"
          "{\"name\":\"web_fetch\","
          "\"description\":\"Fetch a web page and return its readable text "
          "(scripts, styles and HTML tags stripped). Use after web_search to "
          "read the content of a specific result.\","
          "\"parameters\":{\"type\":\"object\","
            "\"properties\":{"
              "\"url\":{\"type\":\"string\","
                "\"description\":\"Absolute http(s) URL to fetch\"},"
              "\"max_chars\":{\"type\":\"integer\","
                "\"description\":\"Max characters of text to return (default 8000)\"}"
            "},"
            "\"required\":[\"url\"]"
          "}}"
        "]";
    *out = (char*)tools;
    return SC_OK;
}

static sc_status_t tool_web_search(const char* args, strbuf_t* sb) {
    char query[1024];
    if (!json_get_string(args, "query", query, sizeof(query)) || !query[0])
        return SC_ERR_INVALID_ARG;

    int max_results = json_get_int(args, "max_results", DEFAULT_SEARCH_RESULTS);
    if (max_results < 1) max_results = 1;
    if (max_results > MAX_SEARCH_RESULTS) max_results = MAX_SEARCH_RESULTS;

    search_result_t* results =
        (search_result_t*)calloc((size_t)max_results, sizeof(search_result_t));
    if (!results) return SC_ERR_NO_MEMORY;

    int n = do_search(query, max_results, results);

    sb_puts(sb, "{\"query\":");
    sb_json_string(sb, query, strlen(query));

    if (n < 0) {
        sb_puts(sb, ",\"error\":\"search failed (network or upstream block)\","
                    "\"count\":0,\"results\":[]}");
        free(results);
        return SC_OK;
    }

    char tmp[64];
    int k = snprintf(tmp, sizeof(tmp), ",\"count\":%d,\"results\":[", n);
    sb_append(sb, tmp, (size_t)k);

    for (int i = 0; i < n; i++) {
        if (i > 0) sb_puts(sb, ",");
        sb_puts(sb, "{\"title\":");
        sb_json_string(sb, results[i].title, strlen(results[i].title));
        sb_puts(sb, ",\"url\":");
        sb_json_string(sb, results[i].url, strlen(results[i].url));
        sb_puts(sb, ",\"snippet\":");
        sb_json_string(sb, results[i].snippet, strlen(results[i].snippet));
        sb_puts(sb, "}");
    }
    sb_puts(sb, "]}");

    free(results);
    return SC_OK;
}

static sc_status_t tool_web_fetch(const char* args, strbuf_t* sb) {
    char url[MAX_URL_LEN];
    if (!json_get_string(args, "url", url, sizeof(url)) || !url[0])
        return SC_ERR_INVALID_ARG;

    if (!url_is_safe(url)) {
        sb_puts(sb, "{\"error\":\"url is not allowed\"}");
        return SC_OK;
    }

    int max_chars = json_get_int(args, "max_chars", DEFAULT_FETCH_CHARS);
    if (max_chars < 100) max_chars = 100;
    if (max_chars > MAX_FETCH_CHARS) max_chars = MAX_FETCH_CHARS;

    size_t html_len = 0;
    char* html = http_get(url, &html_len);
    if (!html) {
        sb_puts(sb, "{\"error\":\"network request failed\"}");
        return SC_OK;
    }

    strbuf_t text;
    if (!sb_init(&text, 8192)) { free(html); return SC_ERR_NO_MEMORY; }
    html_to_text(html, html_len, &text, (size_t)max_chars);
    free(html);

    char tmp[64];
    sb_puts(sb, "{\"url\":");
    sb_json_string(sb, url, strlen(url));
    sb_puts(sb, ",\"length\":");
    int k = snprintf(tmp, sizeof(tmp), "%llu", (unsigned long long)text.len);
    sb_append(sb, tmp, (size_t)k);
    sb_puts(sb, ",\"text\":");
    sb_json_string(sb, text.data ? text.data : "", text.data ? text.len : 0);
    sb_puts(sb, "}");

    sb_free(&text);
    return SC_OK;
}

static sc_status_t call_tool(const char* name, const char* args, char** out) {
    if (!name || !out) return SC_ERR_INVALID_ARG;
    *out = nullptr;

    strbuf_t* sb = out_reset();
    if (!sb) return SC_ERR_NO_MEMORY;

    sc_status_t rc;
    if      (strcmp(name, "web_search") == 0) rc = tool_web_search(args, sb);
    else if (strcmp(name, "web_fetch")  == 0) rc = tool_web_fetch(args, sb);
    else                                      return SC_ERR_NOT_FOUND;

    if (rc != SC_OK) return rc;

    *out = sb->data;
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
};

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    return &g_api;
}