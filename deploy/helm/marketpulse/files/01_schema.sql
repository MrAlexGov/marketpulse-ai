-- ClickHouse сам читает Kafka (движок Kafka) и через материализованные представления
-- раскладывает события по аналитическим таблицам. Отдельный сервис-консьюмер не нужен.

CREATE DATABASE IF NOT EXISTS marketpulse;

-- Отправления: одна строка на (продавец, отправление, SKU). ReplacingMergeTree с версией
-- status_rank схлопывает повторы (at-least-once из outbox) и оставляет последний статус.
CREATE TABLE IF NOT EXISTS marketpulse.postings
(
    seller_id      LowCardinality(String),
    posting_number String,
    sku            UInt64,
    offer_id       String,
    name           String,
    status         LowCardinality(String),
    status_rank    UInt8,
    quantity       UInt32,
    price          Float64,
    in_process_at  DateTime64(3, 'UTC'),
    observed_at    DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree(status_rank)
PARTITION BY toYYYYMM(in_process_at)
ORDER BY (seller_id, posting_number, sku);

-- Снимки остатков: ключ включает время снимка, дубли одного снимка схлопываются.
CREATE TABLE IF NOT EXISTS marketpulse.stocks
(
    seller_id   LowCardinality(String),
    sku         UInt64,
    offer_id    String,
    present     Int32,
    reserved    Int32,
    observed_at DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMM(observed_at)
ORDER BY (seller_id, sku, observed_at)
TTL toDateTime(observed_at) + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS marketpulse.kafka_postings
(
    event_id         String,
    seller_id        String,
    posting_number   String,
    sku              UInt64,
    offer_id         String,
    name             String,
    status           String,
    status_rank      UInt8,
    quantity         UInt32,
    price            Float64,
    in_process_at_ms Int64,
    observed_at_ms   Int64
)
ENGINE = Kafka
SETTINGS kafka_broker_list = 'kafka:9092',
         kafka_topic_list = 'marketplace.postings',
         kafka_group_name = 'clickhouse-postings',
         kafka_format = 'JSONEachRow',
         kafka_num_consumers = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS marketpulse.mv_postings TO marketpulse.postings AS
SELECT seller_id, posting_number, sku, offer_id, name, status, status_rank, quantity, price,
       fromUnixTimestamp64Milli(in_process_at_ms, 'UTC') AS in_process_at,
       fromUnixTimestamp64Milli(observed_at_ms, 'UTC')   AS observed_at
FROM marketpulse.kafka_postings;

CREATE TABLE IF NOT EXISTS marketpulse.kafka_stocks
(
    event_id       String,
    seller_id      String,
    sku            UInt64,
    offer_id       String,
    present        Int32,
    reserved       Int32,
    observed_at_ms Int64
)
ENGINE = Kafka
SETTINGS kafka_broker_list = 'kafka:9092',
         kafka_topic_list = 'marketplace.stocks',
         kafka_group_name = 'clickhouse-stocks',
         kafka_format = 'JSONEachRow',
         kafka_num_consumers = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS marketpulse.mv_stocks TO marketpulse.stocks AS
SELECT seller_id, sku, offer_id, present, reserved,
       fromUnixTimestamp64Milli(observed_at_ms, 'UTC') AS observed_at
FROM marketpulse.kafka_stocks;
