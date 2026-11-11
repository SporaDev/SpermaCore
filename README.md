# SpermaCore

Modular runtime for personal LLM bots and userbots written in Go and C++20.

[Russian version](README.ru.md) | [English version](README.md)

---

## What is this

SpermaCore is a lightweight runtime engine for running autonomous LLM agents and Telegram userbots. 

Instead of hardcoding prompts, agent loops, and messanger protocols into one big monolith, SpermaCore separates them:
* Core (Go): worker pool, task queue, event bus, LLM routing (OpenRouter, Groq, Ollama), SQLite storage, and a real-time WebUI dashboard on port 8080.
* Modules (C++20 dynamic libraries): Telegram TDLib transport, long-term vector memory (HNSW / RAG), autonomous initiative loops, media analysis, and custom tools.

If a module fails or is not installed, the core stays alive and falls back to safe mode.

---

## Installing Modules

C++ modules are packaged into `releases/modules.zip` to keep the main repository lightweight.

### 1. Extract the archive
Unpack `releases/modules.zip` into the root directory of the project:

```bash
# On Linux / macOS:
unzip releases/modules.zip

# On Windows (PowerShell):
Expand-Archive -Path releases/modules.zip -DestinationPath . -Force
```

After extraction, you will have a `modules/` directory containing the module sources:
* `modules/telegram`: Telegram TDLib userbot transport (QR code login, message sending, reactions, typing)
* `modules/embedsc`: Vector memory (HNSW indexing with SQLite storage)
* `modules/agentloop`: Autonomous background initiative loop
* `modules/grouper`: Message batching and debounce
* `modules/media`: Emoji and sticker analysis
* `modules/dashboard`: Live stats and module telemetry for WebUI
* `modules/moderation`: Inbound filtering and rate limiting
* `modules/websearch`: Web search tool for agents

### 2. Building Modules

You have two options to build the modules:

#### Option A: Automatic build (recommended)
When SpermaCore starts, if a module is listed in `config.toml` but its compiled binary (`.dll` or `.so`) is missing, the Go runtime automatically invokes CMake and compiles it from `modules/<name>/src`. 
You only need CMake and a C++20 compiler (GCC, Clang, or MinGW) installed on your system.

#### Option B: Manual build with CMake
You can build all modules at once using the root `CMakeLists.txt`:

```bash
cmake -B build -G Ninja
cmake --build build
```

Compiled binaries will be placed in their respective `modules/<name>/` directories.

### 3. Enable modules in configuration
Open `config.toml` and adjust the `[plugins]` section:

```toml
[plugins]
path = "./modules"
load = [
  "telegram",
  "embedsc",
  "agentloop",
  "media",
  "dashboard",
  "grouper",
  "moderation"
]
```

---

## Quickstart

### Requirements
* Go 1.22 or newer
* CMake 3.20+ and GCC / Clang / MinGW with C++20 support (for building modules)
* TDLib binaries (`tdjson.dll` on Windows or `libtdjson.so` on Linux) inside `data/telegram/`

### 1. Configure environment
Copy the template and set your credentials:

```bash
cp .env.example .env
```

Fill in your secrets:
```ini
OPENROUTER_API_KEY=sk-or-v1-...
SPERMACORE_TELEGRAM_API_ID=your_api_id
SPERMACORE_TELEGRAM_API_HASH=your_api_hash
SPERMACORE_TELEGRAM_PHONE=+1234567890
SPERMACORE_DB_KEY=your_secure_db_key
```

### 2. Configure runtime
Review `config.toml` to select your preferred LLM model, vision model, and enabled plugins.

### 3. Build and run
Run directly with Go:
```bash
go run ./core
```

Or compile a binary:
```bash
go build -o releases/spermacore.exe ./core
./releases/spermacore.exe
```

Access the WebUI dashboard at `http://localhost:8080`.

---

## Architecture

```text
spermacore/
├── core/              # Go engine (worker pool, event queue, LLM router, WebUI)
├── include/           # C11 / C++20 ABI headers (plugin_api.h)
├── releases/          # Prebuilt binaries and modules.zip archive
├── config.toml        # Application configuration
├── .env.example       # Environment template
└── CMakeLists.txt     # Root CMake build file for modules
```

### Plugin ABI (1.7.1)
All modules communicate with the Go core through a stable C ABI defined in `include/plugin_api.h`. 
Modules export a single entrypoint `plugin_get_api()` that returns function pointers for lifecycle events (`init`, `start`, `stop`, `destroy`), transports, and tools.

---

## Security

* Secrets are strictly loaded from environment variables or `.env`. Never commit credentials to git.
* SQLite databases use SQLCipher encryption via `SPERMACORE_DB_KEY`.
* If `auth_token` is left empty in `config.toml`, WebUI binds strictly to loopback (`127.0.0.1`) and generates a temporary session key on startup.

---

## License

Distributed under the PolyForm Noncommercial License 1.0.0. See [LICENSE](LICENSE) or [polyformproject.org](https://polyformproject.org/licenses/noncommercial/1.0.0).
