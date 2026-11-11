/* json.hpp - minimal JSON DOM parser and serializer.
 *
 * supports objects, arrays, strings (including \uXXXX and surrogate pairs),
 * numbers, and boolean/null literals.
 */
#ifndef SPERMACORE_TELEGRAM_JSON_HPP
#define SPERMACORE_TELEGRAM_JSON_HPP

#include <cstdint>
#include <cstdio>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace scjson {

enum class Type { Null, Bool, Number, String, Array, Object };

struct Value {
    Type type = Type::Null;
    bool boolean = false;
    bool is_integer = false;
    long long integer = 0;
    double number = 0.0;
    std::string string;
    std::vector<Value> array;
    std::vector<std::pair<std::string, Value>> object;

    bool is_null() const { return type == Type::Null; }
    bool is_object() const { return type == Type::Object; }
    bool is_array() const { return type == Type::Array; }

    const Value* get(std::string_view key) const {
        if (type != Type::Object) return nullptr;
        for (const auto& kv : object) {
            if (kv.first == key) return &kv.second;
        }
        return nullptr;
    }

    Value& set(std::string key, Value value) {
        type = Type::Object;
        object.emplace_back(std::move(key), std::move(value));
        return object.back().second;
    }

    void push(Value value) {
        type = Type::Array;
        array.push_back(std::move(value));
    }
};

inline Value make_string(std::string s) {
    Value v;
    v.type = Type::String;
    v.string = std::move(s);
    return v;
}

inline Value make_int(long long n) {
    Value v;
    v.type = Type::Number;
    v.is_integer = true;
    v.integer = n;
    v.number = static_cast<double>(n);
    return v;
}

inline Value make_bool(bool b) {
    Value v;
    v.type = Type::Bool;
    v.boolean = b;
    return v;
}

inline Value make_object() {
    Value v;
    v.type = Type::Object;
    return v;
}

inline Value make_array() {
    Value v;
    v.type = Type::Array;
    return v;
}

// parsing

class Parser {
public:
    explicit Parser(std::string_view text) : s_(text) {}

    bool parse(Value& out) {
        skip_ws();
        if (!parse_value(out)) return false;
        skip_ws();
        return pos_ == s_.size();
    }

    const std::string& error() const { return error_; }

private:
    std::string_view s_;
    size_t pos_ = 0;
    std::string error_;

    bool fail(const char* msg) {
        if (error_.empty()) error_ = msg;
        return false;
    }

