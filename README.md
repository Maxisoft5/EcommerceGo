# маркетплейс на Go с отдельными аккаунтами покупателей и продавцов, товарами магазинов, корзиной, заказами и обновлением ограниченных остатков у всех посетителей. FlashCart остаётся именем backend-проекта: шесть отдельно запускаемых Go-сервисов используют PostgreSQL, Kafka, Redis, OpenSearch и ClickHouse. Интерфейс показывает покупательские сценарии; архитектурные термины находятся в документации.

![Витрина mesto](docs/screenshots/mesto-home.jpg)

[Кабинет продавца с реальной продажей](docs/screenshots/mesto-seller.jpg) · [Заказ покупателя](docs/screenshots/mesto-order.jpg)

## Навигация

- [Руководство пользователей: покупатели, продавцы и администраторы](USER_GUIDE.md).
- [Техническое руководство: развёртывание, стек, архитектура, нагрузка и масштабирование](TECHNICAL_GUIDE.md).
- [Разбор всех 13 требований: технология → модуль → проблема → проверка](TECHNOLOGIES.md).
- [Распределённые системы, гарантии и ADR](docs/ARCHITECTURE.md).
- [Модель угроз и безопасность](docs/SECURITY.md).
- [Эксплуатация, отказы, replay и восстановление](docs/RUNBOOK.md).
- [Нагрузочное тестирование и форма отчёта](docs/PERFORMANCE.md).
- [Фактически выполненные проверки](docs/VALIDATION.md).
- [Сценарий демонстрации и вопросы для интервью](docs/INTERVIEW_GUIDE.md).
- [HTTP-контракт OpenAPI](api/openapi.yaml), [Protobuf](api/proto/inventory/v1/inventory.proto).

## Что можно сделать сейчас

1. Зарегистрировать пользователя и войти: access JWT + ротация refresh token.
2. Открыть витрину, найти товар через OpenSearch, собрать корзину.
3. Создать заказ и сразу получить `202 Accepted` со статусом `pending`.
4. Увидеть `confirmed` или `rejected` через WebSocket без перезагрузки страницы.
5. Повторить запрос с тем же `Idempotency-Key`: новый заказ не появится. Изменить тело с прежним ключом: получить `409`.
6. Попытаться прочитать чужой заказ: получить `404`.
7. Остановить Inventory: новые заказы сохраняются как `pending`. После запуска обработка продолжается.
8. Остановить Kafka: PostgreSQL сохраняет заказ и outbox. После восстановления события доходят до потребителей.
9. Войти администратором и увидеть итоговую аналитику ClickHouse.
10. Запустить конкурентный тест: из 40 покупателей при остатке 5 подтверждаются ровно 5, повторные резервы не списывают товар снова.

## Архитектура

```mermaid
flowchart LR
  Browser[Browser / API client] -->|HTTPS REST JSON + WSS| Gateway
  Gateway --> Identity[Identity / JWT]
  Gateway --> Catalog
  Gateway --> Orders
  Gateway --> Projection[Projection API]
  Identity --> Users[(Identity PostgreSQL)]
  Catalog --> Products[(Catalog PostgreSQL + outbox)]
  Catalog -->|cache aside| Redis[(Redis)]
  Orders -->|FNV1a user_id mod 2| S0[(Orders PostgreSQL shard 0)]
  Orders --> S1[(Orders PostgreSQL shard 1)]
  Products -->|outbox relay| Kafka[(Kafka / 12 partitions)]
  S0 -->|outbox relay| Kafka
  S1 -->|outbox relay| Kafka
  Kafka -->|orders-saga-v1| Orders
  Orders -->|gRPC Protobuf + mTLS| Inventory
  Inventory --> Stock[(Inventory PostgreSQL)]
  Stock -->|stock outbox| Kafka
  Kafka -->|3 independent consumer groups| Projection
  Projection --> Search[(OpenSearch / 3 primary shards)]
  Projection --> Analytics[(ClickHouse)]
  Projection -->|Pub/Sub| Redis
  Redis -->|fanout| Gateway
```

В локальной среде Identity, Catalog и Inventory имеют отдельные БД, владельцев и пароли на одном PostgreSQL instance. Два шарда Orders — два отдельных PostgreSQL instance с независимыми volumes. Это демонстрирует реальную маршрутизацию и независимость хранилищ, но не высокую доступность: у локальных БД нет standby.

| Сервис | Ответственность | Собственные данные | Протоколы |
|---|---|---|---|
| `gateway` | HTTPS, маршрутизация, Origin policy, rate limit, WS tickets и fanout | Redis tickets/rate counters; соединения в памяти | REST/JSON, WSS, HTTP upstream |
| `identity` | Регистрация, bcrypt, JWT, refresh rotation/reuse detection, logout | `identity.users`, `refresh_tokens` | HTTP/JSON |
| `catalog` | Карточки, цены, admin update, cache aside, outbox | `catalog.products`, `outbox` | HTTP/JSON, Redis, Kafka producer |
| `orders` | Идемпотентный checkout, shard routing, асинхронная state machine | `orders`, `idempotency`, `inbox`, `outbox` на двух шардах | HTTP/JSON, Kafka, gRPC client |
| `inventory` | Атомарный резерв нескольких SKU, дедупликация reserve | `stock`, `reservations` | gRPC/Protobuf + mTLS, HTTP health |
| `projection` | Три независимых Kafka-потребителя, поиск, аналитика, уведомления | OpenSearch/ClickHouse — перестраиваемые read models | Kafka, HTTP, Redis Pub/Sub |

