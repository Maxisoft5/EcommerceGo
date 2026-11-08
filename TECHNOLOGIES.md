# 13 требований: реализация, назначение и проверка

Этот файл связывает каждое исходное требование с конкретным модулем FlashCart. Для каждого пункта указаны бизнес-проблема, механизм, способ проверки и пределы решения. Здесь перечислены реализованные свойства; планы развития обозначены отдельно.

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
