<div align="center">

# spermacore v1.9.0

**spermacore** - autonomous modular runtime for personal llm agents in go and c++20

[![go](https://img.shields.io/badge/go-1.22+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![c++20](https://img.shields.io/badge/c++-20-00599C?style=flat&logo=cplusplus&logoColor=white)](https://en.cppreference.com/w/cpp/20)
[![abi](https://img.shields.io/badge/abi-1.7-green?style=flat)](#architecture)
[![webui](https://img.shields.io/badge/webui-8080-orange?style=flat)](#web-dashboard)
[![license](https://img.shields.io/badge/license-polyform%20noncommercial%201.0.0-blue?style=flat)](#license)

</div>

---

## overview

**spermacore** is an ultra-lightweight, modular engine for building autonomous ai personas and personal bots.

core principles:
- bare, independent go runtime - no built-in telegram monolith or hardcoded agent loops in the core.
- native c++20 dynamic plugins (abi 1.7) for transports, reasoning, memory, and autonomous background cycles.
- real-time webui on port 8080 with live metrics, telegram qr login, and interactive module widgets.

---

## quickstart

### option 1 - prebuilt release (recommended)

the prebuilt binary lives in `releases/` (or in standalone bundles `dist/spermacore` and `spermacore-clean`):

1. copy environment template:
   ```bash
   cp .env.example .env
   ```
2. fill in your api keys in `.env` (`REFORMBOSS_API_KEY`, `OPENROUTER_API_KEY`, etc.).
3. run spermacore:
   - **windows:** double-click `start.bat`
   - **terminal:** run `releases/spermacore.exe -config config.toml`
4. open dashboard: **[http://127.0.0.1:8080](http://127.0.0.1:8080)**.

---

### option 2 - build from source

requirements: go 1.22+, cmake 3.20+, gcc/mingw (c++20).

```bash
# build core binary
go build -o releases/spermacore.exe ./core

# run
releases/spermacore.exe -config config.toml
```

---

## modules (abi 1.7)

all capabilities are decoupled into shared libraries inside `modules/`:

| module | role | description |
|---|---|---|
| `telegram` | telegram transport | tdlib-based userbot, qr login via webui, typing simulation, smart quoting |
| `agentloop` | autonomous agent loop | autonomous initiative, background context analysis, interactive webui widget |
| `react` | react planner | model reasoning loop (reason + act) with tool call dispatch |
| `embedsc` | vector memory (rag) | semantic memory and facts stored in local sqlite db with fast hnsw / ann graph indexing |
| `usersc` | user profiles | automatically tracks user facts, schedules, and follow-up events |
| `sysprompt` | system prompt builder | dynamic prompt composition and persona styling |
| `time` | system clock | `get_time` tool with timezone awareness |
| `websearch` | web search | duckduckgo search and web page extraction |

---

## minimal config (`config.toml`)

a clean configuration without unnecessary noise:

```toml
[core]
data_dir = "./data"
log_level = "info"
workers = 4

[llm]
provider = "reformboss"
model = "glm-5.3"

[llm.providers.reformboss]
base_url = "https://api.reformboss.com/v1"
api_key = "${REFORMBOSS_API_KEY}"
timeout_seconds = 60

[webui]
enabled = true
bind = "127.0.0.1"
port = 8080
auth_token = "${SPERMACORE_WEBUI_TOKEN}"

[memory]
enabled = true
embedding_model = "qwen3-embedding:latest"
max_rag_results = 5
similarity_threshold = 0.72
index_type = "hnsw"
hnsw_m = 16
hnsw_ef_construction = 64
hnsw_ef_search = 32

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
]

[plugins.telegram]
enabled = true
api_id = 33558968
api_hash = "${SPERMACORE_TELEGRAM_API_HASH}"
phone = "${SPERMACORE_TELEGRAM_PHONE}"
login_mode = "qr"
reply_to = true

[plugins.agentloop]
enabled = true
interval_min = 15
active_hours_start = 9
active_hours_end = 23
max_per_day = 10

[humanify]
enabled = true
delay_min_ms = 1200
delay_max_ms = 3500
typo_chance_percent = 40
auto_fix_typo = true

[groups]
enabled = true
reply_chance = 0.06
cooldown_seconds = 45
```

---

## web dashboard (:8080)

the built-in dashboard runs on port `8080`:
- **telegram qr login:** scan qr code directly with telegram mobile app.
- **agent cycle widget (`agentloop.cycle`):**
  - live countdown timer until next autonomous cycle.
  - instant trigger and pause/resume buttons.
  - daily action counter and active hours window.
- **live telemetry:** message queue, worker utilization, memory stats, and tool invocations.

---

## repository structure

```text
spermacore/
├── releases/             # prebuilt release binaries
│   └── spermacore.exe
├── config.toml           # production configuration
├── .env.example          # environment key template
├── start.bat             # windows one-click launcher
├── tdjson.dll            # tdlib shared library
├── core/                 # go runtime (abi host, worker pool, webui, event bus)
├── include/              # c/c++ plugin abi header (plugin_api.h)
├── modules/              # native c++ modules
└── data/                 # runtime storage (sqlite dbs, sessions, cache)
```

---

## license

polyform noncommercial 1.0.0.
