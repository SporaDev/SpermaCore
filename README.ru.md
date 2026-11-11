# SpermaCore

Модульный движок для персональных LLM-ботов и юзерботов на Go и C++20.

[Русская версия](README.ru.md) | [English version](README.md)

---

## Что это такое

SpermaCore: это легковесный рантайм для запуска автономных ИИ-ассистентов и Telegram-юзерботов.

Вместо монолитного приложения, где логика промптов, циклы планирования и сетевые протоколы жестко зашиты в один бинарник, SpermaCore разделяет ответственность:
* Ядро (Go): пул воркеров, очередь событий, роутинг LLM-запросов (OpenRouter, Groq, Ollama), зашифрованное хранилище SQLite и веб-панель управления WebUI на порту 8080.
* Модули (динамические библиотеки C++20): транспорт Telegram TDLib, долговременная векторная память (HNSW / RAG), фоновый цикл проактивности, анализ медиа и кастомные инструменты.

Если модуль падает или не загружен, ядро продолжает стабильно работать в безопасном режиме.

---

## Установка модулей

C++ модули упакованы в архив `releases/modules.zip`, чтобы репозиторий оставался компактным и не засорялся исходниками плагинов.

### 1. Распаковка архива
Распакуйте `releases/modules.zip` в корневую директорию проекта:

```bash
# На Linux / macOS:
unzip releases/modules.zip

# На Windows (PowerShell):
Expand-Archive -Path releases/modules.zip -DestinationPath . -Force
```

После распаковки в корне появится папка `modules/` с исходным кодом плагинов:
* `modules/telegram`: транспорт юзербота Telegram TDLib (вход по QR-коду, отправка сообщений, реакции, эмуляция набора текста)
* `modules/embedsc`: векторная память (индексация HNSW с базой SQLite)
* `modules/agentloop`: автономный фоновый цикл инициативы
* `modules/grouper`: группировка сообщений и защита от спама
* `modules/media`: распознавание эмодзи и стикеров
* `modules/dashboard`: телеметрия модулей и метрики для WebUI
* `modules/moderation`: фильтрация входящих сообщений и лимиты
* `modules/websearch`: инструмент поиска в интернете

### 2. Сборка модулей

Собрать модули можно двумя способами:

#### Вариант А: Автоматическая сборка (рекомендуется)
При запуске SpermaCore ядро проверяет плагины из списка `config.toml`. Если скомпилированного файла (`.dll` или `.so`) нет, рантайм Go сам вызовет CMake и соберет модуль из папки `modules/<name>/src`.
Для этого в системе должны быть установлены CMake и компилятор с поддержкой C++20 (GCC, Clang или MinGW).

#### Вариант Б: Ручная сборка через CMake
Вы можете скомпилировать все модули одной командой через корневой `CMakeLists.txt`:

```bash
cmake -B build -G Ninja
cmake --build build
```

Скомпилированные библиотеки будут сохранены в соответствующих директориях `modules/<name>/`.

### 3. Включение модулей в конфиге
Откройте `config.toml` и укажите нужные плагины в секции `[plugins]`:

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

## Быстрый старт

### Требования
* Go версии 1.22 или выше
* CMake 3.20+ и компилятор C++20 (GCC / Clang / MinGW) для сборки модулей
* Библиотеки TDLib (`tdjson.dll` под Windows или `libtdjson.so` под Linux) в папке `data/telegram/`

### 1. Настройка переменных окружения
Скопируйте шаблон и укажите свои ключи:

```bash
cp .env.example .env
```

Заполните переменные:
```ini
OPENROUTER_API_KEY=sk-or-v1-...
SPERMACORE_TELEGRAM_API_ID=your_api_id
SPERMACORE_TELEGRAM_API_HASH=your_api_hash
SPERMACORE_TELEGRAM_PHONE=+79991234567
SPERMACORE_DB_KEY=your_secure_db_key
```

### 2. Настройка параметров
В файле `config.toml` настройте модели LLM, роутинг зрения и список активных плагинов.

### 3. Сборка и запуск
Запуск напрямую через Go:
```bash
go run ./core
```

Либо компиляция бинарника:
```bash
go build -o releases/spermacore.exe ./core
./releases/spermacore.exe
```

Панель управления WebUI доступна в браузере по адресу `http://localhost:8080`.

---

## Архитектура

```text
spermacore/
├── core/              # Движок на Go (воркеры, очередь событий, роутер LLM, WebUI)
├── include/           # Заголовочные файлы ABI (plugin_api.h)
├── releases/          # Бинарники и архив модулей modules.zip
├── config.toml        # Конфигурация приложения
├── .env.example       # Шаблон переменных окружения
└── CMakeLists.txt     # Корневой CMake файл для сборки модулей
```

### Плагинный ABI (1.7.1)
Связь между ядром Go и плагинами осуществляется через стабильный C ABI, описанный в `include/plugin_api.h`.
Каждый модуль экспортирует точку входа `plugin_get_api()`, возвращающую структуру с указателями на функции жизненного цикла (`init`, `start`, `stop`, `destroy`), транспорта и инструментов.

---

## Безопасность

* Все секреты и токены загружаются исключительно из переменных окружения или `.env`. Не сохраняйте их в репозиторий.
* Локальные базы данных SQLite шифруются через SQLCipher ключом `SPERMACORE_DB_KEY`.
* Если параметр `auth_token` в `config.toml` не задан, WebUI слушает только loopback (`127.0.0.1`) и создает одноразовый ключ сессии при старте.

---

## Лицензия

Распространяется под лицензией PolyForm Noncommercial License 1.0.0. Подробнее см. в [LICENSE](LICENSE) или на [polyformproject.org](https://polyformproject.org/licenses/noncommercial/1.0.0).
