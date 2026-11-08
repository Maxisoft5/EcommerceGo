# Безопасность FlashCart

## Границы доверия

Недоверенные inputs: HTTP body/query/path, заголовки, Origin, access/refresh token и Kafka record при компрометации producer. Public endpoint — Gateway HTTPS. Kafka/DB/Redis/OpenSearch/ClickHouse не опубликованы на host. Docker network ограничивает внешнюю достижимость, но не защищает от уже скомпрометированного контейнера. В Kubernetes связи дополнительно ограничены NetworkPolicy при поддержке CNI.

Секреты: password hashes и refresh hashes в Identity DB; JWT signing private key только Identity; service certificate keys только соответствующему service; shared deployment credentials в локальном `.env`. Администратор кластера/Docker daemon имеет доступ к secrets: это привилегированная trust boundary.

## Контроли по угрозам

| Угроза | Контроль | Остаточный риск |
|---|---|---|
| Подделка личности | Ed25519, pinned algorithm, issuer/audience/expiry/subject validation | украденный access token действует до expiry |
| Credential stuffing | bcrypt cost 12, IP auth limits, generic login failure, dummy hash неизвестного user | нет MFA/account lockout/bot detection; registration раскрывает существование email через 409 |
| Refresh replay | одноразовая rotation, hash-only storage, family-wide lock/revocation | attacker может вызвать logout family, предъявив украденный старый token |
| BOLA/IDOR | user ID из JWT; owner filter SQL; 404 для чужого order | admin-функции support impersonation не реализованы |
| Privilege escalation | signup только customer/seller; admin запрещён; ownership в Catalog/Inventory, admin-only analytics | bootstrap admin пароль остаётся секретом dev окружения |
| SQL injection | `$1` placeholders, fixed SQL; клиент не задаёт имя таблицы/shard | schema migrations и operator SQL считаются доверенными |
| Search injection | сервер строит фиксированный DSL, bounded q/size | сложные запросы могут нагружать поисковый движок |
| XSS | textContent, CSP, отдельные JS/CSS, nosniff | CSP не заменяет аудит dependencies/DOM |
| Cross-site запрос | exact Origin policy; no wildcard CORS; bearer tokens | non-browser client может подделать Origin, поэтому Origin не заменяет authentication |
| CSRF | tokens в Authorization/JSON, cookies не используются | при переходе на cookies нужны SameSite/CSRF tokens и отдельный review |
| Replay checkout | scope `(user,key)` + canonical request hash, DB unique key/lock | бесконечное хранение ключей требует retention policy |
| Overselling | sorted stock row locks, atomic decision, CHECK available>=0 | hot SKU contention/availability остаются проблемой capacity |
| WS token leak | короткий одноразовый ticket, GETDEL, query не логируется | ticket до consumption может попасть в browser tooling/proxy logs |
| WS resource exhaustion | bounded outbound queue, heartbeat/write timeout, JWT expiry | нет отдельной quota активных соединений на user/node |
| Large/slow requests | body/header limits, read header/read/request deadlines, bounded message size | нет полного traffic shaping или WAF |
| Container escape blast radius | app non-root/read-only/drop ALL/no-new-privileges, SA token disabled | infrastructure images используют собственные entrypoints; node compromise вне защиты приложения |

## JWT и refresh lifecycle

Access JWT живёт 10 минут, содержит subject/role/issuer/audience/jti и temporal claims. Private signing key не доставляется validators. Key rotation с `kid`/JWKS ещё не реализована: текущий verifier имеет один public key. Ротация требует согласованного rollout и может прекратить существующие sessions. Production design должен поддерживать overlap старого/нового public key до expiry.

Refresh генерируется из 32 случайных bytes, представлен 64 hex-символами. В БД SHA-256 hash, family, user, absolute expiry, used_at и revoked. Исходный refresh нужен только клиенту. При rotation срок family не продлевается. Logout сериализуется с rotation на family advisory lock. Старый access JWT после logout сохраняет валидность до 10 минут — это явный tradeoff stateless authentication.

