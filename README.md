# SpermaCore

<p align="left">
  <img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.22+">
  <img src="https://img.shields.io/badge/C%2B%2B-20-00599C?style=flat-square&logo=c%2B%2B&logoColor=white" alt="C++20">
  <img src="https://img.shields.io/badge/ABI-v1.7.1-8b5cf6?style=flat-square" alt="ABI v1.7.1">
  <img src="https://img.shields.io/badge/Telegram-TDLib-26A5E4?style=flat-square&logo=telegram&logoColor=white" alt="TDLib">
  <img src="https://img.shields.io/badge/Storage-SQLCipher-003B57?style=flat-square&logo=sqlite&logoColor=white" alt="SQLCipher">
  <img src="https://img.shields.io/badge/license-PolyForm%20Noncommercial-0ea5e9?style=flat-square" alt="PolyForm License">
</p>

Modular, crash-resilient runtime for autonomous LLM agents and Telegram userbots written in Go 1.22+ and C++20.

[Russian version](README.ru.md) | [English version](README.md)

---

## Overview

SpermaCore is an agent engine designed around strict separation of concerns. Instead of stuffing prompt handling, memory indexes, and client protocols into an unmaintainable monolith, the runtime divides responsibilities between two layers:

* **Core Runtime (Go 1.22+)**: Non-blocking event loop, worker goroutines pool, multi-provider LLM routing (OpenRouter, Groq, Ollama, Gemini Vision), SQLCipher-encrypted SQLite store, and a local WebUI dashboard on port 8080.
* **Native Modules (C++20 Shared Libraries)**: High-performance modules compiled against a stable C ABI (`include/plugin_api.h`). Includes TDLib transport, long-term HNSW vector memory (`embedsc`), proactive background reflection loop (`agentloop`), media/sticker vision, message grouping, and web search.

If a C++ module crashes or fails to load, the Go core catches the fault, marks the worker degraded, and continues serving requests in safe mode without dropping client sessions.

---

## Architecture & ABI (1.7.1)

```text
spermacore/
├── core/              # Go runtime: dispatcher, LLM routing, SQLCipher storage, WebUI
├── include/           # Stable C ABI headers (plugin_api.h)
├── modules/           # Native C++20 dynamic plugins
├── landing/           # Project dashboard & documentation landing page
├── releases/          # Precompiled binaries and modules.zip package
├── config.toml        # Application and provider configuration
├── .env.example       # Secrets and credential templates
└── CMakeLists.txt     # Root build script for C++ plugins
```

The core talks to modules via standard C function pointers (`plugin_get_api()`). Each plugin exports lifecycle hooks (`init`, `start`, `stop`, `destroy`) and optional event sinks, ensuring zero memory copies across language boundaries.

---

## Quickstart

### Prerequisites
* Go 1.22+
* CMake 3.20+ and a C++20 compiler (GCC, Clang, or MinGW)
* TDLib binaries (`tdjson.dll` on Windows or `libtdjson.so` on Linux) inside `data/telegram/`

### 1. Extract Modules
The source code of C++ modules is packaged into `releases/modules.zip` to keep clones lightweight:

```bash
# Linux / macOS
unzip releases/modules.zip

# Windows (PowerShell)
Expand-Archive -Path releases/modules.zip -DestinationPath . -Force
```

### 2. Configure Environment
Copy the environment template and provide your API keys:

```bash
cp .env.example .env
```

Key environment variables:
```ini
OPENROUTER_API_KEY=sk-or-v1-...
SPERMACORE_TELEGRAM_API_ID=12345678
SPERMACORE_TELEGRAM_API_HASH=abcdef0123456789...
SPERMACORE_DB_KEY=your_secure_db_key
```

### 3. Build & Run

Run directly through Go (uncompiled plugins in `config.toml` are automatically built by the Go runtime via CMake on first boot):

```bash
go run ./core
```

Or build a production executable:
```bash
go build -o releases/spermacore.exe ./core
./releases/spermacore.exe
```

Access the local management WebUI at `http://localhost:8080`.

---

## Active Modules

| Module | Language | Role |
| :--- | :--- | :--- |
| `telegram` | C++20 | Full TDLib userbot client (QR code auth, typing simulation, voice/photo transport) |
| `embedsc` | C++20 | In-memory HNSW vector index for semantic retrieval and long-term memory |
| `agentloop` | C++20 | Proactive reflection loop for autonomous task execution |
| `media` | C++20 | Sticker, emoji, and image preprocessing pipeline |
| `grouper` | C++20 | Inbound message grouping and anti-flood debouncing |
| `moderation` | C++20 | User rate-limiting, allowlist, and blocklist policies |
| `websearch` | C++20 | Low-overhead search engine scraper for live information retrieval |
| `dashboard` | C++20 | Module telemetry and real-time health metrics streaming to WebUI |

To enable or disable modules, adjust the `load` array under `[plugins]` in `config.toml`.

---

## Security

* Secrets and tokens are loaded strictly from environment variables or `.env`.
* Local SQLite databases are encrypted at rest with SQLCipher using `SPERMACORE_DB_KEY`.
* By default, the WebUI binds to `127.0.0.1` and uses a generated one-time session token if `auth_token` is left empty.

---

## License

Distributed under the PolyForm Noncommercial License 1.0.0. See [LICENSE](LICENSE) or visit [polyformproject.org](https://polyformproject.org/licenses/noncommercial/1.0.0) for terms.