    void skip_ws() {
        while (pos_ < s_.size()) {
            char c = s_[pos_];
            if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
                pos_++;
            } else {
                break;
            }
        }
    }

    bool parse_value(Value& out) {
        if (pos_ >= s_.size()) return fail("unexpected end");
        char c = s_[pos_];
        switch (c) {
            case '{': return parse_object(out);
            case '[': return parse_array(out);
            case '"':
                out.type = Type::String;
                return parse_string(out.string);
            case 't':
                if (s_.substr(pos_, 4) != "true") return fail("bad true");
                pos_ += 4;
                out = make_bool(true);
                return true;
            case 'f':
                if (s_.substr(pos_, 5) != "false") return fail("bad false");
                pos_ += 5;
                out = make_bool(false);
                return true;
            case 'n':
                if (s_.substr(pos_, 4) != "null") return fail("bad null");
                pos_ += 4;
                out = Value{};
                return true;
            default:
                return parse_number(out);
        }
    }

    bool parse_object(Value& out) {
        out = make_object();
        pos_++;  // {
        skip_ws();
        if (pos_ < s_.size() && s_[pos_] == '}') {
            pos_++;
            return true;
        }
        while (true) {
            skip_ws();
            if (pos_ >= s_.size() || s_[pos_] != '"') return fail("object key");
            std::string key;
            if (!parse_string(key)) return false;
            skip_ws();
            if (pos_ >= s_.size() || s_[pos_] != ':') return fail("object colon");
            pos_++;
            skip_ws();
            Value value;
            if (!parse_value(value)) return false;
            out.object.emplace_back(std::move(key), std::move(value));
            skip_ws();
            if (pos_ >= s_.size()) return fail("object end");
            if (s_[pos_] == ',') {
                pos_++;
                continue;
            }
            if (s_[pos_] == '}') {
                pos_++;
                return true;
            }
            return fail("object separator");
        }
    }

    bool parse_array(Value& out) {
        out = make_array();
        pos_++;  // [
        skip_ws();
        if (pos_ < s_.size() && s_[pos_] == ']') {
            pos_++;
            return true;
        }
        while (true) {
            skip_ws();
            Value value;
            if (!parse_value(value)) return false;
            out.array.push_back(std::move(value));
            skip_ws();
            if (pos_ >= s_.size()) return fail("array end");
            if (s_[pos_] == ',') {
                pos_++;
                continue;
            }
            if (s_[pos_] == ']') {
                pos_++;
                return true;
            }
            return fail("array separator");
        }
    }

    static void append_utf8(std::string& out, unsigned cp) {
        if (cp <= 0x7F) {
            out.push_back(static_cast<char>(cp));
        } else if (cp <= 0x7FF) {
            out.push_back(static_cast<char>(0xC0 | (cp >> 6)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        } else if (cp <= 0xFFFF) {
            out.push_back(static_cast<char>(0xE0 | (cp >> 12)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        } else {
            out.push_back(static_cast<char>(0xF0 | (cp >> 18)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 12) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        }
    }

    bool hex4(unsigned& out) {
        if (pos_ + 4 > s_.size()) return fail("short unicode escape");
        unsigned v = 0;
        for (int i = 0; i < 4; i++) {
            char c = s_[pos_++];
            v <<= 4;
            if (c >= '0' && c <= '9') v |= static_cast<unsigned>(c - '0');
            else if (c >= 'a' && c <= 'f') v |= static_cast<unsigned>(c - 'a' + 10);
            else if (c >= 'A' && c <= 'F') v |= static_cast<unsigned>(c - 'A' + 10);
            else return fail("bad unicode escape");
        }
        out = v;
        return true;
    }

    bool parse_string(std::string& out) {
        out.clear();
        pos_++;  // "
        while (pos_ < s_.size()) {
            unsigned char c = static_cast<unsigned char>(s_[pos_++]);
            if (c == '"') return true;
            if (c == '\\') {
                if (pos_ >= s_.size()) return fail("string escape");
                char e = s_[pos_++];
                switch (e) {
                    case '"': out.push_back('"'); break;
                    case '\\': out.push_back('\\'); break;
                    case '/': out.push_back('/'); break;
                    case 'b': out.push_back('\b'); break;
                    case 'f': out.push_back('\f'); break;
                    case 'n': out.push_back('\n'); break;
                    case 'r': out.push_back('\r'); break;
                    case 't': out.push_back('\t'); break;
                    case 'u': {
                        unsigned cp = 0;
                        if (!hex4(cp)) return false;
                        if (cp >= 0xD800 && cp <= 0xDBFF && pos_ + 6 <= s_.size() &&
                            s_[pos_] == '\\' && s_[pos_ + 1] == 'u') {
                            pos_ += 2;
                            unsigned low = 0;
                            if (!hex4(low)) return false;
                            cp = 0x10000 + ((cp - 0xD800) << 10) + (low - 0xDC00);
                        }
                        append_utf8(out, cp);
                        break;
                    }
                    default:
                        return fail("unknown escape");
                }
            } else {
                out.push_back(static_cast<char>(c));
            }
        }
        return fail("unterminated string");
    }

    bool parse_number(Value& out) {
        size_t start = pos_;
        if (pos_ < s_.size() && s_[pos_] == '-') pos_++;
        while (pos_ < s_.size() && s_[pos_] >= '0' && s_[pos_] <= '9') pos_++;
        bool integer = true;
        if (pos_ < s_.size() && s_[pos_] == '.') {
            integer = false;
            pos_++;
            while (pos_ < s_.size() && s_[pos_] >= '0' && s_[pos_] <= '9') pos_++;
        }
        if (pos_ < s_.size() && (s_[pos_] == 'e' || s_[pos_] == 'E')) {
            integer = false;
            pos_++;
            if (pos_ < s_.size() && (s_[pos_] == '+' || s_[pos_] == '-')) pos_++;
            while (pos_ < s_.size() && s_[pos_] >= '0' && s_[pos_] <= '9') pos_++;
        }
        if (pos_ == start) return fail("bad number");
        std::string token(s_.substr(start, pos_ - start));
        out.type = Type::Number;
        if (integer) {
            char* end = nullptr;
            out.integer = std::strtoll(token.c_str(), &end, 10);
            out.number = static_cast<double>(out.integer);
            out.is_integer = (end != nullptr && *end == '\0');
            if (!out.is_integer) {
                out.number = std::strtod(token.c_str(), nullptr);
            }
        } else {
            out.is_integer = false;
            out.number = std::strtod(token.c_str(), nullptr);
        }
        return true;
    }
};

inline bool parse(std::string_view text, Value& out, std::string* error = nullptr) {
    Parser p(text);
    if (!p.parse(out)) {
        if (error) *error = p.error();
        return false;
    }
    return true;
}

// serialization

inline void dump_string(std::string& out, std::string_view s) {
    out.push_back('"');
    for (unsigned char c : s) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\b': out += "\\b"; break;
            case '\f': out += "\\f"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (c < 0x20) {
                    char buf[7];
                    std::snprintf(buf, sizeof(buf), "\\u%04x", c);
                    out += buf;
                } else {
                    out.push_back(static_cast<char>(c));
                }
        }
    }
    out.push_back('"');
}

inline void dump_into(std::string& out, const Value& v) {
    switch (v.type) {
        case Type::Null:
            out += "null";
            break;
        case Type::Bool:
            out += v.boolean ? "true" : "false";
            break;
        case Type::Number:
            if (v.is_integer) {
                out += std::to_string(v.integer);
            } else {
                char buf[64];
                std::snprintf(buf, sizeof(buf), "%.17g", v.number);
                out += buf;
            }
            break;
        case Type::String:
            dump_string(out, v.string);
            break;
        case Type::Array:
            out.push_back('[');
            for (size_t i = 0; i < v.array.size(); i++) {
                if (i) out.push_back(',');
                dump_into(out, v.array[i]);
            }
            out.push_back(']');
            break;
        case Type::Object:
            out.push_back('{');
            for (size_t i = 0; i < v.object.size(); i++) {
                if (i) out.push_back(',');
                dump_string(out, v.object[i].first);
                out.push_back(':');
                dump_into(out, v.object[i].second);
            }
            out.push_back('}');
            break;
    }
}

inline std::string dump(const Value& v) {
    std::string out;
    out.reserve(256);
    dump_into(out, v);
    return out;
}

// convenience getters

inline std::string str(const Value* obj, std::string_view key, std::string def = {}) {
    const Value* v = obj ? obj->get(key) : nullptr;
    return (v && v->type == Type::String) ? v->string : std::move(def);
}

inline long long num(const Value* obj, std::string_view key, long long def = 0) {
    const Value* v = obj ? obj->get(key) : nullptr;
    if (!v) return def;
    if (v->type == Type::Number) {
        return v->is_integer ? v->integer : static_cast<long long>(v->number);
    }
    if (v->type == Type::String && !v->string.empty()) {
        try {
            return std::stoll(v->string);
        } catch (...) {
            return def;
        }
    }
    return def;
}

inline bool boolean(const Value* obj, std::string_view key, bool def = false) {
    const Value* v = obj ? obj->get(key) : nullptr;
    return (v && v->type == Type::Bool) ? v->boolean : def;
}

}  // namespace scjson

#endif  // SPERMACORE_TELEGRAM_JSON_HPP