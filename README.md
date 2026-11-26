# 1C Server Monitor & Watchdog

[![Build & Publish](https://github.com/zigorminsk-debug/1C-Monitor/actions/workflows/ci.yml/badge.svg)](https://github.com/zigorminsk-debug/1C-Monitor/actions/workflows/ci.yml)
![.NET 8.0](https://img.shields.io/badge/.NET-8.0-512BD4)
![Avalonia UI 11.3](https://img.shields.io/badge/Avalonia-11.3.7-8B44AC)
![Platform Windows](https://img.shields.io/badge/Platform-Windows%20x64%20%7C%20x86-0078D6)

Комплекс инструментов для мониторинга производительности, диагностики зависаний, автоматического контроля дублирующихся/зависших сеансов и оперативного администрирования серверов **1С:Предприятие 8** на Windows.

Состоит из двух приложений на базе общей библиотеки `OneCMonitor.Core`:
1. **`1C-Server-Monitor.exe`** — десктопная GUI-утилита мониторинга, диагностики, отчётности и администрирования.
2. **`1C-Watchdog.exe`** — автономная системная служба Windows (`LocalSystem`), запускающаяся вместе с Windows, отслеживающая дублирующиеся и зависшие сеансы 1С через RAC/RAS, завершающая проблемные сеансы и ведущая ротируемые логи + журнал событий Windows.

---

## Возможности

### 1. Десктопный монитор (`1C-Server-Monitor.exe`)
- **Рабочие процессы `rphost`**: отслеживание CPU, RAM (`WorkingSet`), количества сеансов, среднего времени вызовов и запросов к БД, автоматический расчёт индекса риска (`0–100`) и текстового диагноза.
- **Сеансы пользователей через `rac.exe`**: состояние (активный/спящий/выполняет вызов), база, приложение, хост, память и CPU-время RAC, нагрузка и I/O за последние 5 минут, блокировки (`blocked-by-dbms` / `blocked-by-ls`), оценочная доля CPU пользователя.
- **Технологический журнал (ТЖ)**: парсинг последних `.log`-файлов для сопоставления сеансов с контекстом документа (`Context=`) и длительностью последнего вызова.
- **Системные ресурсы сервера**: PDH-счётчики диска (`PhysicalDisk (_Total)`), сетевые адаптеры, гипервизор/физический сервер, модель CPU, RAM, тип накопителей (`NVMe`/`SSD`/`HDD`) и тома.
- **Управление и плавный вывод `rphost`**: ручной и автоматический `process turn-off`, управление фоновыми заданиями, завершение сеансов по категориям, экспорт автономных и суточных HTML-отчётов, CSV-история.

### 2. Системная служба контроля сеансов (`1C-Watchdog.exe`)
- **Автозапуск с Windows (`LocalSystem`)**: работает в фоне независимо от входа пользователя в систему (через `Microsoft.Extensions.Hosting.WindowsServices`), с политикой автовосстановления SCM при сбое.
- **Самодостаточная установка привилегий (`--setup`)**:
  - Автоматический запрос UAC-повышения прав администратора при запуске.
  - Развёртывание в `%ProgramFiles%\1CServerMonitor\Watchdog\1C-Watchdog.exe`.
  - Настройка защищённых прав NTFS ACL (`SYSTEM` и `Administrators`) на каталоги службы и конфигурации.
  - Регистрация источника в журнале событий Windows (`1CServerWatchdog` в `Application`).
  - Шифрование пароля кластера 1С и опционального GitHub-токена через **Windows DPAPI** (`DataProtectionScope.LocalMachine`).
- **Контроль дублирующихся сеансов**:
  - Группировка по `(База, Пользователь, Компьютер, Приложение)` с защитой исключений (`Configurator`, `BackgroundJob`, `SrvrConsole`, `Agent`, `RAS` и настраиваемый список).
  - Сохранение самого нового сеанса и завершение более ранних дубликатов (с учётом минимального возраста сеанса).
- **Контроль зависших сеансов**:
  - Длительные активные вызовы (`duration-current` ≥ порога, по умолчанию `60 мин`).
  - Длительные блокировки (`blocked-by-dbms` / `blocked-by-ls` ≥ порога, по умолчанию `15 мин`).
  - Длительные спящие сеансы (`hibernate=yes`, настраиваемый порог, по умолчанию `180 мин`, можно отключить значением `0`).
  - Режим `DryRun` для безопасной предварительной проверки политик по логам без реального завершения сеансов.
- **Ведение журналов**:
  - Ежедневные файлы логов `%ProgramData%\1CServerMonitor\logs\watchdog-YYYY-MM-DD.log` с автоматическим удалением старых файлов (по умолчанию 30 дней).
  - Дублирование критичных событий в Windows Event Log (`1CServerWatchdog`).

### 3. Автообновление с GitHub Releases
- Поддерживается как для **`1C-Server-Monitor.exe`**, так и для службы **`1C-Watchdog.exe`** (включая остановку/обновление/перезапуск службы Windows).
- Периодическая проверка `https://api.github.com/repos/<owner>/<repo>/releases/latest` (по умолчанию раз в 6 часов) и ручная проверка из UI / CLI (`--check-update`).
- Проверка целостности загруженного архива по `SHA256SUMS.txt` из релиза.
- Поддержка **приватных репозиториев GitHub** через опциональный Personal Access Token (сохраняется на диск только в зашифрованном виде через Windows DPAPI).

---

## Требования

### Для запуска на сервере 1С
- **ОС**: Windows 10 / Windows 11 / Windows Server 2016+ (`x64` или `x86`).
- **Права**: запуск **от имени администратора** (для установки службы `1C-Watchdog.exe --setup` выполняется автоматический запрос UAC).
- **Платформа 1С:Предприятие 8**: наличие `rac.exe` и `ras.exe` на сервере и запущенного `ragent.exe`.
- При использовании self-contained сборки установка .NET Runtime на сервер не требуется.

### Для сборки из исходного кода
- [.NET 8.0 SDK](https://dotnet.microsoft.com/download/dotnet/8.0)

---

## Быстрый старт

### Десктопный монитор
1. Скачайте `OneCServerMonitor-win-x64.zip` (или `win-x86`) из раздела **Releases** и распакуйте архив (внутри находятся `1C-Server-Monitor.exe` и `1C-Watchdog.exe`).
2. Запустите `1C-Server-Monitor.exe` **от имени администратора** на сервере 1С.
3. На вкладке **Настройки** при необходимости нажмите **Запустить RAS**, укажите учётную запись администратора кластера и параметры автообновления GitHub, затем нажмите **Сохранить и проверить**.

### Установка службы Watchdog (`LocalSystem`)
Вариант 1 — из интерфейса: откройте вкладку **Администрирование** в `1C-Server-Monitor.exe` и нажмите **Установить / настроить службу 1C Watchdog (LocalSystem)**.

Вариант 2 — из консоли или PowerShell:
```powershell
.\1C-Watchdog.exe --setup
```
Или для автоматической установки без интерактивных вопросов:
```powershell
.\scripts\install-watchdog.ps1 -Silent -Endpoint "127.0.0.1:1545" -AgentEndpoint "127.0.0.1:1540" -ClusterUser "admin" -ClusterPassword "secret"
```

Управление службой из CLI:
- `1C-Watchdog.exe --status` — показать статус службы, пути логов и конфигурацию.
- `1C-Watchdog.exe --console` — запуск в консольном режиме для отладки.
- `1C-Watchdog.exe --check-update` — проверить обновление на GitHub.
- `1C-Watchdog.exe --uninstall` — остановить и удалить службу Windows.

---

## Конфигурация и данные программы

| Назначение | Путь по умолчанию |
|---|---|
| Настройки GUI-монитора (GitHub-токен шифруется DPAPI) | `%LocalAppData%\1CServerMonitor\settings.json` |
| Суточная история метрик GUI (CSV) | `%LocalAppData%\1CServerMonitor\history\history-YYYY-MM-DD.csv` |
| Диагностика RAC | `%LocalAppData%\1CServerMonitor\last-rac-diagnostic.txt` |
| Конфигурация службы Watchdog (секреты шифруются DPAPI `LocalMachine`) | `%ProgramData%\1CServerMonitor\watchdog.json` |
| Логи службы Watchdog (ротация по дням) | `%ProgramData%\1CServerMonitor\logs\watchdog-YYYY-MM-DD.log` |
| Исполняемый файл установленной службы Watchdog | `%ProgramFiles%\1CServerMonitor\Watchdog\1C-Watchdog.exe` |
| Суточные HTML-отчёты | `%UserProfile%\Documents\1CServerMonitor\Reports\1C-daily-<SERVER>-YYYY-MM-DD.html` |

---

## Сборка из исходного кода

```bash
dotnet restore OneCMonitor.sln
dotnet build OneCMonitor.sln -c Release
```

Сборка и упаковка релизных сборок (GUI + Watchdog для `win-x64` и `win-x86`, формирование `SHA256SUMS.txt`):
```powershell
.\scripts\publish.ps1
```

---

## Структура репозитория

```text
.
├── .github/                  # CI/CD workflows и шаблоны Issue / PR
├── OneCMonitor.Core/         # Общая библиотека (RAC/RAS, анализ сеансов, DPAPI, автообновление GitHub)
├── OneCMonitor/              # Десктопный GUI-монитор (.NET 8 + Avalonia UI)
├── OneCWatchdog/             # Системная служба Windows 1C Watchdog (LocalSystem)
├── scripts/                  # Скрипты сборки (publish.ps1) и установки службы (install-watchdog.ps1)
├── .editorconfig             # Единые правила форматирования кода
├── .gitattributes            # Нормализация окончаний строк и бинарных файлов
├── .gitignore                # Исключения Git для .NET и пользовательских данных
├── CHANGELOG.md              # История изменений и релизов
├── CONTRIBUTING.md           # Правила внесения изменений
├── OneCMonitor.sln           # Файл решения Visual Studio / .NET CLI
├── README.md                 # Документация проекта
└── SECURITY.md               # Политика безопасности и рекомендации по эксплуатации
```

---

## Контакты и авторские права

- **Разработчик**: Zakharevich Igor
- **Организация**: ООО «Компьютерная безопасность»
- **Сайт**: [csl.by](https://csl.by)
- **Email**: [info@csl.by](mailto:info@csl.by)
- **Телефон**: +375 29 337 14 12
- **Адрес**: Республика Беларусь, г. Минск, ул. Фабрициуса, 9

© 2026 Zakharevich Igor · csl.by
