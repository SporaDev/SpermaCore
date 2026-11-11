<div align="center">

# SpermaCore v1.9.0

**СПЕРМА** — Содружество Принципиальных Единомышленников Ради Мира и Абсолюта  
*High-performance, autonomous modular runtime engine for personal LLM agents and cyber-personas in Go & C++20*

[🇷🇺 Читать на русском](README.ru.md) • [🇬🇧 English Version](README.md)

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![C++ Standard](https://img.shields.io/badge/C++-20-00599C?style=flat&logo=cplusplus&logoColor=white)](https://en.cppreference.com/w/cpp/20)
[![ABI Version](https://img.shields.io/badge/ABI-1.7.1-green?style=flat)](#plugin-architecture-abi-17)
[![WebUI](https://img.shields.io/badge/WebUI-8080-orange?style=flat)](#web-dashboard)
[![Storage](https://img.shields.io/badge/Storage-SQLite%20%2B%20SQLCipher-blue?style=flat)](#security--secrets)
[![License](https://img.shields.io/badge/License-PolyForm%20Noncommercial%201.0.0-purple?style=flat)](#license)

</div>

---

## Overview

**SpermaCore** is an ultra-lightweight, decoupled execution engine designed for running autonomous, human-like AI agents, personal assistants, and cyber-personas.

### Core Philosophy

Most AI agent frameworks are bulky monoliths that hardcode prompt chains, agent loops, and communication protocols into a single runtime. SpermaCore takes the opposite approach inspired by operating system microkernels:

- **Bare Go Runtime Core**: The core handles concurrency, worker pools, event dispatching, local storage, LLM provider communication, and the real-time WebUI. It contains **no hardcoded persona**, **no fixed prompt templates**, and **no built-in transport lock-in**.
- **Native C++20 / C11 Dynamic Plugins (ABI 1.7.1)**: Transports (Telegram TDLib), memory systems (RAG / HNSW), system prompts, tools, and autonomous cycles live in standalone dynamic libraries (`.dll` / `.so`).
- **Pluggable & Graceful Degradation**: Replace or rebuild a single `.dll` without touching the core. If a module fails or is omitted, the core safely degrades (e.g. falling back to chat-only mode).
- **Multi-Mode Bridge**: Supports both direct, zero-copy in-process CGO execution (`CGO_ENABLED=1`) and sandboxed out-of-process sidecar supervisors (`CGO_ENABLED=0`).

---

## Key Features

- ⚡ **High-Speed CGO & Sidecar ABI 1.7.1**: Native plugin loading with strict ABI stability, designated initializers, thread-safety flags (`SC_PLUGIN_FLAG_THREAD_SAFE`), and automatic CMake building on demand.
- 📱 **Native Telegram TDLib Userbot**: In-process TDLib (`tdjson`) transport with WebUI QR code login, phone/2FA authentication, SOCKS5/HTTP/MTProto proxies, WebSocket proxy bypasses (`tgwsproxy`), typing indicators, and message reactions.
- 🧠 **Autonomous Initiative Cycle (`agentloop`)**: Proactive background cycles during configurable active hours (`active_hours_start`/`end`), friend list tracking, context evaluation, and WebUI cycle controls.
- ✍️ **Hyper-Realistic Human Mimicry (`humanify`)**: Realistic typing speed variations, human-like delay curves, typo generation with automatic corrections, pause-splitting into natural multi-message bursts, and distraction intervals.
- 👥 **Group Policy Engine (`groups`)**: Context-aware participation in group chats: ambient vs. direct activations, priority thresholds (0-3), active trigger phrases (`active-phrases.json`), cooldown windows, and hourly message rate limits.
- 🔍 **Embedded Vector Memory / RAG (`embedsc`)**: In-process SQLite vector store backed by an embedded **HNSW** (Hierarchical Navigable Small World) graph index with cosine similarity filtering.
- 🖥️ **Real-Time WebUI Dashboard (`:8080`)**: WebSocket-driven dark cyber dashboard with live telemetry, queue metrics, worker pool status, Telegram QR login modal, agent cycle controls, and live terminal streaming.
- 🌐 **Provider-Agnostic LLM Routing**: Any OpenAI-compatible endpoint (OpenRouter, ReformBoss, Groq, Cerebras, Ollama) with role-based routing (`chat`, `vision`, `embedding`) and custom request payloads.
- 🔐 **Hardened Security**: Environment variables via `.env` keep secrets out of Git; local SQLite databases support AES-256 encryption via SQLCipher (`db_key`).

---

## Quickstart

### Prerequisites

| Tool | Version | Purpose |
|---|---|---|
| **Go** | 1.22+ | Core runtime and WebUI server |
| **CMake** | 3.20+ | Native plugin build system |
| **GCC / MinGW-w64** | C++20 & C11 | Plugin compilation & CGO bridge |
| **Ninja** | Latest | Recommended CMake generator |

---

### Step 1: Environment Setup

Clone the repository and copy the environment template:

```bash
cp .env.example .env
```

Edit `.env` to configure your API keys and secrets:

```ini
# LLM Providers
OPENROUTER_API_KEY=sk-or-v1-...
REFORMBOSS_API_KEY=...

# Telegram Userbot Credentials (https://my.telegram.org)
SPERMACORE_TELEGRAM_API_HASH=your_telegram_api_hash
SPERMACORE_TELEGRAM_PHONE=+1234567890

# Database & WebUI Security
SPERMACORE_DB_KEY=your-secure-sqlite-key
SPERMACORE_WEBUI_TOKEN=your-secure-webui-token
```

---

### Step 2: Compile Plugins (Optional / Automatic)

Plugins reside in `modules/`. You can build all native plugins using the root `CMakeLists.txt`:

```bash
# Generate build files and compile all modules
cmake -B build -G Ninja
cmake --build build
```

> **Automatic Compilation**: If a plugin is listed in `config.toml` but its `.dll`/`.so` is not found, the Go runtime will automatically detect its `src/` directory and build it via CMake on startup!

---

### Step 3: Build & Run SpermaCore

```bash
# Build the Go core binary
go build -o spermacore.exe ./core

# Run SpermaCore with default configuration
./spermacore.exe -config config.toml
```

CLI flags:
- `-config <path>`: Path to configuration file (default: `config.toml`).
- `-version`: Print SpermaCore version and exit.
- `-rebuild`: Clean all module build directories and force a full rebuild.

---

### Step 4: Open Web Dashboard

Navigate to **[http://127.0.0.1:8080](http://127.0.0.1:8080)** in your browser:
- If `SPERMACORE_WEBUI_TOKEN` is set, enter it in the auth prompt.
- If using Telegram with `login_mode = "qr"`, scan the displayed QR code with your Telegram mobile app (*Settings → Devices → Link Desktop Device*).

---

## Module Ecosystem (ABI 1.7.1)

All capabilities are decoupled into independent dynamic libraries inside `modules/`:

| Module | Category | Description | Tools / Syscalls |
|---|---|---|---|
| **`telegram`** | Transport | TDLib-based userbot: QR/phone login, typing simulation, smart quoting, reactions, sticker support | `send_message`, `react`, `send_sticker`, `mark_read` |
| **`agentloop`** | Autonomy | Autonomous background loop: proactive messaging, active hours, live WebUI cycle widget | `agentloop.cycle`, cycle controls |
| **`react`** | Planner | ReAct execution loop (Reason + Act): dispatches tool calls and feeds results back to LLM | Dynamic OpenAI function calling |
| **`embedsc`** | Memory | Vector memory: semantic embeddings stored in SQLite with fast in-process HNSW graph indexing | `embedsc.remember`, `embedsc.recall`, `embedsc.stats` |
| **`usersc`** | Context | User facts and profiles: tracks schedules, follow-up events, and aliases | `get_user`, `resolve_username` |
| **`sysprompt`** | Persona | Dynamic prompt composer: injects persona, system instructions, and guidelines from text files | Dynamic prompt injection |
| **`time`** | Utility | System clock tool with timezone awareness | `get_time` |
| **`websearch`** | Utility | DuckDuckGo search integration and webpage content extraction | `web_search`, `web_fetch` |
| **`media`** | Multimedia | Emoji and sticker vision analysis, multimedia event routing | `media.inspect`, vision triggers |
| **`dashboard`** | Telemetry | Native dashboard metrics, system status, and plugin telemetry provider | Status hooks |
| **`grouper`** | Flow | Message batching: groups rapid bursts of inbound messages from the same chat before LLM execution | Inbound aggregator |
| **`bypasses`** | Network | MTProto WebSocket proxy (`tgwsproxy`) for networks with deep packet inspection | Connection proxy |

---

## Configuration (`config.toml`)

`config.toml` uses environment variable expansion (`${VAR_NAME}`) to separate structure from secrets:

```toml
# spermacore production configuration

[core]
data_dir  = "./data"
log_level = "info"
workers   = 4
db_key    = "${SPERMACORE_DB_KEY}"

[llm]
provider = "openrouter"
model    = "google/gemma-4-26b-a4b-it:free"

[llm.providers.openrouter]
base_url        = "https://openrouter.ai/api/v1"
api_key         = "${OPENROUTER_API_KEY}"
timeout_seconds = 60
max_concurrent  = 8

[webui]
enabled    = true
bind       = "127.0.0.1"
port       = 8080
auth_token = "${SPERMACORE_WEBUI_TOKEN}"

[plugins]
load = [
  "sysprompt",
  "react",
  "usersc",
  "telegram",
  "embedsc",
  "time",
  "websearch",
  "agentloop",
  "media",
  "dashboard",
  "grouper",
  "bypasses",
]

[plugins.telegram]
enabled      = true
api_id       = "${SPERMACORE_TELEGRAM_API_ID}"
api_hash     = "${SPERMACORE_TELEGRAM_API_HASH}"
phone        = "${SPERMACORE_TELEGRAM_PHONE}"
login_mode   = "qr"            # "qr", "phone", or "auto"
reply_to     = true            # smart reply quoting
device_model = "Desktop"
app_version  = "1.0.0"

[plugins.agentloop]
enabled            = true
friends            = []        # list of usernames or chat IDs to proactively talk to
interval_min       = 15
active_hours_start = 9
active_hours_end   = 23
max_per_day        = 10

[memory]
enabled              = true
embedding_model      = "liquid/lfm-2.5-embedding-350m:free"
max_rag_results      = 5
similarity_threshold = 0.72
index_type           = "hnsw"
hnsw_m               = 16
hnsw_ef_construction = 64
hnsw_ef_search       = 32

[humanify]
enabled             = true
delay_min_ms        = 1200
delay_max_ms        = 3500
typo_chance_percent = 40
auto_fix_typo       = true
distraction_chance  = 0.08
split_long          = true
split_parts         = 3
split_threshold     = 200

[groups]
enabled                = true
reply_chance           = 0.06
cooldown_seconds       = 45
active_phrases_file    = "data/active-phrases.json"
max_replies_per_hour   = 8
priority1_chance       = 0.12
priority2_chance       = 0.55
priority3_always_reply = true
```

---

## Web Dashboard (:8080)

The built-in WebUI provides real-time control and monitoring:

- **Telegram QR Login**: Scan the real-time QR code directly inside the WebUI to link the userbot account without console interaction.
- **Autonomous Cycle Widget (`agentloop.cycle`)**:
  - Live countdown timer to the next scheduled proactive cycle.
  - Buttons for instant trigger (`Trigger Now`) and pause/resume.
  - Current daily action counter and active hours window status.
- **Telemetry & Worker Utilization**: Live display of incoming queue depth, active worker goroutines, memory allocation, and tool invocation counters.
- **Terminal Log Stream**: Real-time WebSocket streaming of logs with level filtering (`DEBUG`, `INFO`, `TOOL`, `WARN`, `ERROR`).
- **Interactive Addon Widgets**: Dynamic widgets registered by C++ plugins via the ABI widget protocol.

---

## Developing Plugins (ABI 1.7.1)

Creating a new plugin requires a single dynamic library with one entry point `plugin_get_api()`.

### Example: Minimal C++20 Plugin

Create `modules/hello/src/hello.cpp`:

```cpp
#include "plugin_api.h"
#include <string>

static sc_tool_def_t TOOLS[] = {
    {
        .name = "say_hello",
        .description = "Greets a person by name",
        .parameters_json = R"({
            "type": "object",
            "properties": {
                "name": {"type": "string", "description": "The person to greet"}
            },
            "required": ["name"]
        })",
    },
};

static int hello_call_tool(const char* name, const char* args_json,
                           char** out_json, const sc_host_api_t* host) {
    if (std::string(name) == "say_hello") {
        *out_json = sc_strdup(R"({"greeting": "Hello from C++20 ABI!"})");
        return SC_OK;
    }
    return SC_ERR_NOT_FOUND;
}

static sc_plugin_api_t API = {
    .abi_version = SC_ABI_VERSION,
    .info = {
        .name = "hello",
        .version = "1.0.0",
        .human_description = "A friendly greeting plugin",
        .flags = SC_PLUGIN_FLAG_THREAD_SAFE,
    },
    .get_tools = [](uint32_t* count) -> const sc_tool_def_t* {
        *count = sizeof(TOOLS) / sizeof(TOOLS[0]);
        return TOOLS;
    },
    .call_tool = hello_call_tool,
};

extern "C" SC_EXPORT sc_plugin_api_t* plugin_get_api(void) {
    return &API;
}
```

### Key ABI Rules
1. **Thread Safety**: By default, calls into plugins are serialized with a mutex. Set `SC_PLUGIN_FLAG_THREAD_SAFE` in `.flags` if your plugin safely handles concurrent calls.
2. **Memory Allocation**: Buffers allocated for `*out_json` must use `sc_strdup()` or `host->alloc()` so the core can free them cleanly.
3. **No Name Mangling**: The entry point `plugin_get_api` must always be wrapped in `extern "C"`.

---

## Repository Structure

```text
spermacore-clean/
├── core/                     # Go runtime engine
│   ├── dashboard/            # State aggregation & telemetry
│   ├── embedsc/              # In-process HNSW graph indexing
│   ├── grouper/              # Inbound message batching
│   ├── humanify/             # Typing simulation, delays & typos
│   ├── media/                # Emoji, sticker & media vision
│   ├── sandbox/              # Sandbox configuration & validation
│   ├── sidecar/              # Out-of-process supervisor protocol
│   ├── webui/                # WebUI HTTP/WebSocket server & static assets
│   ├── addon.go              # Plugin loader & automatic CMake builder
│   ├── cgo_bridge.go         # In-process CGO bridge to ABI 1.7
│   ├── cgo_stub.go           # Out-of-process sidecar stub (!cgo fallback)
│   ├── llm.go                # Multi-provider LLM client & role router
│   ├── react.go              # ReAct planning & execution loop
│   ├── worker.go             # Concurrency worker pool & queue
│   └── main.go               # Entry point
├── data/                     # Prompts, runtime data & binaries
│   ├── telegram/             # TDLib libraries (tdjson.dll, openssl, zlib)
│   ├── active-phrases.json   # Group chat active trigger phrases
│   ├── base-prompt.txt       # Core system instructions and priorities
│   └── policy.txt            # Persona policy rules
├── include/                  # Native C/C++ ABI headers
│   ├── json.hpp              # Header-only JSON library
│   └── plugin_api.h          # C11 / C++20 ABI 1.7.1 contract
├── modules/                  # Dynamic C++20 plugin source & build targets
├── CMakeLists.txt            # Root CMake script for building all modules
├── config.toml               # Production configuration
├── .env.example              # Environment variables template
├── CONTRIBUTING.md           # Coding standards and PR guidelines
├── go.mod / go.sum           # Go dependencies
└── README.md                 # Project documentation
```

---

## Security & Secrets

- **Never commit `.env` or `*.session` files**: Secrets are strictly read from environment variables or the `.env` file.
- **Encrypted Local Storage**: SQLite databases are encrypted via SQLCipher using `SPERMACORE_DB_KEY`.
- **Loopback Protection**: If `auth_token` is left empty, WebUI binds strictly to loopback (`127.0.0.1`) and generates a temporary session key logged on startup.
- **TDLib Security**: TDLib local storage is independently encrypted with `SPERMACORE_TELEGRAM_DB_KEY`.

---

## License

Distributed under the **PolyForm Noncommercial License 1.0.0**. See [LICENSE](LICENSE) or [polyformproject.org](https://polyformproject.org/licenses/noncommercial/1.0.0).

- Free to read, modify, and run for personal, educational, research, and non-commercial purposes.
- Commercial use, SaaS hosting, or closed-source commercial redistribution requires a separate license from the author.
- Third-party plugins authored by you belong to you and are not restricted by this license.

Contributions and pull requests are welcome! Please read [CONTRIBUTING.md](CONTRIBUTING.md) before submitting code.

---

<div align="center">

**SpermaCore Project** · 2026  
*Содружество Принципиальных Единомышленников Ради Мира и Абсолюта*

</div>