Browser demo хранит tokens в оперативной памяти JS. Ни localStorage, ни cookie, ни URL access JWT не используются. Один promise сериализует refresh внутри вкладки. При обновлении страницы пользователь входит заново. Для настоящего storefront можно вынести refresh в защищённый BFF/httpOnly cookie, но это другой threat model с CSRF protection.

В localStorage marketplace UI сохраняются только публичные товарные данные корзины и избранное; credentials там отсутствуют. Public `/ws/catalog` не требует JWT и не содержит order/user notifications. Seller notifications и seller order API содержат только позиции этого магазина: общий состав и buyer ID не передаются. Stock endpoints проверяют владельца SKU; повторы пополнений защищены seller-scoped idempotency key и транзакцией.

## TLS и service identity

Gateway обслуживает HTTPS/WSS; smoke проверяет сертификат по `.secrets/ca.pem`. Inventory RPC требует TLS 1.3, проверку client CA и SAN URI `spiffe://flashcart/orders`. Простого possession произвольного client certificate от CA недостаточно.

Bootstrap выпускает локальные cert на один год и не меняет системное хранилище доверия. Private CA key не сохраняется; для нового набора сертификатов генерируется новый CA и нужен согласованный restart всех зависимых сервисов. `.env` и `.secrets` не коммитятся. На Unix каталог `.secrets` имеет 0700; файлы монтируются отдельно и читаемы container UID 10001. На Windows права определяет ACL каталога — при совместной машине настройте доступ к workspace.

Dev HTTP между сервисами, PG `sslmode=disable`, Kafka PLAINTEXT и OpenSearch без security plugin не подходят недоверенной сети. Перед production нужны TLS/service authentication, Kafka SASL/ACL и полноценные OpenSearch credentials. NetworkPolicy не заменяет криптографическую authentication.

## Proxy и IP identity

Rate limiter берёт socket RemoteAddr и не доверяет клиентскому X-Forwarded-For. Gateway удаляет identity/forwarded headers перед proxy; backend identity берётся из подписанного JWT. Если gateway поставить за ingress без trusted proxy policy, все клиенты могут оказаться одним IP для rate limit. Нельзя просто довериться любому forwarded header: это позволит обходить лимит.

Текущий fixed-window лимит допускает burst на границе двух окон. На public API полезны per-user + per-IP limits и отдельные quotas для дорогих операций. При Redis error ответ `503`, а не обход ограничителя. Performance override увеличивает лимиты только для локального нагрузочного стенда.

## Данные, логи и эксплуатация

HTTP logs содержат method/path/status/request ID/duration. Тела, Authorization, refresh, password и query-string не логируются. WS tickets поэтому не попадают в application path logs, но операторские reverse proxies могут логировать полный URL — их нужно настроить отдельно.

Kafka events содержат user ID/items/amount и считаются конфиденциальными. DLQ хранит raw event и требует тех же ACL/retention. Analytics не хранит email, пароль или token, но order ID позволяет связать данные с owner store. Это pseudonymous data, а не доказанная анонимность.

Автоматическое удаление expired refresh/idempotency/inbox/outbox и GDPR deletion flow не реализованы. Не удалять idempotency раньше максимального retry/replay window; иначе старый запрос создаст новый заказ. Retention должна определяться бизнес-контрактом и tested replay horizon.

## Security acceptance перед публичным размещением

- Настроить внешние cert/PKI rotation, private network TLS, secrets manager и encryption at rest/backups.
- Убрать dev admin bootstrap и применить OIDC/MFA или управляемый identity provider.
- Подтвердить NetworkPolicy enforcement, service RBAC, registry digest/scanning/SBOM.
- Добавить connection admission quotas, user-based rate limits, account recovery/verification и audit trail.
- Проверить dependency advisories (`govulncheck`), abuse cases, threat model и penetration testing на целевой инфраструктуре.

Эти пункты — план для внешней эксплуатации. Локальный проект не заявляет результаты penetration test или соответствие стандартам сертификации.
