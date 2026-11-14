# SpermaCore

<p align="left">
  <img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.22+">
  <img src="https://img.shields.io/badge/C%2B%2B-20-00599C?style=flat-square&logo=c%2B%2B&logoColor=white" alt="C++20">
  <img src="https://img.shields.io/badge/ABI-v1.7.1-8b5cf6?style=flat-square" alt="ABI v1.7.1">
  <img src="https://img.shields.io/badge/Telegram-TDLib-26A5E4?style=flat-square&logo=telegram&logoColor=white" alt="TDLib">
  <img src="https://img.shields.io/badge/Storage-SQLCipher-003B57?style=flat-square&logo=sqlite&logoColor=white" alt="SQLCipher">
  <img src="https://img.shields.io/badge/license-PolyForm%20Noncommercial-0ea5e9?style=flat-square" alt="PolyForm License">
</p>

Модульный, отказоустойчивый рантайм для автономных LLM-ассистентов и Telegram-юзерботов на Go 1.22+ и C++20.

[Русская версия](README.ru.md) | [English version](README.md)

---

## Архитектура и принципы

SpermaCore построен вокруг строгого разделения обязанностей между ядром и плагинами. Вместо монолитного приложения логика разделена на два уровня:

* **Ядро (Go 1.22+)**: Неблокирующий диспетчер событий, пул воркеров, роутинг LLM-запросов к разным провайдерам (OpenRouter, Groq, Ollama, Gemini Vision), база SQLite с шифрованием SQLCipher и веб-панель управления WebUI на порту 8080.
* **Нативные модули (C++20 Shared Libraries)**: Высокопроизводительные библиотеки, работающие через стабильный C ABI (`include/plugin_api.h`). Включают транспорт Telegram TDLib, векторную память HNSW (`embedsc`), цикл автономной инициативы (`agentloop`), предобработку медиа и стикеров, группировку сообщений и веб-поиск.

При аварийном завершении любого C++ модуля ядро Go перехватывает сбой, переводит воркер в безопасный режим и продолжает обслуживать остальные сессии без падения всего сервиса.

---

## Структура проекта

```text
spermacore/
├── core/              # Рантайм на Go: диспетчер, роутер моделей, хранилище SQLCipher, WebUI
├── include/           # Заголовочные файлы стабильного C ABI (plugin_api.h)
├── modules/           # Нативные динамические модули на C++20
├── landing/           # Веб-страница документации и промо-лендинг
├── releases/          # Скомпилированные бинарники и архив modules.zip
├── config.toml        # Конфигурация провайдеров, параметров и модулей
├── .env.example       # Шаблон ключей и переменных окружения
└── CMakeLists.txt     # Корневой CMake для сборки C++ плагинов
```

Ядро взаимодействует с модулями через стандартные указатели функций C (`plugin_get_api()`). Каждый плагин предоставляет методы жизненного цикла (`init`, `start`, `stop`, `destroy`) и обработчики событий без лишнего копирования данных между языковыми границами.

---

## Быстрый старт

### Требования
* Go версии 1.22 или выше.
* CMake 3.20+ и компилятор C++20 (GCC, Clang или MinGW).
* Библиотеки TDLib (`tdjson.dll` под Windows или `libtdjson.so` под Linux) в каталоге `data/telegram/`.

### 1. Установка и управление модулями

Рекомендуемый способ распаковки, проверки подписей и управления модулями — через менеджер **[scpkg](https://github.com/SporaDev/scpkg)**:

```bash
# Быстрый bootstrap официальных модулей и компиляция
scpkg bootstrap

# Или установка отдельных плагинов из реестра
scpkg install telegram
scpkg install websearch
```

Либо распакуйте архив `releases/modules.zip` вручную:

```bash
# Linux / macOS
unzip releases/modules.zip

# Windows (PowerShell)
Expand-Archive -Path releases/modules.zip -DestinationPath . -Force
```

### 2. Настройка переменных окружения
Скопируйте шаблон `.env.example` в `.env` и задайте необходимые ключи:

```bash
cp .env.example .env
```

Основные переменные:
```ini
OPENROUTER_API_KEY=sk-or-v1-...
SPERMACORE_TELEGRAM_API_ID=12345678
SPERMACORE_TELEGRAM_API_HASH=abcdef0123456789...
SPERMACORE_DB_KEY=your_secure_db_key
```

### 3. Сборка и запуск

Запуск напрямую через Go (если скомпилированных `.dll` или `.so` файлов нет в папках модулей, рантайм Go соберет их через CMake автоматически):

```bash
go run ./core
```

Либо сборка бинарника:
```bash
go build -o releases/spermacore.exe ./core
./releases/spermacore.exe
```

Панель управления WebUI доступна по адресу `http://localhost:8080`.

---

## Доступные модули

| Модуль | Язык | Назначение |
| :--- | :--- | :--- |
| `telegram` | C++20 | Клиент TDLib (QR-авторизация, отправка текста/фото/голосовых, эмуляция набора) |
| `embedsc` | C++20 | Локальный векторный HNSW граф для семантической долговременной памяти |
| `agentloop` | C++20 | Фоновый цикл саморефлексии и проактивных действий |
| `media` | C++20 | Распознавание эмодзи, стикеров и подготовка медиа для мультимодальных моделей |
| `grouper` | C++20 | Батчинг серии коротких сообщений и анти-флуд защита |
| `moderation` | C++20 | Ограничение рейтов, белые и черные списки пользователей |
| `websearch` | C++20 | Легковесный веб-поиск информации для промптов |
| `dashboard` | C++20 | Сбор метрик и потоковая передача телеметрии в WebUI |

Список активных модулей настраивается в секции `[plugins]` файла `config.toml`.

---

## Безопасность

* Ключи и токены загружаются исключительно из окружения или `.env`.
* Базы SQLite шифруются через SQLCipher ключом `SPERMACORE_DB_KEY`.
* Если токен WebUI не задан явно, сервер слушает только loopback (`127.0.0.1`) и создает одноразовый токен сессии.

---

## Лицензия

Распространяется под лицензией PolyForm Noncommercial License 1.0.0. Подробнее см. в [LICENSE](LICENSE) или на [polyformproject.org](https://polyformproject.org/licenses/noncommercial/1.0.0).
