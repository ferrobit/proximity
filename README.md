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
| Прокси для программ хоста (HTTP и SOCKS5 на одном порту) | `127.0.0.1:7890` |
| Настройки (том `/config`) | Linux: `~/.config/proximity`, macOS: `~/Library/Application Support/proximity`, Windows: `%APPDATA%\proximity` |

Проверка: `curl -x http://127.0.0.1:7890 https://example.com`.

### Первый вход

При первом старте создаётся администратор `admin`. Пароль — случайный, он печатается
в журнал контейнера и лежит в файле `initial_admin_password.txt` в папке настроек:

```bash
docker logs proximity 2>&1 | grep "created administrator"
```

Свой логин и пароль можно задать заранее переменными `PROXIMITY_ADMIN_USER`
и `PROXIMITY_ADMIN_PASSWORD`.

## Роли

| Роль | Права |
|---|---|
| Administrator | всё, включая пользователей и настройки |
| Operator | правка Endpoints, Fallbacks, Filters |
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

* У каждого туннеля своя таблица маршрутов (`51000 + N`) и правило `ip rule fwmark`.
  Основная таблица контейнера не меняется: `redirect-gateway` от VPN ничего не ломает.
* Если туннель упал, в его таблице остаётся `blackhole` — трафик не утекает мимо VPN.
* OpenVPN запускается в псевдотерминале; Proximity сам отвечает на запросы логина,
  пароля и одноразового кода (TOTP, как `oathtool --totp -b`).
* Маршруты, которые выдал VPN, появляются в Filters как фильтр «provided»
  (только чтение, выключен). Его можно включить и двигать.
* Статистика и список «Unreachable routes» берутся из API mihomo (`/connections`, `/logs`).

## Сеть

* По умолчанию — мост `proximity_lan`. Работает на Linux, macOS и Windows.
  HOST-endpoint видит только интерфейсы контейнера.
* Режим хоста (только Linux): `PROXIMITY_NETWORK=host scripts/proximity.sh up -d --build`.
  Тогда доступны настоящие интерфейсы хоста и фильтры `PROCESS-NAME`. Таблицы
  `51001…60999` и интерфейсы `proximity_o*` / `proximity_a*` создаются прямо на хосте.
  Для туннелей нужен `sysctl net.ipv4.conf.all.rp_filter=2` (или 0).
* Порт прокси опубликован только на `127.0.0.1`. Открыть в локальную сеть:
  `PROXIMITY_PROXY_BIND=0.0.0.0`. Порт в Settings должен совпадать с опубликованным.

## Фильтры

Каждая строка параметров — одно правило mihomo `<Directive>,<параметр>,<Target>`.
Преобразования при сохранении:

| Ввод | Результат |
|---|---|
| `https://Example.com/path` (DOMAIN) | `example.com` |
| `*.example.com` (DOMAIN-SUFFIX) | `example.com` |
| `пример.рф` | `xn--e1afmkfd.xn--p1ai` |
| `192.168.1.7` (IP-CIDR) | `192.168.1.7/32` |
| `10.1.2.3/8` | `10.0.0.0/8` |
| `1.2.3.4:443` | `AND,((IP-CIDR,1.2.3.4/32),(DST-PORT,443)),Target` |
| `5.5.5.5,no-resolve` | `IP-CIDR,5.5.5.5/32,Target,no-resolve` |

Противоречия (дубли, пересечения сетей, домен внутри суффикса) не запрещены —
они показываются предупреждением вверху раздела.

## Сборка и тесты

```bash
make web      # собрать интерфейс (Node.js 22)
make build    # собрать bin/proximity
make cover    # тесты с покрытием
make up       # собрать образ и запустить контейнер
```

Запуск без Docker (нужны `mihomo`, `ip`; для VPN — `openvpn`, `amneziawg-go`, `awg`):

```bash
sudo ./bin/proximity -listen :8899 -config ./config -run /run/proximity
```

Версии компонентов задаются аргументами сборки в `Dockerfile`: `MIHOMO_IMAGE`,
`AWG_GO_VERSION`, `AWG_TOOLS_VERSION`.

## Файлы в папке настроек

| Файл | Содержимое |
|---|---|
| `proximity.json` | пользователи, endpoints, fallbacks, filters, settings |
| `state.json` | счётчики трафика и список недоступных адресов |
| `mihomo/config.yaml` | сгенерированная конфигурация mihomo (JSON — это допустимый YAML) |
| `initial_admin_password.txt` | первый пароль администратора |

Пароли endpoint'ов и конфигурации VPN лежат в `proximity.json` открытым текстом:
папку настроек нужно беречь.
