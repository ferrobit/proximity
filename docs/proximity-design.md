# Proximity — принятые решения (версия 1.1.0, 2026-11-15)

Исходное ТЗ: файл «Proximity project» (контейнер Docker + mihomo + веб-интерфейс на порту 8899).
Код отдан архивом `proximity.zip` в чате; в проекте исходники не хранятся.

## Состав
- Сервер: Go 1.24, один бинарник `proximity`, без cgo. Зависимость только для тестов — testify.
- Интерфейс: Vue 3 + Ant Design Vue + Vite, встроен в бинарник через `go:embed` (`web/dist`).
- Пакеты: `config` (модель, хранилище JSON, резервная копия), `rules` (разбор и проверка фильтров),
  `router` (генерация конфигурации mihomo, процесс mihomo, учёт трафика),
  `link` (endpoint'ы: HOST, прокси, OpenVPN, AmneziaWG, мониторинг, захват трафика хоста),
  `auth` (пароли PBKDF2, токены), `server` (HTTP API), `wgconf` (разбор конфигурации WireGuard/AmneziaWG).

## Ключевые решения
1. Endpoint → mihomo: HOST и туннели — прокси `direct` (`interface-name` или `routing-mark`),
   SOCKS5 — `socks5`, HTTP — `http`.
2. Туннели не трогают основную таблицу маршрутов: у каждого своя таблица `51000 + slot`,
   правило по метке, правило по адресу туннеля (работает и при строгом `rp_filter=1`),
   запасной маршрут `blackhole`.
3. Имена интерфейсов: `proximity_o<slot>` (OpenVPN), `proximity_a<slot>` (AmneziaWG),
   `proximity_tun` (захват); сеть Docker и мост в режиме bridge — `proximity_lan`.
4. OpenVPN запускается в псевдотерминале; ответы на запросы логина/пароля/OTP даёт Proximity.
5. Маршруты от VPN — фильтр с пометкой `provided` (только чтение, выключен по умолчанию).
6. HTTP-прокси с отдельными серверами HTTPS/FTP: прокси `ID~https`, `ID~ftp` и правила по порту.
7. Статистика и недоступные адреса: API mihomo `/connections` и поток `/logs?level=info`.
8. Конфигурация mihomo — JSON в `config.yaml`; проверка `mihomo -t`, применение через unix-сокет.
9. Версии в Dockerfile: `metacubex/mihomo:v1.19.34`, amneziawg-go `v3.1.20260828`,
   amneziawg-tools `v3.1.20260812`, база `debian:bookworm-slim`.

## Добавлено в 1.1.0
1. Порт прокси по умолчанию — 7899.
2. Linux: `docker-compose.yml` = сеть хоста + захват всего исходящего трафика хоста.
   macOS/Windows: `docker-compose.bridge.yml` (прокси-порт). Скрипт выбирает файл сам.
3. Захват: mihomo `tun` (устройство `proximity_tun`, стек gvisor, `auto-route` выключен),
   правила ставит Proximity: 5150 `fwmark 51000 → main` (обход для mihomo и транспорта туннелей),
   5155 DNS хоста (`iif lo`, UDP/TCP 53 → таблица 51000), 5160 `main suppress_prefixlength 0`
   (локальная сеть напрямую), 5170 `from 0.0.0.0 iif lo → 51000`.
   Домены — из fake-ip DNS (запросы хоста к DNS перехватываются) и сниффера (TLS SNI,
   HTTP Host, QUIC).
4. Папки настроек: Linux `~/.config/proximity`, macOS `~/Library/Application Support/Proximity`,
   Windows `%APPDATA%\Proximity`.
5. Первый вход: диалог «Create Administrator Account» (`POST /api/setup`), пароль не генерируется.
   Переменные `PROXIMITY_ADMIN_USER`/`PROXIMITY_ADMIN_PASSWORD` по-прежнему работают.
6. Повтор пароля во всех диалогах учётных записей (сервер тоже сверяет поле `confirm`).
7. Предупреждения фильтров — только когда верхнее правило перекрывает нижнее или дубль.
8. ID фильтра по умолчанию `Filter <N>`. ID endpoint'ов, fallback'ов и фильтров редактируются;
   ссылки обновляются каскадом; туннель при переименовании не рвётся (ключ — slot).
   Endpoint и fallback не могут называться одинаково (оба — цели фильтров).
9. HOST: `attempts` (3) и `reconnect_timeout` (10 с); после исчерпания — `failed`, проверки продолжаются.
10. Fallback: `health_check` (по умолчанию выключен). Выключен — группа `select` из endpoint'ов
    в состоянии `up` по данным Proximity; включён — группа `fallback` с URL-проверкой mihomo.
11. Settings: `connectivity_checks` (по умолчанию включён), блок «Backup & Restore»
    (`GET/POST /api/backup`, роли Administrator и Operator; учётные записи в файл не входят;
    импорт оператором не меняет Settings).
12. Журнал AmneziaWG: вывод `amneziawg-go` (LOG_LEVEL=verbose) + шаги настройки + `awg show`.
    Окно журнала обновляется каждые 1,5 с и прокручивается само.

## Комментарии в параметрах фильтров
1. Строка, у которой первый непробельный символ `#`, — комментарий (`rules.IsComment`).
   Комментарий в конце строки с правилом не поддерживается.
2. `rules.ParseText` отдаёт правила и нормализованный текст; комментарии остаются
   на своих местах (обрезаются только пробелы по краям). `rules.Parse` комментарии пропускает.
3. Сохранение фильтра и `POST /api/filters/preview` возвращают параметры вместе с комментариями;
   в mihomo и в проверку перекрытий комментарии не попадают.
4. Фильтр из одних комментариев — ошибка `rule parameters do not exist`.
5. Интерфейс: подпись поля упоминает `#`, в таблице комментарии приглушены.

## Что проверено на деле (1.1.0)
- 82 теста, покрытие 92,7% (по пакетам 80,5–99,1%), `go vet`, `go test -race`.
- Захват проверен в отдельном сетевом пространстве («хост» + «серверы»), в том числе
  настоящим образом Docker: обычная программа → DIRECT, REJECT, домен → OpenVPN / AmneziaWG,
  локальная сеть и входящие соединения не затронуты, `rp_filter=1`, остановка и kill контейнера.

## Что не проверено
- `docker-compose.yml` с настоящим `network_mode: host` на реальной машине, `PROCESS-NAME`.
- Взаимодействие захвата с systemd-resolved, NetworkManager, firewalld, другими VPN на хосте.
- macOS и Windows (Docker Desktop), скрипт `proximity.ps1`.
- Захват IPv6, динамический запрос OTP (CRV1), устройства `tap`.
- Импорт ключа `vpn://` и QR-кода на настоящих данных Amnezia.