В HTTP-обработчиках нет межсервисных SQL join. Orders не читает таблицы Inventory или Catalog. Общие пакеты содержат контракты и инфраструктуру, а не общий доступ к чужим базам.

## Быстрый запуск Docker Compose

### Требования

- Go 1.25+ для bootstrap и smoke. Зависимости зафиксированы в `go.mod` / `go.sum`.
- Docker Engine с Compose v2, Linux containers. На Windows — работающий Docker Desktop/WSL2.
- Ориентир для полного стека: 4–8 vCPU, 8–12 GiB RAM для Docker, 10+ GiB свободного диска. Это рекомендация для запуска, не результат capacity planning.
- Первый запуск скачивает тяжёлые образы Kafka/OpenSearch/ClickHouse и занимает несколько минут.

**Вариант только с Docker, без установленного Go:** `.\scripts\up.ps1` в PowerShell или `sh scripts/up.sh` в bash. Скрипт собирает bootstrap отдельным Docker-образом, создаёт credentials и запускает весь стек. Локальный Go нужен только для приведённых ниже команд разработки/smoke; основные сервисы всегда собираются внутри Docker.

Из корня репозитория одинаково в PowerShell и bash:

```sh
go run ./cmd/bootstrap
docker compose up --build -d --wait --wait-timeout 240
docker compose ps
go run ./cmd/smoke
```

