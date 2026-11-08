# Эксплуатация и демонстрация отказов

Команды выполняются из корня проекта. Сценарии `stop/start` обратимы и предназначены для собственной локальной среды. Сначала дождитесь успешного `go run ./cmd/smoke`.

## Диагностика старта

```sh
docker compose ps -a
docker compose logs --tail=80 postgres kafka kafka-init
docker compose logs --tail=80 identity catalog inventory orders projection gateway
```

| Симптом | Проверка |
|---|---|
| Docker pipe/daemon not found | запустить Docker Desktop, включить Linux containers/WSL2 |
| `POSTGRES_PASSWORD` missing | выполнить bootstrap из корня, проверить наличие `.env` |
| password authentication failed после нового `.env` | volumes имеют прежние credentials; восстановить исходный `.env` либо намеренно пересоздать свой dev dataset |
| key permission denied | certs монтируются отдельными файлами; проверить directory permissions и readable file mode для UID 10001 |
| OpenSearch exit/bootstrap checks | RAM и `vm.max_map_count` host/VM, storage permissions |
| Projection waiting init | health ClickHouse/OpenSearch и matching password в существующем volume |
| Orders pending | Kafka topic/consumer lag, inventory RPC TLS, inventory DB |
| API 503 при живых БД | Redis limiter fail-closed, upstream availability, readiness |
| WS 403 | exact Origin должен совпадать с PUBLIC_ORIGIN и browser host |
| WS 401 | ticket consumed/expired; запросить новый через authenticated POST |
| K8s Pending PVC | default StorageClass и доступный provisioner |
| HPA Unknown | установить Metrics Server; без него ручной scale работает |

## Kafka lag

```sh
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server kafka:9092 --describe --group orders-saga-v1
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server kafka:9092 --describe --group projection-search-v1
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server kafka:9092 --describe --group projection-analytics-v1
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server kafka:9092 --describe --group projection-notifications-v1
```

Lag показывает разрыв offset, не возраст бизнес-заказа. Полезно сопоставлять oldest pending timestamp, inventory failures, consumer retry logs и storage latency. Увеличивать replicas имеет смысл до достижения partition/downstream bottleneck.

## Outbox и shards

```sh
docker compose exec orders-db-0 psql -U orders -d orders -c "SELECT count(*), min(created_at) AS oldest FROM outbox WHERE published_at IS NULL;"
docker compose exec orders-db-1 psql -U orders -d orders -c "SELECT count(*), min(created_at) AS oldest FROM outbox WHERE published_at IS NULL;"
docker compose exec orders-db-0 psql -U orders -d orders -c "SELECT status,count(*) FROM orders GROUP BY status;"
docker compose exec orders-db-1 psql -U orders -d orders -c "SELECT status,count(*) FROM orders GROUP BY status;"
```

`published_at` означает broker ack, а не применение всех read models. Не отмечать строки published вручную для очистки backlog: это теряет события. В production добавить отдельные metrics oldest age/count и alert до исчерпания disk/retention window.

## Сценарий 1: Inventory недоступен

```sh
docker compose stop inventory
```

Создайте заказ из UI: `202 pending` остаётся в PostgreSQL. Consumers логируют retry; HTTP создание не ждёт 3-second RPC. Запустите:

```sh
docker compose start inventory
```

Ожидается terminal state и WS update после catch-up. В очереди могут быть также события других типов; consumer последовательно проходит assigned partitions. Отдельный тест forward recovery проверяет неоднозначный RPC result после успешного reserve commit.

## Сценарий 2: Kafka недоступна

```sh
docker compose stop kafka
```

Создайте заказ, посмотрите неопубликованный outbox. Source-of-truth write остаётся успешным, пока DB доступна. Не обещайте пользователю мгновенный reserve. Затем:

```sh
docker compose start kafka
```

Relay повторит publication, consumers продолжат offsets. При outage дольше retention/backup horizon нужна reconciliation: вечное хранение pending без операционного контроля не является recovery strategy.

## Сценарий 3: Search недоступен

```sh
docker compose stop opensearch
```

