# Highload: измерение и инженерные ограничения

Проект содержит механизмы для работы под нагрузкой и исполняемый сценарий. Объявленная цель p95 — гипотеза стенда; успешная компиляция не доказывает RPS.

Фактический короткий local run и его границы записаны в [VALIDATION.md](VALIDATION.md): 2 078 orders, p95 create 4,87 ms, p95 polling convergence 511 ms. Это baseline одного пользователя/SKU, не предел capacity.

## Какие показатели различать

1. **Acceptance latency:** HTTP POST до durable pending. Для текущего сценария budget p95 <500 ms на прогретом стенде.
2. **Convergence latency:** POST до terminal state. Цель сценария p95 <10 sec при штатных dependencies.
3. **Success semantics:** `202` означает приём. `rejected` из-за stock exhaustion — terminal бизнес-результат, не server error. Разделяйте acceptance rate, confirmation rate и availability.
4. **Correctness:** нет overselling, duplicate orders и частичных reserves. Этот инвариант нельзя заменить низким latency.
5. **Resource saturation:** pool wait, SQL lock time, CPU, RSS, GC, goroutines, disk, broker lag, backlog age. p99 при постоянно растущем backlog не является устойчивой capacity.

В k6 thresholds — критерии теста, а не обещание достигнутого результата. Сценарий имеет закрытую модель VUs, поэтому при замедлении сам снижает arrival rate. Для capacity planning дополнительно нужен constant-arrival-rate/open workload и контроль dropped iterations.

## Локальный нагрузочный запуск

Используйте отдельный собственный стенд с тестовыми данными. Установите k6. При первом прогоне желательно сначала warm-up 1–5 VUs и проверить smoke.

```sh
docker compose -f compose.yaml -f deploy/loadtest.override.yaml up -d gateway
k6 run tests/load/checkout.js
```

Override повышает gateway limits, чтобы измерять checkout, а не стандартный anti-abuse limit 120/min/IP. Он не публикует новые порты. После теста верните штатную конфигурацию:

```sh
docker compose up -d gateway
```

`insecureSkipTLSVerify` включён только в local k6 config из-за self-signed dev CA; smoke отдельно проверяет TLS chain. Для внешнего performance стенда используйте доверенный certificate и отключите этот k6 флаг.

Тест создаёт customer, идёт от 1 к 30 VUs, повторяет каждый create с прежним key и сверяет ID, ждёт terminal state через GET. Один пользователь идёт в один PostgreSQL shard, один SKU — в один hot stock row. Это намеренно показывает contention; для проверки распределения обоих shards добавьте отдельный workload с набором разных users, фиксированным seed и распределением нагрузки. Нельзя заявлять проверку shard balance на этом single-user сценарии.

Начальный stock=1000; после исчерпания заказы rejected. Тест throughput acceptance продолжит работать, но это уже другой профиль Inventory lock/update, чем успешные резервы. Для сравнения positive-reserve capacity нужен заранее подготовленный большой stock либо ограниченный прогон; не меняйте stock рабочего магазина ради benchmark.

## Проверка инвариантов после прогона

```sh
docker compose exec orders-db-0 psql -U orders -d orders -c "SELECT user_id,key,count(*) FROM idempotency GROUP BY user_id,key HAVING count(*)>1;"
docker compose exec orders-db-1 psql -U orders -d orders -c "SELECT user_id,key,count(*) FROM idempotency GROUP BY user_id,key HAVING count(*)>1;"
docker compose exec postgres psql -U postgres -d inventory -c "SELECT sku,available FROM stock ORDER BY sku;"
docker compose exec postgres psql -U postgres -d inventory -c "SELECT accepted,count(*) FROM reservations GROUP BY accepted;"
```

Counts нужно сопоставлять с начальным dataset и quantity каждого accepted reservation; таблица reservations в этой версии хранит request hash, не items. Для полного offline audit stock ledger потребуется append-only inventory movement table — она пока не реализована.

## Что ограничивает масштабирование сейчас

- **Hot SKU:** последовательные stock row locks. Увеличение HTTP replicas этого не убирает.
- **Price quote:** последовательные HTTP reads до 20 SKU внутри checkout transaction, pool 12. При высокой fanout latency нужен batch quote и короткая DB transaction.
- **Outbox:** batch 20, polling 200 ms на shard; network publish под SQL lock. Это измеримый предел текущего relay, не бесконечно масштабируемый CDC.
- **Saga:** 4 Kafka readers на Orders replica; group assignment ограничен 12 partitions. При добавлении replicas полезный parallelism не превышает partition budget; потом доминирует Inventory.
- **Projection:** один consumer на read model на process; CH insert per event, search index request per product. Для больших потоков batch workers и более явное partition processing.
- **Realtime:** все Gateway replicas получают все Redis events; bounded local queues пропускают сообщения. Отсутствует отдельная quota активных sockets; нужна admission policy для внешнего high fanout.
- **Redis:** единый instance для cache/security/live, shared memory/failure domain.
- **Readiness:** coarse readiness обоих order shards может снизить partial availability; описано в архитектуре.
- **Storage:** одна replica infrastructure. HPA приложений не создаёт HA stateful dependencies.

## Матрица экспериментов

| Сценарий | Что меняем | Что доказывает |
|---|---|---|
| Normal mixed commerce | разные users/SKU, read/write mix | steady-state capacity обоих shards |
| Flash sale hot SKU | один SKU, много покупателей | lock contention, invariant available>=0 |
| Idempotency storm | один user/key, concurrent requests | один durable order, conflict detection |
| Broker outage | stop Kafka 30 sec, потом start | growth/catch-up outbox, acceptance vs convergence |
| Inventory timeout | stop/restart RPC | pending backlog и forward recovery |
| Slow subscriber | задержка WS client reads | bounded memory, REST recovery |
| Projection replay | offset reset на dev data | version guard, CH dedup, duplicate notifications |
| One shard outage | stop одного Orders DB | availability владельцев, readiness tradeoff |

## Шаблон performance report

Заполнить после реального прогона и приложить raw results:

```text
Commit / UTC date:
OS / Docker / architecture:
CPU / RAM / disk / Docker resource limits:
Replica count / partitions / DB pool / topology:
Dataset size / initial stock / active users / SKU distribution:
Workload model / target arrival / VUs / duration / warm-up:
Accepted RPS / confirmed / rejected / HTTP errors:
Create p50 / p95 / p99:
Convergence p50 / p95 / p99:
Max Kafka lag / oldest outbox age / drain duration:
CPU/RSS/GC / PG lock wait / disk utilization:
Correctness queries / duplicate counts / final stock:
First saturated component / change / comparison run:
```

Сильный разбор на интервью: назвать первый bottleneck, показать профиль/метрики, предложить изменение, повторить одинаковый workload и объяснить цену компромисса. Список используемых технологий без таких измерений этого не заменяет.