Открыть [маркетплейс mesto](https://localhost:8443). Bootstrap создаёт локальный CA и сертификат для `localhost`/`127.0.0.1`; браузер предупредит о неизвестном издателе. Для локальной демонстрации можно явно принять сертификат. `smoke` проверяет цепочку через `.secrets/ca.pem` и не отключает TLS verification. Не используйте локальный CA вне dev-среды.

Bootstrap создаёт `.env` со случайными раздельными паролями БД, Redis, ClickHouse и admin. Ключи лежат в `.secrets/`. Оба пути исключены из Git и Docker build context. Повторный запуск сохраняет существующие credentials. Если `.env` уже есть, bootstrap не восстанавливает вручную удалённые ключи — сначала восстановите их из своей копии. Пароль администратора: значение `ADMIN_PASSWORD` в локальном `.env`; email: `admin@flashcart.local`.

Начальная витрина содержит 9 товаров четырёх категорий: электроника, дом, одежда/аксессуары, спорт. У новых SKU ограниченные запасы — например, 3 камеры и 4 лампы. Три исторических SKU сохраняют ранее созданные остатки; обновление не обнуляет заказы и склад. Новые цены и заказы — RUB, в целых копейках; имена API `price_cents`/`total_cents` сохранены для совместимости. Исторические заказы первой версии помечены USD, и суммы разных валют не складываются. Повторный старт не перезаписывает изменения продавцов.

Публикуются только `127.0.0.1:8443` и, опционально, Prometheus на `127.0.0.1:9091`. БД и брокер доступны сервисам во внутренней Docker network. У приложений отдельные процессы/образы, non-root UID 10001, read-only root filesystem, dropped capabilities и `no-new-privileges`.

### Управление стеком

```sh
docker compose logs -f orders inventory projection
docker compose restart orders
docker compose stop
docker compose start
docker compose down
```

`down` сохраняет named volumes. Команда `docker compose down -v` **удаляет все данные проекта**, включая пользователей и остатки; используйте её только для намеренного сброса своей dev-среды. После удаления данных сохранённые `.env`/сертификаты можно использовать повторно.

Не удаляйте `.env` у существующих volumes для «смены пароля»: первоначальные PostgreSQL credentials уже записаны в БД. Ротация требует согласованных изменений на стороне каждого сервера и его клиентов; повторный bootstrap не заменяет эту процедуру.

### Мониторинг

```sh
docker compose --profile observability up -d prometheus
```

[Prometheus](http://localhost:9091) собирает requests, HTTP latency, processed events и стандартные Go/process metrics. Пример p95:

```promql
histogram_quantile(0.95, sum by (le, service) (rate(flashcart_http_duration_seconds_bucket[5m])))
```

Метрики не содержат user/order ID в labels. SQL backlog outbox и Kafka consumer lag проверяются отдельно: см. runbook. Distributed tracing и Grafana пока не добавлены; request ID покрывает только HTTP-вызов, а event ID позволяет связать логи обработчика с событием.

## Проверки

### Быстрые проверки без инфраструктуры

```sh
go vet ./...
go test -count=1 ./...
go build ./...
```

Unit-тесты проверяют JWT issuer/audience/expiration/algorithm, RBAC, строгий JSON, ограничения заказа, канонический hash и стабильность shard routing. Обычный `go test ./...` не запускает integration-тесты и не доказывает корректность SQL-поведения.

### Реальный PostgreSQL и race detector

```sh
docker compose -f compose.yaml -f compose.test.yaml run --build --rm tests
```

Runner устанавливает C toolchain в отдельном test-образе и запускает unit/integration с `-race`. Integration-тесты создают собственные случайные PostgreSQL schemas и удаляют только их; пользовательские таблицы не сбрасываются. Проверяются конкурентное резервирование, отсутствие частичного списания, replay конфликты и 30 конкурентных checkout с одним ключом. В GitHub Actions дополнительно запускается полный Compose и smoke.

При уже доступном тестовом PostgreSQL:

```powershell
$env:TEST_DATABASE_URL = 'postgres://postgres:YOUR_PASSWORD@localhost:5432/postgres?sslmode=disable'
go test -tags=integration -count=1 -v ./tests/...
```

В Linux/macOS можно добавить `-race`; в Windows он требует установленный C compiler. Значения credentials в примере заменяются вашими. Не используйте production DB для тестов.

### End-to-end smoke

```sh
go run ./cmd/smoke
```

Smoke обращается через публичный HTTPS endpoint, проверяет JWT/RBAC, создание и повтор заказа, `409` при конфликте ключа, `404` для другого пользователя, резерв через gRPC, доставку Kafka → Redis → WS, одноразовость WS ticket, отзыв refresh family после reuse, поиск и admin analytics. Тест создаёт двух пользователей и расходует одну единицу товара. Он завершается ошибкой при отсутствии хотя бы одного из этих результатов. Это не нагрузочный тест.

Для автоматического dev-теста реального outage Inventory в PowerShell: `$env:CHAOS_INVENTORY='1'; go run ./cmd/smoke; Remove-Item Env:CHAOS_INVENTORY`. В bash: `CHAOS_INVENTORY=1 go run ./cmd/smoke`. В этом режиме smoke сам временно останавливает и запускает только Compose-сервис `inventory`, проверяет pending во время outage и terminal/WSS после восстановления. Не включайте режим против чужого/production стенда.

Для другого адреса задайте `BASE_URL` и `SMOKE_CA`; сертификат должен соответствовать hostname. Smoke читает admin credentials из локального `.env`.

Проверка полного marketplace-сценария: в PowerShell `$env:MARKETPLACE_CHECK='1'; go run ./cmd/smoke; Remove-Item Env:MARKETPLACE_CHECK`, в bash `MARKETPLACE_CHECK=1 go run ./cmd/smoke`. Она создаёт 2 продавцов и 3 покупателей, проверяет продажи последнего экземпляра, public WSS, идемпотентность товара/пополнения и скрытие чужих позиций в смешанном заказе.

## Запуск Kubernetes

### Локальный кластер kind

Нужны `kind`, `kubectl`, Docker и default StorageClass. Пример создаёт отдельный кластер и прокидывает HTTPS через NodePort:

```sh
kind create cluster --name flashcart --config deploy/kind.yaml
```

PowerShell:

```powershell
.\scripts\kube-up.ps1 -KindCluster flashcart
```

bash:

```sh
KIND_CLUSTER=flashcart sh scripts/kube-up.sh
```

Скрипт собирает образы `flashcart/<service>:dev`, загружает их в kind, создаёт Secrets из локальных файлов, применяет Kustomize и ждёт rollout. Он работает с **текущим kubectl context**. Проверьте `kubectl config current-context` перед запуском.

Для Docker Desktop Kubernetes выполните скрипт без `KindCluster`; local image store должен быть доступен кластеру. Для удалённого кластера нужно предварительно push образов в собственный registry и заменить image references через Kustomize overlay. Скрипт не публикует образы в registry автоматически.

Открыть [FlashCart](https://localhost:8443), либо отдельно:

```sh
kubectl -n flashcart port-forward svc/gateway 8443:443
```

Не запускайте Compose gateway и kind port mapping на одном `8443` одновременно. Остановите первый перед запуском второго.

### Что входит в манифесты

- Namespace, Deployments для шести Go-сервисов, ClusterIP Services и NodePort только у gateway.
- StatefulSets и PVC для PostgreSQL, двух order shards, Kafka, Redis, OpenSearch и ClickHouse.
- Kafka init Job с 12 partitions основного topic и 3 partitions DLQ.
- Startup/readiness/liveness probes приложений, resource requests/limits, rolling updates.
- HPA по CPU для gateway/catalog/orders: 1–4 replicas; нужен Metrics Server.
- PDB с `maxUnavailable: 1`: полезен при нескольких replicas, при одной replica не обеспечивает непрерывность.
- NetworkPolicy: default deny, DNS и разрешённые связи приложений с хранилищами.
- Никаких Secret с настоящими значениями в репозитории.

NetworkPolicy применяется только при поддержке CNI; стандартный kind не является доказательством её enforcement. Для проверки изоляции используйте Cilium/Calico или CNI вашего кластера с поддержкой политик. K8s не превращает один broker или один postgres в HA-кластер.

Проверка и обновление манифестов:

```sh
go run ./cmd/manifests
kubectl kustomize deploy
kubectl apply -k deploy
kubectl -n flashcart get pods,pvc,hpa,pdb
kubectl -n flashcart logs deployment/orders
```

`cmd/manifests` — воспроизводимый генератор повторяющихся объектов. Результат хранится в `deploy/k8s/generated.json`; Kubernetes/Kustomize принимает JSON как YAML. Сетевые политики и namespace написаны отдельно. Источник для изменения повторяющихся объектов — генератор, затем регенерация. Secret volumes читаются через `subPath`: после ротации сертификата нужен rollout restart соответствующего Deployment.

На Linux для OpenSearch проверьте `vm.max_map_count`; если bootstrap checks сообщают нехватку, настройте host/VM согласно официальной документации. Манифесты намеренно не включают privileged init container для изменения системных параметров.

## HTTP API

Все business paths начинаются с `/api`. JSON errors имеют вид `{"error":"..."}`. Тело ограничено 64 KiB, неизвестные JSON fields отклоняются, ровно один JSON value. Полная схема находится в `api/openapi.yaml`.

| Метод и путь | Доступ | Результат |
|---|---|---|
| `POST /api/auth/register` | public, auth rate limit | `201`, customer или seller с shop_name; admin запрещён |
| `GET /api/auth/me` | JWT | собственный профиль и название магазина |
| `POST /api/auth/login` | public, auth rate limit | access JWT 10 min + refresh 7 days |
| `POST /api/auth/refresh` | refresh token | одноразовая rotation, абсолютный срок family не продлевается |
| `POST /api/auth/logout` | refresh token | revoke всей family, `204` |
| `GET /api/products` | public | первые 100 SKU, PostgreSQL |
| `GET /api/products/{sku}` | public | cache aside, TTL 5 sec |
| `PUT /api/products/{sku}` | admin JWT | имя и цена; version назначает сервер |
| `GET /api/search?q=...` | public | до 20 hits из OpenSearch, eventual consistency |
| `POST /api/orders` | JWT + Idempotency-Key | `202`, server price snapshot |
| `GET /api/orders` | JWT | последние 50 собственных заказов |
| `GET /api/orders/{id}` | owner JWT | состояние из PostgreSQL owner shard |
| `POST /api/ws-ticket` | JWT | одноразовый ticket с TTL до 30 sec |
| `GET /ws?ticket=...` | ticket + Origin policy | WebSocket до expiration access JWT |
| `GET /api/analytics/summary` | admin JWT | summary из ClickHouse с `FINAL` |
| `GET /api/stock?skus=...` | public | актуальные available/version до 100 SKU |
| `GET /ws/catalog` | public + Origin policy | только публичные product/stock events, без заказов |
| `GET/POST /api/seller/products` | seller JWT | свои товары / идемпотентная публикация с initial_stock |
| `PUT /api/seller/products/{sku}` | owner seller JWT | обновление собственных данных товара |
| `POST /api/seller/stock/{sku}/adjustments` | owner seller JWT + key | добавление/списание единиц, атомарно с outbox |
| `GET /api/seller/orders` | seller JWT | свои позиции из последних 50 заказов, без buyer ID |

Авторизация: `Authorization: Bearer <access_token>`. Пароль: 12–72 bytes, bcrypt cost 12. Refresh передаётся JSON-телом, а не cookie; в браузерной демонстрации tokens хранятся только в памяти вкладки. После перезагрузки требуется новый вход.

Пример checkout через PowerShell после login:

```powershell
$base = 'https://localhost:8443'
# Для curl.exe используйте --cacert .secrets/ca.pem; не отключайте проверку в публичной среде.
$body = '{"items":[{"sku":"sku-keyboard","quantity":1}]}'
curl.exe --cacert .secrets/ca.pem -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-checkout-001' --data-raw $body "$base/api/orders"
```

Клиент не задаёт user ID, status или цену. До 20 уникальных SKU, quantity каждого 1–100. Повтор с переставленными items считается тем же запросом. Повтор с изменённым quantity и тем же ключом даёт `409`. Область ключа — пользователь и его owner shard. Idempotency records пока не удаляются автоматически.

### Событие Kafka

```json
{
  "id": "random-event-id",
  "type": "order.confirmed",
  "schema": 1,
  "aggregate_id": "0-order-id",
  "user_id": "customer-id",
  "version": 2,
  "at": "2026-11-07T12:00:00Z",
  "data": {"id":"0-order-id","status":"confirmed","items":[],"total_cents":12900}
}
```

Пример сокращён для чтения: реальные payloads типизированы через `domain.Order`/`Product`, все поля выставляет producer. Partition key — aggregate ID. Kafka topic: `commerce.events.v1`, DLQ: `commerce.dlq.v1`. Payload содержит user ID и товары; считать topic конфиденциальным. Пароли и tokens в события не попадают.

## Гарантии и важные ограничения

| Область | Реализованное свойство | Ограничение |
|---|---|---|
| PostgreSQL + Kafka | бизнес-запись и outbox атомарны в одной БД | relay может доставить событие повторно |
| Доставка | at least once, commit offset после успешного side effect | нет end-to-end exactly once |
| Inventory | атомарный multi-SKU reserve, replay по order ID | горячий SKU сериализует конкурентов |
| Checkout | один заказ на `(user, idempotency key)`, hash conflict | ключи не имеют TTL; quote делает HTTP внутри короткой транзакции |
| Saga | forward recovery, стабильный reserve ID, terminal CAS | нет payment saga, cancel или compensation |
| Search | versioned update `external_gte` | индекс отстаёт; доступность поиска не определяет truth |
| Analytics | одинаковый event ID дедуплицируется в `ReplacingMergeTree`; summary использует `FINAL` | каждый event — отдельный insert; для throughput нужны batches |
| Realtime | bounded fanout, WS heartbeat, reconnect + REST snapshot | Redis Pub/Sub не durable; уведомления могут теряться/дублироваться |
| Sharding | два физических Orders DB, детерминированный owner | topology v1 зафиксирована; автоматического resharding нет |
| Auth | Ed25519 JWT, RBAC, ownership, refresh family revocation | access JWT после logout действителен до 10 min |
| TLS | HTTPS/WSS на edge и mTLS на inventory RPC | внутрисетевые HTTP/DB/Kafka соединения в dev без TLS |
| Highload | ограниченные pools/queues/workers, partitions, HPA, нагрузочный сценарий | capacity и p99 ещё нужно измерять; singleton infra не HA |

Rate limit по socket IP: 120 API/WS requests в минуту, auth endpoints — 20. Не доверяем присланному клиентом `X-Forwarded-For`. Для размещения за ingress нужен отдельный allowlist trusted proxies и корректное извлечение client IP; текущая конфигурация иначе агрегирует клиентов ingress в один лимит. При отказе Redis rate limiter закрывает доступ к API с `503`.

## Структура репозитория

```text
cmd/                  отдельные main для 6 сервисов + bootstrap/manifests/smoke
internal/domain/      модели, validation, canonical hash, фиксированный shard router
internal/identity/    credentials и refresh sessions
internal/catalog/     source of truth продуктов и outbox
internal/orders/      checkout, HTTP ownership, Kafka orchestration
internal/inventory/   транзакционный reserve
internal/projection/  OpenSearch/ClickHouse/Redis consumers и read API
internal/gateway/     proxy, tickets, hub, статическая browser demo
internal/platform/    конфигурация, SQL migrations, JWT, TLS, Kafka, Redis, HTTP metrics
api/                  OpenAPI, protobuf и сгенерированный Go-код
deploy/               PostgreSQL init, Kustomize, kind, Prometheus
tests/                unit + integration + k6 load scenario
scripts/              запуск Kubernetes и локальные проверки
docs/                 архитектура, безопасность, runbook, performance, validation
```

Shared module упрощает портфолио и атомарное изменение контрактов, но сервисы деплоятся отдельно. В production понадобятся правила backward compatibility и независимые release pipelines; mono-repo сам по себе не делает систему монолитом.

## Что добавить перед production

1. HA PostgreSQL с backup/PITR и проверяемым restore, multi-AZ Kafka RF=3/min ISR=2, Redis failover, replicated ClickHouse, OpenSearch replicas.
2. TLS/SASL/ACL брокера, security plugin OpenSearch, service identity для внутренних HTTP вызовов, PKI lifecycle/secret manager, external identity provider/MFA.
3. Durable websocket resume или broker-backed inbox, presence-aware fanout, лимит активных соединений и восстановление по cursor.
4. Catalog batch quote с TTL/price-version контрактом; убрать серию HTTP calls из транзакции checkout.
5. Batch projections, outbox CDC/батчинг, retry topics/parking-lot для domain poison messages, replay tooling с проверкой schema.
6. Metrics для backlog/lag, distributed traces, SLO burn-rate alerts и performance report с hardware/commit/config.
7. Payment authorization, reservation expiry/release, compensation и periodic reconciliation. Проект сейчас не списывает деньги.
8. Directory/virtual buckets для resharding, keyset pagination, retention policies и data deletion workflows.

## Первичные источники

Реализация опирается на [PostgreSQL row/advisory locks](https://www.postgresql.org/docs/17/explicit-locking.html), [Kafka Docker quickstart](https://kafka.apache.org/41/getting-started/docker/), [gRPC Go API](https://pkg.go.dev/google.golang.org/grpc), [OpenSearch Docker setup](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/docker/). Эти ссылки объясняют механизмы; результаты конкретного проекта находятся в `docs/VALIDATION.md`.
- [Покупатели, продавцы, остатки и новые проверки](docs/MARKETPLACE.md).
- [Товарные изображения и воспроизводимый prompt](docs/ASSETS.md).



# требований: реализация, назначение и проверка

Текущий продукт — marketplace **mesto**: buyer/seller accounts, seller-owned listings, ограниченные остатки, stock outbox, публичные обновления доступности и приватные seller order views. Подробный сценарий и проверки — в [MARKETPLACE.md](docs/MARKETPLACE.md).

## 1. Go

**Где:** `cmd/{gateway,identity,catalog,inventory,orders,projection}`, `internal/*`, `go.mod`.

**Зачем:** обслуживать конкурентные HTTP/WS/RPC соединения и фоновые consumers с контролируемыми затратами памяти. Go даёт context cancellation, стандартные HTTP/TLS/crypto и удобный deployment одним бинарным файлом.

**Как:** у каждого bounded context отдельный entrypoint и процесс. Конфигурация приходит из environment; зависимости передаются в `Service` через поля. HTTP/SQL/gRPC получают context и deadlines. Shutdown по SIGTERM закрывает HTTP server; gRPC использует graceful stop с ограничением времени. Connection pools ограничены, фоновые consumers имеют фиксированную concurrency. Используются `slog`, `pgxpool`, типизированный protobuf-клиент и сгенерированный контракт.

**Что проверить:** `go vet ./...`, `go test ./...`, `go build ./...`; race detector в Linux test container/CI. Цена хранится в `int64` cents, количество — ограниченный integer. Credentials не зашиты в binary.

**Компромисс:** один Go module упрощает работу с примером, но сервисы разделяют версии контрактов. Независимые releases требуют expand/contract-подхода и проверки совместимости. Это прагматичная service-layer архитектура с SQL рядом с use case; искусственный repository interface для каждой таблицы не добавлен.

## 2. Kafka

**Где:** `internal/platform/events.go`, Orders saga и три consumer group Projection; Compose/K8s Kafka init.

**Проблема:** checkout не должен синхронно ждать аналитики, индекса и live-уведомления. Сбой поискового индекса не должен отменять сохранённый заказ.

**Механизм:** один topic `commerce.events.v1` с 12 partitions и key=aggregate ID. Events: `order.created`, `order.confirmed`, `order.rejected`, `product.updated`, `stock.updated`. Запись события делается через transactional outbox в Orders, Catalog и Inventory. Producer запрашивает `acks=all`; consumer фиксирует offset только после успешного side effect. Для заказов есть `orders-saga-v1`, для начального выделения складского запаса — `inventory-products-v1`; у read models отдельные группы `projection-search-v2`, `projection-analytics-v1`, `projection-notifications-v1`.

**Отказы:** crash между broker ack и отметкой `published_at` приводит к повтору. Поэтому reserve идемпотентен, state transition защищён условием `status='pending'`, search использует external version, analytics дедуплицирует event ID. Transport/handler errors повторяются с ограниченным exponential backoff. Непарсируемый envelope/неподдерживаемая schema отправляются в `commerce.dlq.v1`; после подтверждения DLQ исходный offset фиксируется.

**Проверка:** остановить Kafka, создать заказ, проверить outbox; запустить Kafka, дождаться terminal state. Посмотреть offsets и lag по runbook. Smoke подтверждает delivery через конечные состояния, а не только факт `WriteMessages`.

**Предел:** `acks=all` при RF=1 не защищает от потери единственного диска. Семантика end-to-end — at least once. Блокирующая ошибка consumer задерживает его assigned partitions; полноценные retry topics/domain DLQ — дальнейшая работа. Shared event topic удобен для демонстрации, но крупному домену нужны разграниченные ACL/topics и schema compatibility policy.

## 3. PostgreSQL

**Где:** embedded SQL в `internal/platform/migrations`, реализации Identity/Catalog/Orders/Inventory, `deploy/postgres/init.sh`.

**Проблема:** финансово значимые состояния, ownership, количество товара и idempotency должны сохраняться транзакционно и переживать restart.

**Механизм:** PostgreSQL — authoritative store. У сервисов отдельные logical DB и роли. Заказы распределены между двумя physical DB. SQL параметризован; ограничения `CHECK`, unique keys, foreign key для idempotency и refresh sessions. Migrations сериализованы advisory lock и проверяются checksum. Orders сохраняет заказ, idempotency record и outbox в одной транзакции. Inventory берёт row locks в одном порядке и сохраняет reservation decision вместе со списанием.

**Проверка:** integration-тесты на настоящем PostgreSQL проверяют race, stock invariants и число строк в orders/outbox/idempotency. Не используются mock-ответы, чтобы «доказать» поведение `FOR UPDATE`.

**Предел:** initial migrations создаются приложением при старте; для зрелого deployment стоит вынести versioned migration pipeline в отдельный job с отдельной DDL role. Нет standby, PITR automation и cross-shard SQL transactions. Общий PG для трёх logical DB экономит память локально и остаётся общим failure domain.

## 4. ClickHouse

**Где:** `internal/projection/service.go`: `AnalyticsEvent`, `click`, `/api/analytics/summary`.

**Проблема:** count/revenue по потоку заказов не должны создавать тяжёлые аналитические запросы к OLTP-shards. История событий полезнее текущей строки заказа для анализа потока.

**Механизм:** consumer пишет `order_events`: event ID, order ID, type, integer amount, event time и version. `ReplacingMergeTree(version)` с ключом event ID и месячным partition. Повторное событие имеет тот же event ID/time, поэтому попадает в ту же partition. Summary использует `FINAL`, затем `countIf`/`sumIf` для terminal events. Admin-only API отделено от customer checkout.

**Проверка:** smoke ждёт появление confirmed в summary; повторная Kafka-доставка одинакового event ID не должна увеличивать результат summary. В runbook есть SQL для диагностики.

**Предел:** merge не мгновенный; обычный `sum` по raw table без `FINAL` может посчитать дубли. `FINAL` и insert на каждый event подходят демонстрации, но для большого потока нужны batch inserts и измеренная модель дедупликации/предагрегации. Здесь один CH node: это partitioning по времени, а не distributed ClickHouse cluster. Физическое горизонтальное шардирование реализовано в Orders PostgreSQL.

## 5. Аутентификация и авторизация

**Где:** `internal/identity/service.go`, `internal/platform/auth.go`, `cmd/bootstrap`, JWT mounts.

**Проблема:** отличать пользователя от другого пользователя и администратора, ограничивать время сессии, обнаруживать повтор украденного refresh token.

**Аутентификация:** пароль bcrypt cost 12; access token Ed25519 JWT на 10 минут. Проверяются signature algorithm, issuer, audience, expiry и subject. Identity владеет private key, validators получают public key. Refresh token — случайные 256 bits, в БД только SHA-256 hash. Refresh живёт до 7 дней; при rotation старый помечается used, новый имеет ту же family и исходный absolute expiry.

**Авторизация:** регистрация создаёт customer или seller; seller требует название магазина, admin через signup запрещён. Seller ID/название берутся из подписанных claims, ownership проверяется в Catalog и Inventory SQL. Admin имеет отдельный catalog endpoint и analytics. Orders извлекает buyer ID только из JWT и добавляет `WHERE user_id=$owner`. Seller order views фильтруют JSONB items по seller ID и не раскрывают чужие позиции/ID покупателя.

**Reuse:** family-wide advisory lock сериализует refresh/logout. Повтор уже использованного token отзывает всю family, включая новый token. Клиент должен сериализовать refresh: параллельные rotations сами выглядят как replay.

**Проверка:** unit-тесты неверного issuer/audience/algorithm/expired JWT; smoke для customer `403`, чужого заказа `404`, refresh reuse и отзыва нового token.

**Предел:** logout отзывает refresh, но ранее выпущенный JWT остаётся действительным до expiration. Нет OIDC/MFA/email verification/password reset и account-level lockout. Admin bootstrap — dev-механизм, не invitation flow production.

## 6. OpenSearch

**Где:** Projection `SearchEvent`, `/api/search`; index `products-v2` с категорией, магазином, описанием и товарными атрибутами.

**Проблема:** полнотекстовый поиск не должен превращать PostgreSQL в поиск по `ILIKE '%query%'` и конкурировать с checkout за OLTP ресурсы.

**Механизм:** продукт меняется в Catalog PostgreSQL вместе с `product.updated` outbox. Consumer создаёт read model с explicit mapping: `sku` keyword, `name` text, price/version long. Три primary shards показывают внутреннее распределение индекса. `version_type=external_gte` защищает от повторов и более старых updates; конфликт старой версии считается успешно обработанным устаревшим событием. Query строится сервером из JSON-структуры, пользователь не передаёт произвольный DSL; q<=200 bytes, size=20.

**Проверка:** обновить товар администратором; GET карточки видит PostgreSQL/cache, поиск меняется немного позже. Повторить события в обратном порядке: более новая version не должна откатиться.

**Предел:** refresh interval даёт eventual consistency. Покупка не зависит от выдачи search, price quote берётся из Catalog. Replica count=0 и отключённый security plugin — локальная dev-конфигурация; наружу `9200` не публикуется. Для production нужны security plugin, TLS, ACL, replicas и измеренный shard sizing.

## 7. Docker + Kubernetes

**Где:** `Dockerfile`, `compose.yaml`, `Dockerfile.test`, `deploy/kustomization.yaml`, `cmd/manifests`, `scripts/kube-up.*`.

**Проблема:** воспроизводимая среда и deployment каждого сервиса с явно заданными зависимостями, persistent state и health checks.

**Docker:** multi-stage build, отдельный binary по `SERVICE`, pinned image tags, минимальный runtime, non-root. Compose поднимает все dependencies, health-based startup для БД/брокера, Kafka topic initialization, volumes и локальный HTTPS. Никакой внешней SaaS-зависимости для основного сценария.

**Kubernetes:** Deployment per service; StatefulSet/PVC per store; namespace; readiness/liveness/startup; resource budgets; rolling update; HPA и PDB; NetworkPolicy; Secrets генерируются локально. Kind config даёт URL без обязательного ingress controller.

**Проверка:** `docker compose config --quiet`, `kubectl kustomize deploy`, rollout status, smoke через одинаковый public API. HPA требует metrics API. Security policies требуют соответствующего CNI.

**Предел:** Dev topology не включает operators, multi-AZ и backup controllers. Image tags фиксированы, но не immutable digests; production pipeline должен добавлять digest pinning, scanning/SBOM/signing и registry. Наличие HPA без измеренной bottleneck capacity само по себе не доказывает масштабируемость.

## 8. Redis

**Где:** `internal/platform/redis.go`, Catalog cache, Gateway tickets/hub, Projection notifications.

**Проблемы и четыре использования:**

1. Cache aside карточек снижает число reads в PostgreSQL. TTL 5 sec ограничивает stale window; write делает best-effort invalidation. При cache miss/ошибке читаем source of truth.
2. Rate limiter использует atomic Lua `INCR` + first-use `PEXPIRE`, поэтому счётчик не останется без TTL при crash между двумя командами.
3. WS ticket хранится до 30 sec и погашается атомарным `GETDEL`, чтобы браузер не передавал долгоживущий access JWT в URL.
4. Pub/Sub рассылает order notifications всем Gateway replicas, которые фильтруют по локальным подписчикам user ID.

**Проверка:** повтор WS ticket получает `401`; превышение лимита даёт `429`; outage Redis закрывает gateway API с `503`, но не изменяет сохранённые заказы. Повторное подключение восстанавливает snapshots через REST.

**Предел:** Pub/Sub не durable, slow queues могут терять updates. Cache может заполниться старой версией после invalidation race, поэтому TTL является частью price-staleness контракта. `noeviction` защищает security keys от вытеснения: при исчерпании memory записи ошибаются. В production cache и security/session state следует разделить по instances/ACL/eviction policy; один Redis — общий failure domain.

## 9. Микросервисы

**Где:** 6 entrypoints, отдельные images/deployments, собственные DB и API boundaries.

**Проблема:** разные части e-commerce имеют разную нагрузку, доступность и характер хранения. Поиск можно перестраивать, резерв нельзя терять, browser connections живут дольше HTTP request.

**Механизм:** bounded contexts Identity/Catalog/Orders/Inventory; edge Gateway; Projection как единый deployable с независимыми consumer groups. Сервисы общаются через HTTP/gRPC/events, не через shared tables. Асинхронные read models масштабируются независимо от checkout. Services явно внедряют clients/pools; глобальная DB бизнес-домена не используется.

**Проверка:** отдельно restart/scale Orders или Projection; посмотреть, что authoritative state остаётся в owning DB. Отключить OpenSearch: сохранение заказа возможно, search недоступен. Отключение Inventory задерживает terminal transition.

**Предел:** Projection объединяет search/analytics/realtime в одном binary ради удобства dev; группы и offsets независимы, process failure всё равно общий. Можно разделить binary без смены event protocol. Небольшой реальный магазин логичнее начать модульным монолитом; здесь распределение выбрано для демонстрации инженерных задач.

## 10. HTTP, WS, gRPC, SSL/TLS, REST, JSON

**HTTP/REST/JSON:** public resource API `/api/products`, `/api/orders`; `POST` создаёт, `GET` читает, `PUT` обновляет product. Checkout возвращает `202` + `Location`, потому что reserve ещё не завершён. Errors имеют стабильный JSON shape. Responses описаны в OpenAPI.

**WebSocket:** private stream выдаёт JWT в защищённом POST за ticket, затем открывает WSS; public `/ws/catalog` доступен гостям и содержит только product/stock events. Контроль Origin, одноразовость private ticket, heartbeat, write deadlines, queue 32, закрытие private stream при expiry JWT. Client сравнивает aggregate version и выполняет REST reconciliation каждые 10 sec/после reconnect.

**gRPC/Protobuf:** `InventoryService.Reserve` — компактный типизированный внутренний RPC; `.proto` и generated Go checked in. Клиент использует deadline 3 sec. Multi-SKU request ограничен, server message size <=64 KiB.

**TLS:** термин «SSL» обычно используют для HTTPS, но старые SSL protocols не включены. Edge использует стандартный Go TLS stack, внутренний RPC — TLS 1.3 + client certificates. Inventory дополнительно проверяет SAN URI `spiffe://flashcart/orders`, а не только факт наличия любого cert от CA.

**Проверка:** smoke проверяет доверенный local CA и настоящий WSS. Подключение без client certificate к Inventory не проходит TLS handshake; certificate другого service identity должен получить PermissionDenied.

**Предел:** mTLS не включён для каждого внутреннего HTTP/DB/broker connection. Локальные сертификаты живут год, автоматической rotation нет. Это явно отражено в security model.

## 11. Безопасность веб-приложений

**Где:** `docs/SECURITY.md`, HTTP middleware, auth, gateway, validation, Docker/K8s policies.

**Механизмы:** parameterized SQL, strict bounded JSON, SKU/email/quantity validation, отказ от client price, ownership checks, RBAC, algorithm-pinned JWT, bcrypt, refresh reuse detection, CSP/security headers, exact Origin policy, rate limiting, timeouts, non-root runtime, отсутствие wildcard CORS, отсутствие token persistence в localStorage и секретов в Git.

Browser rendering использует `textContent`, а не `innerHTML` для данных товара/событий. CSRF cookie flow не создаётся: API использует Authorization bearer и JSON refresh, browser не отправляет credentials автоматически как cookie. Token theft через XSS всё равно возможен; CSP уменьшает поверхность, но не заменяет audit.

**Проверка:** unit/smoke negative cases; `403` Origin, `401` подписи/expiry, `404` ownership, `400` client price/unknown fields, `429` rate limit. Логи не пишут Authorization, body, password, refresh, query ticket.

**Предел:** нет WAF/bot management, MFA, email verification, distributed audit pipeline и formal penetration test. Это перечисленные границы, а не заявление об абсолютной защищённости. Особенности reverse proxy и revocation описаны отдельно.

## 12. Шаблоны проектирования и архитектуры

| Паттерн | Реализация | Зачем |
|---|---|---|
| Transactional outbox | orders/catalog transaction + relay | устранить небезопасный dual write DB/broker |
| Inbox/idempotent consumer | orders inbox, inventory reservation key | безопасно пережить repeated delivery |
| Forward recovery state machine | pending → confirmed/rejected | согласовать два сервиса без distributed SQL transaction |
| CQRS/read projections | OLTP owners vs OpenSearch/ClickHouse | разные access patterns и нагрузка |
| Cache aside | Catalog + Redis TTL | снизить частые reads, сохранить authoritative DB |
| Gateway/reverse proxy | public edge → internal APIs | единая транспортная policy и WS boundary |
| Decorator/middleware | Auth, Admin, RateLimit, HTTP metrics | композиция сквозных политик без копирования handlers |
| Dependency injection | Service fields с pool/client/public key | явно показать зависимости и подменять внешние границы в тестах |
| Optimistic version guard | order CAS, search external version, UI version | не применять устаревший update |
| Bounded worker/fanout | 4 saga readers, channel 32 | ограничить память и конкуренцию |

Подробные альтернативы и последствия находятся в `docs/ARCHITECTURE.md`. Не заявлены event sourcing (истина — строки PostgreSQL), строгий Clean Architecture или circuit breaker: этих механизмов в коде нет.

## 13. Распределённые системы и шардирование

**Физическое шардирование:** Orders выбирает `FNV-1a(user_id) % 2`. DSN ведут в разные PostgreSQL processes/volumes. Заказы, idempotency, inbox и outbox пользователя colocated. Чтение своего заказа использует ровно один shard; prefix order ID диагностический и не заменяет ownership. Router topology зафиксирована тестами.

**Распределённая согласованность:** заказ может быть durable pending, пока брокер/Inventory недоступен. Inventory и Orders не коммитят общую транзакцию. Если reserve commit прошёл, а ответ потерялся, consumer повторяет тот же order ID и получает сохранённое решение. Если Orders commit прошёл, а offset commit нет, повтор видит terminal state. Проекции и WS могут отставать/дублироваться.

**Конкуренция:** advisory lock защищает scope idempotency и reserve order ID; row locks защищают stocks; deterministic SKU order уменьшает deadlocks; CHECK не позволяет отрицательный stock. Version/CAS предотвращает повторный terminal transition.

**Backpressure:** bounded reader count/pools/queues, retries и offset lag вместо неограниченного spawning. Это сохраняет предел ресурсов, но переносит задержку в backlog. Для SLO нужно измерять backlog age, а не только HTTP `202` latency.

**Проверка:** конкурентные PostgreSQL тесты, replay запросов, остановки broker/consumer, запросы к двум shard DB, k6 acceptance/convergence. В `docs/ARCHITECTURE.md` разобраны crash windows, CAP и migration plan.

**Предел:** hash mod нельзя поменять на 4 без migration: часть user ID начнёт смотреть в пустую другую БД. Онлайн resharding не реализован. Hot user попадает в один shard; hot SKU всё ещё сериализуется в Inventory. Kafka partitions, PostgreSQL shards, CH time partitions и OpenSearch shards — разные механизмы с разными гарантиями.


