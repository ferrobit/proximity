# Proximity

Контейнер Docker, который раздаёт исходящие соединения хоста по разным «выходам»
(прямой интернет, прокси, VPN) по правилам: домен, IP, IP:порт, страна (GeoIP) и т. д.
Внутри работает маршрутизатор [mihomo](https://github.com/MetaCubeX/mihomo), а Proximity
управляет им, туннелями и даёт веб-интерфейс.

## Быстрый старт

```bash
scripts/proximity.sh up -d --build      # Linux и macOS
.\scripts\proximity.ps1 up -d --build   # Windows (PowerShell)
```

| Что | Где |
|---|---|
| Веб-интерфейс | http://localhost:8899 |
| Прокси (HTTP и SOCKS5 на одном порту) | `127.0.0.1:7899` |
| Настройки, Linux | `~/.config/proximity` |
| Настройки, macOS | `~/Library/Application Support/Proximity` |
| Настройки, Windows | `%APPDATA%\Proximity` |

### Первый вход

При первом открытии интерфейс показывает диалог «Create Administrator Account»:
имя, пароль и повтор пароля (роль — всегда Administrator). Пароль нигде не генерируется.
Для автоматической установки можно заранее задать `PROXIMITY_ADMIN_USER`
и `PROXIMITY_ADMIN_PASSWORD` — тогда диалог не появится.

## Режимы сети

| Режим | Файл | Что делает |
|---|---|---|
| Linux (по умолчанию) | `docker-compose.yml` | контейнер в сети хоста; **весь исходящий трафик хоста** идёт через Proximity, программам не нужны настройки прокси |
| macOS, Windows | `docker-compose.bridge.yml` | отдельная сеть `proximity_lan`; программы указывают прокси `127.0.0.1:7899` |

Скрипт `scripts/proximity.sh` выбирает файл сам. На Linux без захвата трафика:
`PROXIMITY_NETWORK=bridge scripts/proximity.sh up -d --build`.

### Как устроен захват трафика на Linux

* mihomo создаёт интерфейс `proximity_tun`. Proximity добавляет правила `ip rule`
  (приоритеты 5150–5170) и таблицу 51000 с маршрутом в этот интерфейс.
* Захватываются только новые исходящие соединения. Локальная сеть, сети Docker и входящие
  соединения (SSH, сам интерфейс Proximity) идут как раньше.
* Домен для фильтров DOMAIN берётся из DNS (fake-ip) и самого соединения (TLS SNI,
  заголовок HTTP Host, QUIC).
* DNS-запросы хоста перехватываются: имя разрешает цель правила — SOCKS5/HTTP-прокси
  удалённо, туннель через DNS туннеля, DIRECT через системный DNS. Поэтому имена,
  известные только внутри прокси или VPN, работают и без настройки прокси в программе.
* В режиме без захвата (macOS/Windows) программы указывают прокси `127.0.0.1:7899`;
  тогда имя разрешает Proximity через DNS цели правила.
* Собственный трафик mihomo и туннелей помечен меткой 51000 и в захват не попадает.
* При остановке контейнера правила удаляются. Если процесс убит — интерфейс исчезает
  вместе с маршрутом, и трафик идёт обычным путём.
* Выключается в Settings → [Router] → «Route all outgoing connections…».
* IPv6 захватывается только при включённом флажке IPv6 в Settings.

## Роли

| Роль | Права |
|---|---|
| Administrator | всё, включая пользователей и настройки |
| Operator | правка Endpoints, Fallbacks, Filters; экспорт и импорт конфигурации |
| User | только просмотр (секреты скрыты) |
| Anonymous | только Dashboard (отключается в Settings) |

## Как устроено

| Endpoint | Как подключён к mihomo |
|---|---|
| HOST Internet | прокси `direct`; для интерфейса — `interface-name`, со шлюзом — метка `routing-mark` |
| SOCKS5 Proxy | прокси `socks5` |
| HTTP Proxy | прокси `http` (туннель CONNECT); отдельные серверы HTTPS/FTP — по порту 443/21 |
| OpenVPN | процесс `openvpn` на интерфейсе `proximity_o<N>` + `direct` с `routing-mark` |
| AmneziaWG | `amneziawg-go` на интерфейсе `proximity_a<N>` + `direct` с `routing-mark` |

* У каждого туннеля своя таблица маршрутов (`51000 + N`), правило по метке и правило
  по адресу туннеля. Основная таблица не меняется.
* Если туннель упал, в его таблице остаётся `blackhole` — трафик не утекает мимо VPN.
* OpenVPN запускается в псевдотерминале; Proximity сам отвечает на запросы логина,
  пароля и одноразового кода (TOTP, как `oathtool --totp -b`).
* Маршруты, которые выдал VPN, появляются в Filters как фильтр «provided»
  (только чтение, выключен). Его можно включить и двигать.
* HOST endpoint: после «Connection attempts» неудач подряд (пауза — «Reconnection timeout»)
  переходит в `failed`, но проверки продолжаются — при появлении связи он сам вернётся в `good`.
* Fallback без «Enable health check» берёт первый endpoint в состоянии `good`
  (по данным Proximity). С флажком — mihomo сам опрашивает URL через каждый endpoint.

## Идентификаторы

* ID endpoint'ов, fallback'ов и фильтров можно менять. При переименовании ссылки
  (состав fallback'ов, цели фильтров, цель по умолчанию) обновляются сами, туннель не рвётся.
* ID уникален внутри раздела. Endpoint и fallback дополнительно не могут называться
  одинаково: оба — цели фильтров.
* Пустой ID фильтра заменяется на `Filter <N>`.

## Фильтры

Каждая строка параметров — одно правило mihomo `<Directive>,<параметр>,<Target>`.

| Ввод | Результат |
|---|---|
| `https://Example.com/path` (DOMAIN) | `example.com` |
| `*.example.com` (DOMAIN-SUFFIX) | `example.com` |
| `пример.рф` | `xn--e1afmkfd.xn--p1ai` |
| `192.168.1.7` (IP-CIDR) | `192.168.1.7/32` |
| `10.1.2.3/8` | `10.0.0.0/8` |
| `1.2.3.4:443` | `AND,((IP-CIDR,1.2.3.4/32),(DST-PORT,443)),Target` |
| `5.5.5.5,no-resolve` | `IP-CIDR,5.5.5.5/32,Target,no-resolve` |

Предупреждение появляется, только когда верхнее правило полностью перекрывает нижнее
(нижнее никогда не сработает) или правила совпадают. Порядок «частное выше общего» —
норма: `im-squadus.elvees.com → DIRECT`, ниже `elvees.com → OpenVPN 1`.

## Резервная копия

Settings → [Backup & Restore]: «Export configuration» сохраняет один файл JSON
(endpoints с паролями и ключами VPN, fallbacks, filters, settings), «Import configuration»
заменяет текущие. Учётные записи в файл не входят. При импорте оператором настройки
(Settings) остаются прежними.

## Сборка и тесты

```bash
make web      # собрать интерфейс (Node.js 22)
make build    # собрать bin/proximity
make cover    # тесты с покрытием
make up       # собрать образ и запустить контейнер
```

Версии компонентов задаются аргументами сборки в `Dockerfile`: `MIHOMO_IMAGE`,
`AWG_GO_VERSION`, `AWG_TOOLS_VERSION`.

## Файлы в папке настроек

| Файл | Содержимое |
|---|---|
| `proximity.json` | пользователи, endpoints, fallbacks, filters, settings |
| `state.json` | счётчики трафика и список недоступных адресов |
| `mihomo/config.yaml` | сгенерированная конфигурация mihomo (JSON — это допустимый YAML) |

Пароли endpoint'ов и конфигурации VPN лежат в `proximity.json` открытым текстом:
папку настроек нужно беречь.
