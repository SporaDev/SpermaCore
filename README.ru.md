<div align="center">

# SpermaCore v1.9.0

**СПЕРМА** — Содружество Принципиальных Единомышленников Ради Мира и Абсолюта  
*Высокопроизводительное модульное ядро для персональных LLM-агентов и автономных кибер-персон на Go и C++20*

[🇷🇺 Русская версия](README.ru.md) • [🇬🇧 English Version](README.md)

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![C++ Standard](https://img.shields.io/badge/C++-20-00599C?style=flat&logo=cplusplus&logoColor=white)](https://en.cppreference.com/w/cpp/20)
[![ABI Version](https://img.shields.io/badge/ABI-1.7.1-green?style=flat)](#архитектура-плагинов-abi-171)
[![WebUI](https://img.shields.io/badge/WebUI-8080-orange?style=flat)](#веб-панель-управления-8080)
[![Storage](https://img.shields.io/badge/Storage-SQLite%20%2B%20SQLCipher-blue?style=flat)](#безопасность-и-секреты)
[![License](https://img.shields.io/badge/License-PolyForm%20Noncommercial%201.0.0-purple?style=flat)](#лицензия)

</div>

---

## Что такое SpermaCore

**SpermaCore** — это модульный движок для создания и автономного запуска персональных ИИ-агентов, компаньонов и кибер-личностей с естественным поведением.

### Философия ядра

Большинство существующих агентских фреймворков представляют собой монолиты, где логика промптов, циклы планирования и протоколы мессенджеров намертво зашиты в одном коде. Архитектура SpermaCore построена по микроядерному принципу:

- **Чистое Go-ядро (Core)**: отвечает за диспетчеризацию событий, пул воркеров, шифрованную базу данных SQLite (SQLCipher), ReAct-цикл, маршрутизацию LLM-запросов и WebUI. В ядре **нет захардкоженного характера**, **нет монолитного кода Telegram** и **нет привязки к конкретным провайдерам**.
- **Нативные C++20 / C11 плагины (ABI 1.7.1)**: транспорты (Telegram TDLib), долговременная векторная память (RAG / HNSW), системные промпты, пользовательские профили и автономные фоновые циклы вынесены в динамические библиотеки (`.dll` / `.so`).
- **Модульность и надежность (Graceful Degradation)**: любой плагин можно заменить, перезагрузить или перекомпилировать без изменения ядра. Если плагин отсутствует или упал, ядро продолжает работу в безопасном режиме (например, переходит в chat-only).
- **Двухрежимный мост**: поддержка прямого zero-copy вызова через CGO (`CGO_ENABLED=1`) для максимальной скорости или изолированного процесса-супервизора sidecar (`CGO_ENABLED=0`).

---

## Ключевые возможности

- ⚡ **Высокоскоростной CGO & Sidecar ABI 1.7.1**: загрузка нативных динамических библиотек со строгим контрактом ABI, designated initializers, флагами потокобезопасности (`SC_PLUGIN_FLAG_THREAD_SAFE`) и автоматической компиляцией через CMake при первом старте.
- 📱 **Telegram-юзербот на базе TDLib**: нативный транспорт на TDLib (`tdjson`) со входом по QR-коду прямо в WebUI, двухфакторной аутентификацией, поддержкой прокси (SOCKS5/HTTP/MTProto), WebSocket-обходами блокировок (`tgwsproxy`), эмуляцией набора текста и реакциями на сообщения.
- 🧠 **Автономный фоновый цикл (`agentloop`)**: проактивная инициатива агента в заданные часы активности (`active_hours_start`/`end`), проверка списка друзей, оценка контекста и интерактивный виджет управления циклом в WebUI.
- ✍️ **Имитация живого человека (`humanify`)**: переменные паузы и темп набора в зависимости от длины текста, генерация реалистичных опечаток с их последующим исправлением, разбиение длинных мыслей на отдельные сообщения (очереди реплик), имитация случайных отвлечений.
- 👥 **Политика групповых чатов (`groups`)**: умное поведение в беседах: ambient (фоновое чтение) vs direct (прямое обращение), приоритеты собеседников (уровни 0–3), триггерные фразы (`active-phrases.json`), окна удержания диалога и лимиты реплик в час.
- 🔍 **Векторная память и RAG (`embedsc`)**: локальное хранилище в SQLite со встроенным графовым индексом **HNSW** (Hierarchical Navigable Small World), косинусным сходством и нулевой зависимостью от сторонних векторных баз данных.
- 🖥️ **Интерактивный WebUI-дашборд (`:8080`)**: кибер-панель в темных тонах на WebSockets с метриками очереди, мониторингом воркеров, модалкой QR-авторизации Telegram, контролем цикла автономности и живым стримингом логов.
- 🌐 **Мультипровайдерный LLM-роутинг**: подключение любых OpenAI-совместимых эндпоинтов (OpenRouter, ReformBoss, Groq, Cerebras, Ollama) с распределением ролей (`chat`, `vision`, `embedding`) и кастомными HTTP-заголовками/телом запроса.
- 🔐 **Безопасность из коробки**: секреты хранятся в `.env` (в Git не попадают); локальная база SQLite защищена шифрованием AES-256 через SQLCipher (`db_key`).

---

## Быстрый старт

### Системные требования

| Инструмент | Версия | Назначение |
|---|---|---|
| **Go** | 1.22+ | Компиляция и запуск ядра |
| **CMake** | 3.20+ | Система сборки нативных плагинов |
| **GCC / MinGW-w64** | C++20 и C11 | Компилятор для плагинов и CGO |
| **Ninja** | Актуальная | Рекомендуемый генератор для CMake |

---

### Шаг 1: Настройка окружения

Склонируйте репозиторий и создайте локальный конфигурационный файл `.env`:

```bash
cp .env.example .env
```

Заполните переменные окружения в файле `.env`:

```ini
# LLM-провайдеры
OPENROUTER_API_KEY=sk-or-v1-...
REFORMBOSS_API_KEY=...

# Данные для Telegram Userbot (https://my.telegram.org)
SPERMACORE_TELEGRAM_API_HASH=ваш_telegram_api_hash
SPERMACORE_TELEGRAM_PHONE=+79991234567

# Ключи безопасности
SPERMACORE_DB_KEY=пароль-для-шифрования-sqlite
SPERMACORE_WEBUI_TOKEN=секретный-токен-для-webui
```

---

### Шаг 2: Сборка нативных плагинов

Все модули располагаются в каталоге `modules/`. Их можно собрать одной командой через корневой `CMakeLists.txt`:

```bash
# Генерация файлов сборки и компиляция всех модулей
cmake -B build -G Ninja
cmake --build build
```

> **Автоматическая компиляция**: Если плагин указан в секции `load` в `config.toml`, но его `.dll`/`.so` еще не скомпилирован, SpermaCore автоматически найдет исходники в папке `modules/<имя>/src` и запустит CMake при старте!

---

### Шаг 3: Сборка и запуск ядра

```bash
# Сборка исполняемого файла ядра
go build -o spermacore.exe ./core

# Запуск SpermaCore
./spermacore.exe -config config.toml
```

Флаги запуска:
- `-config <path>`: путь к конфигурационному файлу (по умолчанию: `config.toml`).
- `-version`: вывод версии SpermaCore.
- `-rebuild`: очистить артефакты сборки всех модулей и выполнить чистую перекомпиляцию.

---

### Шаг 4: Открытие WebUI дашборда

Перейдите по адресу **[http://127.0.0.1:8080](http://127.0.0.1:8080)**:
- Если задан `SPERMACORE_WEBUI_TOKEN`, введите его в модальном окне авторизации.
- Если Telegram настроен в режиме `login_mode = "qr"`, отсканируйте появившийся QR-код через мобильное приложение Telegram (*Настройки → Устройства → Подключить устройство*).

---

## Модульная экосистема (ABI 1.7.1)

Все возможности агента реализованы в виде модулей в папке `modules/`:

| Модуль | Категория | Описание | Инструменты (Tools / Syscalls) |
|---|---|---|---|
| **`telegram`** | Транспорт | Юзербот на базе TDLib: вход по QR/телефону, реакции, статус «печатает…», умные цитаты, отправка стикеров | `send_message`, `react`, `send_sticker`, `mark_read` |
| **`agentloop`** | Автономность | Фоновый цикл проактивности: запуск диалогов с друзьями, часы бодрствования, виджет в WebUI | `agentloop.cycle`, управление циклом |
| **`react`** | Планировщик | ReAct-цикл (Reason + Act): обработка намерений модели и выполнение тулов OpenAI function calling | Диспетчер тулов модели |
| **`embedsc`** | Память | Векторная семантическая память в SQLite с быстрым графовым индексом HNSW | `embedsc.remember`, `embedsc.recall`, `embedsc.stats` |
| **`usersc`** | Профили | Профили собеседников: сохранение фактов о пользователе, расписания, резолв юзернеймов | `get_user`, `resolve_username` |
| **`sysprompt`** | Личность | Динамический сборщик системного промпта из шаблонов и файлов персонажа | Инъекция системного промпта |
| **`time`** | Утилита | Системные часы с поддержкой таймзон | `get_time` |
| **`websearch`** | Утилита | Поиск информации в DuckDuckGo и загрузка содержимого веб-страниц | `web_search`, `web_fetch` |
| **`media`** | Мультимедиа | Распознавание эмодзи и стикеров через vision-модели, обработка медиа | `media.inspect`, vision-триггеры |
| **`dashboard`** | Телеметрия | Сбор и передача метрик производительности и состояния модулей в WebUI | Хуки телеметрии |
| **`grouper`** | Диспетчер | Группировка серии быстрых сообщений от одного пользователя перед отправкой в LLM | Агрегатор входного потока |
| **`bypasses`** | Сеть | MTProto WebSocket прокси (`tgwsproxy`) для работы в ограниченных сетях | Проксирование TDLib |

---

## Конфигурация (`config.toml`)

Файл `config.toml` разделяет конфигурационную структуру и приватные секреты с помощью подстановки `${VAR_NAME}`:

```toml
# SpermaCore production config

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
login_mode   = "qr"            # "qr", "phone" или "auto"
reply_to     = true            # умное цитирование
device_model = "Desktop"
app_version  = "1.0.0"

[plugins.agentloop]
enabled            = true
friends            = []        # список юзернеймов/ID для проактивного общения
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

## Веб-панель управления (:8080)

Встроенный дашборд предоставляет полный интерактивный контроль:

- **Авторизация по QR-коду**: отображение живого QR-кода Telegram для быстрой привязки юзербота без ввода кодов в терминале.
- **Управление автономным циклом (`agentloop.cycle`)**:
  - Обратный отсчет до следующего автономного действия.
  - Кнопки мгновенного запуска (`Trigger Now`) и паузы/возобновления.
  - Счетчик действий за текущие сутки и статус окна активности.
- **Мониторинг воркеров и очередей**: визуализация заполненности очереди задач, загрузки горутин, расхода памяти и вызовов инструментов.
- **Стриминг терминальных логов**: WebSocket-трансляция логов ядра с фильтрацией по уровням (`DEBUG`, `INFO`, `TOOL`, `WARN`, `ERROR`).
- **Интерактивные виджеты**: динамические компоненты, регистрируемые C++ плагинами через виджетный протокол ABI.

---

## Разработка собственных плагинов (ABI 1.7.1)

Для создания нового плагина достаточно одной динамической библиотеки с точкой входа `plugin_get_api()`.

### Пример: минимальный плагин на C++20

Создайте файл `modules/hello/src/hello.cpp`:

```cpp
#include "plugin_api.h"
#include <string>

static sc_tool_def_t TOOLS[] = {
    {
        .name = "say_hello",
        .description = "Приветствует собеседника по имени",
        .parameters_json = R"({
            "type": "object",
            "properties": {
                "name": {"type": "string", "description": "Имя собеседника"}
            },
            "required": ["name"]
        })",
    },
};

static int hello_call_tool(const char* name, const char* args_json,
                           char** out_json, const sc_host_api_t* host) {
    if (std::string(name) == "say_hello") {
        *out_json = sc_strdup(R"({"greeting": "Привет из нативного C++20 плагина!"})");
        return SC_OK;
    }
    return SC_ERR_NOT_FOUND;
}

static sc_plugin_api_t API = {
    .abi_version = SC_ABI_VERSION,
    .info = {
        .name = "hello",
        .version = "1.0.0",
        .human_description = "Простой приветственный плагин",
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

### Основные правила ABI
1. **Потокобезопасность**: по умолчанию вызовы в плагин синхронизируются внутренним мьютексом ядра. Если плагин потокобезопасен, укажите флаг `SC_PLUGIN_FLAG_THREAD_SAFE` в структуре `.info.flags`.
2. **Управление памятью**: строки для `*out_json` должны выделяться через `sc_strdup()` или `host->alloc()`, чтобы ядро могло корректно освободить память после отправки.
3. **C-линковка**: экспортируемая функция `plugin_get_api` обязательно оборачивается в `extern "C"`.

---

## Структура репозитория

```text
spermacore-clean/
├── core/                     # Исходный код ядра на Go
│   ├── dashboard/            # Сбор статистики и состояние дашборда
│   ├── embedsc/              # Встроенный HNSW графовый индекс
│   ├── grouper/              # Агрегатор пакетов входящих сообщений
│   ├── humanify/             # Движок имитации человека (опечатки, паузы, очереди)
│   ├── media/                # Модуль анализа эмодзи, стикеров и медиа
│   ├── sandbox/              # Валидация и параметры песочницы
│   ├── sidecar/              # Протокол изолированного супервизора процессов
│   ├── webui/                # HTTP/WebSocket сервер WebUI и статические ресурсы
│   ├── addon.go              # Загрузчик плагинов и авто-сборщик CMake
│   ├── cgo_bridge.go         # Высокоскоростной мост CGO к ABI 1.7.1
│   ├── cgo_stub.go           # Заглушка для сборки без CGO (режим sidecar)
│   ├── llm.go                # Мультипровайдерный LLM клиент и роутер
│   ├── react.go              # ReAct планировщик и диспетчер тулов
│   ├── worker.go             # Пул параллельных воркеров и очередь задач
│   └── main.go               # Точка входа приложения
├── data/                     # Промпты, шаблоны и бинарные библиотеки
│   ├── telegram/             # Библиотеки TDLib (tdjson.dll, openssl, zlib)
│   ├── active-phrases.json   # Триггерные фразы для групповых бесед
│   ├── base-prompt.txt       # Базовые системные инструкции и приоритеты
│   └── policy.txt            # Правила поведения персонажа
├── include/                  # Заголовочные файлы ABI
│   ├── json.hpp              # Header-only библиотека JSON
│   └── plugin_api.h          # Контракт плагинов ABI 1.7.1 (C11 / C++20)
├── modules/                  # Каталог нативных C++20 плагинов
├── CMakeLists.txt            # Корневой CMake-скрипт сборки плагинов
├── config.toml               # Основная конфигурация SpermaCore
├── .env.example              # Шаблон переменных окружения и секретов
├── CONTRIBUTING.md           # Правила участия в разработке и стандарты кода
├── go.mod / go.sum           # Зависимости Go
└── README.md                 # Документация проекта
```

---

## Безопасность и секреты

- **Не коммитьте `.env` и файлы сессий `*.session`**: все приватные токены и ключи вынесены в переменные окружения.
- **Шифрование данных**: база SQLite шифруется с помощью SQLCipher алгоритмом AES-256 (`SPERMACORE_DB_KEY`).
- **Защита WebUI**: если `auth_token` не задан, WebUI слушает исключительно loopback (`127.0.0.1`) и генерирует одноразовый ключ доступа в логе запуска.
- **Защита хранилища TDLib**: сессия Telegram дополнительно шифруется отдельным ключом `SPERMACORE_TELEGRAM_DB_KEY`.

---

## Лицензия

Проект распространяется под лицензией **PolyForm Noncommercial License 1.0.0**. См. [LICENSE](LICENSE) или [polyformproject.org](https://polyformproject.org/licenses/noncommercial/1.0.0).

- Разрешено свободное чтение, изменение, изучение и запуск в личных, исследовательских и некоммерческих целях.
- Коммерческое использование, развертывание в виде SaaS или проприетарное встраивание требует отдельного соглашения с автором.
- Плагины, написанные вами, принадлежат вам и не ограничиваются данной лицензией.

Правила отправки пулл-реквестов описаны в [CONTRIBUTING.md](CONTRIBUTING.md).

---

<div align="center">

**SpermaCore Project** · 2026  
*Содружество Принципиальных Единомышленников Ради Мира и Абсолюта*

</div>
