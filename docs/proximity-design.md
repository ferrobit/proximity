# Proximity — принятые решения (версия 1.0.0, 2026-11-14)

Исходное ТЗ: файл «Proximity project» (контейнер Docker + mihomo + веб-интерфейс на порту 8899).
Код отдан архивом `proximity.zip` в чате; в проекте исходники не хранятся.

## Состав
- Сервер: Go 1.24, один бинарник `proximity`, без cgo. Зависимость только для тестов — testify.
- Интерфейс: Vue 3 + Ant Design Vue + Vite, встроен в бинарник через `go:embed` (`web/dist`).
- Пакеты: `config` (модель, хранилище JSON), `rules` (разбор и проверка фильтров),
  `router` (генерация конфигурации mihomo, процесс mihomo, учёт трафика),
  `link` (endpoint'ы: HOST, прокси, OpenVPN, AmneziaWG, мониторинг),
  `auth` (пароли PBKDF2, токены), `server` (HTTP API), `wgconf` (разбор конфигурации WireGuard/AmneziaWG).

## Ключевые решения
1. Endpoint → mihomo: HOST и туннели — прокси `direct` (`interface-name` или `routing-mark`),
   SOCKS5 — `socks5`, HTTP — `http`. Fallback — группа `fallback`.
2. Туннели не трогают основную таблицу маршрутов: у каждого своя таблица `51000 + slot`,
   правило `ip rule fwmark`, запасной маршрут `blackhole` (трафик не утекает при падении VPN).
3. Имена интерфейсов: `proximity_o<slot>` (OpenVPN), `proximity_a<slot>` (AmneziaWG);
   сеть Docker и мост на хосте — `proximity_lan`.
4. OpenVPN запускается в псевдотерминале; ответы на запросы логина/пароля/OTP даёт Proximity.
   Данные о шлюзе, маршрутах и DNS приходят через `--up "proximity hook <файл>"`.
5. Маршруты от VPN — фильтр с пометкой `provided` (только чтение, выключен по умолчанию).
6. HTTP-прокси с отдельными серверами HTTPS/FTP: дополнительные прокси `ID~https`, `ID~ftp`
   и правила `AND,(…),(DST-PORT,443|21)`.
7. Статистика и список недоступных адресов: API mihomo `/connections` и поток `/logs?level=info`
   (не зависит от уровня журнала в настройках). Для туннелей — счётчики интерфейса.
8. Конфигурация mihomo пишется в `config.yaml` в виде JSON; перед применением проверяется `mihomo -t`,
   применяется через `PUT /configs?force=true` по unix-сокету.
9. Версии в Dockerfile: `metacubex/mihomo:v1.19.34`, amneziawg-go `v3.1.20260828`,
   amneziawg-tools `v3.1.20260812`, база `debian:bookworm-slim`.

## Что проверено на деле
- 69 тестов, покрытие 92,8% (по пакетам 79,5–99,1%), `go vet`, `go test -race`.
- Образ собран, контейнер запущен через `docker-compose.yml`: OpenVPN (логин + пароль + OTP),
  AmneziaWG, DNS через VPN, GEOIP/GEOSITE, перезапуск с сохранением настроек.

## Что не проверено
- `docker-compose.host.yml` (режим сети хоста) и фильтры `PROCESS-NAME`.
- macOS и Windows (Docker Desktop), скрипт `proximity.ps1`.
- Динамический запрос OTP (CRV1), устройства `tap`, IPv6 внутри туннелей.
- Импорт ключа `vpn://` и QR-кода на настоящих данных Amnezia.
