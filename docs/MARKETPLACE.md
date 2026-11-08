# mesto: покупатели, продавцы и ограниченные товары

## Пользовательские сценарии

**Покупатель:** витрина с фотографиями → поиск/категория/магазин → карточка товара → корзина → вход/регистрация → оформление → «Заказ принят» или отказ из-за закончившегося товара. Доступность и количество обновляются у посетителей, включая гостей. Когда остаток равен нулю, кнопка добавления недоступна. Корзина и избранное сохраняются локально; токены — только в памяти.

**Продавец:** «Продавать на mesto» → регистрация seller с названием магазина → кабинет → публикация товара с начальным запасом → изменение названия, описания, категории, изображения, цены → пополнение запаса → просмотр заказов своих товаров. Продавец может также делать покупки. Для нового магазина не подставляются вымышленные продажи, оценки или отзывы.

В customer flow нет названий баз, Kafka, WebSocket, Redis, статусов `pending/confirmed` или портфолио-баннеров. Документация и API сохраняют технические контракты. Дизайн и изображения принадлежат этому проекту; стили Ozon/WB не копируются.

## Как проверить руками

1. Открыть `https://localhost:8443`.
2. Нажать «Продавать на mesto», выбрать seller, ввести магазин/email/пароль.
3. Добавить товар, например «Наушники WEEKEND», цену 3490 ₽, количество 2, выбрать изображение.
4. В другом аккаунте/окне открыть товар и купить последние экземпляры. Отдельное окно удобно для наблюдения live-остатка; сессии хранятся в памяти вкладки.
5. При нулевом stock увидеть «Раскупили». Продавец видит заказ, собственную сумму и остаток 0.
6. Нажать «Пополнить»: новые единицы становятся доступны всем посетителям.

Отображаемое наличие — snapshot/read model, а не обещание резерва. Даже если две вкладки показывают последнюю штуку, окончательное решение принимает Inventory под SQL row lock. При проигранной гонке заказ получает бизнес-отказ, а не лишнюю единицу товара.

## Архитектура изменений

- **Identity:** `customer/seller/admin`, `shop_name`, `/api/auth/me`, подписанные role/shop claims. Нельзя зарегистрировать admin или передать другой seller ID в товаре.
- **Catalog:** товар содержит seller ID/name, description, category, image key, old price и immutable initial stock. Owner filter действует на update/list. Публикация защищена `(seller_id, Idempotency-Key, payload_hash)`.
- **Inventory:** consumer `inventory-products-v1` создаёт stock из product event единожды. `INSERT ... ON CONFLICT DO NOTHING` гарантирует, что повтор/редактирование товара не восстановит уже проданные единицы. Reserve и adjustment сериализуются на одном stock row.
- **Inventory outbox:** `stock.updated` пишется в той же транзакции, что available/version. Нет окна «stock committed, notification forgotten». Идемпотентное пополнение хранит прежний результат; тот же key с другой delta — `409`.
- **Orders:** price/name/seller/image snapshots назначаются сервером, клиент не управляет ими. Seller API читает оба физических shard с JSONB GIN-фильтром и возвращает только его lines и subtotal. В смешанном заказе всё ещё действует all-or-nothing reserve.
- **Projection/Gateway:** stock/product → `catalog:events` → public WSS; order → private buyer channel и отфильтрованные seller notifications. Public stream не включает заказы. Клиент сравнивает version и восстанавливает state через REST после reconnect.
- **OpenSearch:** новый `products-v2` и отдельная consumer group перестраивают расширенную read model, не меняя старый mapping на месте.
- **Currency:** новые заказы RUB, в целых копейках. Исторические orders маркированы USD. CH report явно фильтрует currency=RUB; разные валюты не складываются.

Миграции 001 не изменены: их прежний checksum остаётся валиден. Дополнительные `_002.sql`/`_003.sql` применяются по порядку под advisory lock. Имеющиеся пользователи, заказы и остатки не удаляются. Инициализированные fixture listings не перезаписываются при restart, даже если описание продавец очистил.