GET search вернёт `503`, но PostgreSQL каталог и checkout не обращаются к search. Analytics/notification consumers имеют свои группы и продолжают работу, если их зависимости доступны. После старта Opensearch search group догоняет поток. В Kubernetes общий Projection readiness может временно сделать read API неготовым целиком; это известная coarse-grained probe.

```sh
docker compose start opensearch
```

## Сценарий 4: restart consumers и frontend

```sh
docker compose restart orders projection gateway
```

Orders offset/decision/outbox сохраняются; повтор события безопасен. Gateway теряет in-memory connections: UI получает close, запрашивает новый ticket, перечитывает REST snapshot. Если token expired, browser refresh flow выполняется перед новым ticket. Redis Pub/Sub не воспроизводит пропущенные события, поэтому snapshot обязателен.

## DLQ и replay

```sh
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server kafka:9092 --topic commerce.dlq.v1 --from-beginning --max-messages 10 --timeout-ms 5000
```

DLQ содержит original topic/partition/offset, consumer group, причину и raw record. Невалидный envelope/unsupported schema уходит туда; доменная ошибка внутри корректного envelope сейчас повторяется и требует исправления handler/data. Не считайте это полной автоматической poison-message quarantine.

Read-model replay: остановить соответствующий consumer process, выбрать новую consumer group либо reset offsets и запустить. **Не reset offset активной группы.** Projection сейчас объединён в один процесс: его остановка приостанавливает все три consumer groups.

Пример только для собственного стенда с остановленным Projection:

```sh
docker compose stop projection
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server kafka:9092 --group projection-search-v1 --topic commerce.events.v1 --reset-offsets --to-earliest --dry-run
```

Проверьте диапазон и retention перед осознанным `--execute`; затем `docker compose start projection`. Старые search versions безопасно отбрасываются, CH summary дедуплицирует одинаковые event IDs. Replaying notifications может повторно показать UI events — version guard обязателен. Kafka хранит только ограниченный retention window, поэтому полного rebuild после старой потери index эта команда не гарантирует.

Не предоставлен автоматический replay произвольного raw DLQ в основной topic: сначала исправляется причина и проверяется schema/aggregate version/идемпотентность side effects. Смена event ID при повторе analytics создаёт логически новый event и обходит дедупликацию.

## Backup и восстановление

Named volumes переживают container restart, но не являются backup. До production: периодические base backups + WAL archive/PITR для каждого PG shard, backups CH/OpenSearch, broker durability/replication, encrypted secret backup и проверяемый restore drill. Восстановление Orders без соответствующего Inventory snapshot может оставить pending/confirmed несовместимыми с reservations; нужен reconciliation по order ID.

Пример PostgreSQL dev export с файлами внутри контейнера, без риска PowerShell binary-redirection:

```sh
docker compose exec orders-db-0 sh -ec 'pg_dump -U orders -d orders -Fc -f /tmp/orders0.dump'
docker compose cp orders-db-0:/tmp/orders0.dump artifacts/orders0.dump
```

Восстановление делать в **отдельный** PostgreSQL, затем сравнить counts/checksums/reservations/outbox и только после проверки переключать routing. Не импортируйте dump поверх работающего checkout без плана freeze/fencing.

## Очистка и retention

Нужен согласованный срок для published outbox, inbox, idempotency, expired refresh и analytics. Если удалить reservation/idempotency раньше возможного replay, историческая доставка может повторно списать stock/создать заказ. Если удалить outbox и Kafka history без snapshot/archive, исчезнет возможность восстановления projections. Автоматическая очистка в версии v1 намеренно отсутствует; наблюдайте размер таблиц и диска.

## Kubernetes диагностика

```sh
kubectl -n flashcart get deploy,statefulset,pods,pvc,hpa,pdb
kubectl -n flashcart describe pod POD_NAME
kubectl -n flashcart logs deployment/orders --tail=80
kubectl -n flashcart rollout status deployment/gateway
kubectl -n flashcart get networkpolicy
```

Для новой сборки `:dev` на существующем kind: загрузить образ и выполнить rollout restart deployments; `IfNotPresent` и прежний tag не инициируют rollout автоматически. Для обычного release используйте immutable image tag/digest и Kustomize image update. Не удаляйте Namespace/PVC ради устранения ошибки конфигурации.