## API

| Endpoint | Доступ | Назначение |
|---|---|---|
| `POST /api/auth/register` | public | `{email,password,role?,shop_name?}`; seller требует shop_name |
| `GET /api/auth/me` | JWT | собственный профиль |
| `GET /api/stock?skus=sku-a,sku-b` | public | `items[{sku,available,version}]`, до 100 |
| `GET /ws/catalog` | public Origin policy | только публичные product/stock события |
| `GET /api/seller/products` | seller | первые 100 собственных товаров |
| `POST /api/seller/products` | seller + key | name/price/description/category/image/initial_stock; `201` |
| `PUT /api/seller/products/{sku}` | owner | редактирование метаданных; количество меняется отдельно |
| `POST /api/seller/stock/{sku}/adjustments` | owner + key | `{delta}`; −100000..100000 кроме 0; итог 0..1000000 |
| `GET /api/seller/orders` | seller | последние 50 собственных sales без buyer ID/чужих lines |

У объявления SKU/owner/version назначает сервер. Категории: electronics/home/fashion/sport. Выбор `image_key` ограничен девятью локальными изображениями. Произвольные URL/HTML продавцов не принимаются.

## Автоматические проверки

```powershell
docker compose -f compose.yaml -f compose.test.yaml run --build --rm tests
$env:MARKETPLACE_CHECK='1'
go run ./cmd/smoke
Remove-Item Env:MARKETPLACE_CHECK
```

В bash: `MARKETPLACE_CHECK=1 go run ./cmd/smoke`.

Новые SQL/race tests подтверждают ownership товаров, изоляцию seller views, server-side snapshots, seller registration claims, повтор initial allocation и конкурентное повторное пополнение. E2E создаёт 2 продавцов и 3 покупателей: при stock=2 подтверждаются ровно 2 заказа, третий rejected; проверяются public WSS, restock replay, mixed-seller privacy и private seller WSS.

При обновлении 7 ноября 2026 эти тесты прошли. Также в браузере создан WEEKEND-магазин, товар с 2 экземплярами, выполнено пополнение +1 и оформлена покупка всех 3 единиц на 10 470 ₽. Новый UI отобразил принятие заказа. Логи — `artifacts/marketplace-*.log`; исторический load report первой версии хранится отдельно в VALIDATION.

Продавец затем увидел 1 принятый заказ, продажи 10 470 ₽ и stock=0. Через кабинет выполнено пополнение +5, которое отобразилось на публичной витрине. Проверен поиск «лампа» и отсутствие ошибок JavaScript в browser console; горизонтального переполнения при фактической ширине 846 px не обнаружено. [Витрина](screenshots/mesto-home.jpg), [продавец](screenshots/mesto-seller.jpg), [покупатель](screenshots/mesto-order.jpg).

Повторно прошли `go vet`, `go build`, unit tests, строгая проверка OpenAPI и исходный E2E smoke (HTTPS/JWT/RBAC/refresh, Kafka, gRPC/mTLS, WSS, search, analytics). Полный SQL integration suite с race detector завершился `PASS` за 15,131 sec. Новый performance/capacity run после расширения потока stock events не выполнялся.

## Практические пределы

Это работающий marketplace-сценарий, но ещё не полнофункциональная коммерческая платформа. Нет эквайринга/списания денег, доставки, возвратов, buyer addresses, вариантов размера/цвета, настоящих отзывов, moderation/KYC и seller payouts. Кнопка оформления подтверждает заказ и резерв, не сообщает об оплате. Товарные изображения — локальный заранее подготовленный набор; загрузка собственных файлов не реализована.

Начальный запас выделяется асинхронно: короткое время после публикации UI показывает подготовку наличия. Бизнес-данные остаются в сервисах-владельцах; Catalog не делает синхронный dual write в Inventory. Stock имеет version, public stream — best effort, snapshot polling 10 sec. Seller list ограничен 100 products/50 orders; для большой истории нужна pagination. Метрики продаж показывают только возвращённое окно заказов, что подписано в кабинете.
