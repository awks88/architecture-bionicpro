CREATE TABLE IF NOT EXISTS reports.crm_clients_kafka (
    raw String
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list = 'kafka:9092',
    kafka_topic_list = 'crm.public.clients',
    kafka_group_name = 'clickhouse-crm-cdc',
    kafka_format = 'JSONAsString',
    kafka_num_consumers = 1,
    kafka_skip_broken_messages = 10;

CREATE TABLE IF NOT EXISTS reports.crm_clients_cdc (
    client_id UInt64,
    user_id String,
    full_name String,
    prosthesis_model String,
    crm_status String,
    version UInt64,
    is_deleted UInt8
)
ENGINE = ReplacingMergeTree(version)
ORDER BY client_id;

CREATE MATERIALIZED VIEW IF NOT EXISTS reports.crm_clients_cdc_mv
TO reports.crm_clients_cdc
AS
WITH JSONExtractString(raw, 'op') AS op
SELECT
    if(op = 'd', JSONExtractUInt(raw, 'before', 'client_id'), JSONExtractUInt(raw, 'after', 'client_id')) AS client_id,
    if(op = 'd', JSONExtractString(raw, 'before', 'user_id'), JSONExtractString(raw, 'after', 'user_id')) AS user_id,
    if(op = 'd', JSONExtractString(raw, 'before', 'full_name'), JSONExtractString(raw, 'after', 'full_name')) AS full_name,
    if(op = 'd', JSONExtractString(raw, 'before', 'prosthesis_model'), JSONExtractString(raw, 'after', 'prosthesis_model')) AS prosthesis_model,
    if(op = 'd', JSONExtractString(raw, 'before', 'crm_status'), JSONExtractString(raw, 'after', 'crm_status')) AS crm_status,
    if(JSONExtractUInt(raw, 'source', 'lsn') > 0, JSONExtractUInt(raw, 'source', 'lsn'), JSONExtractUInt(raw, 'ts_ms')) AS version,
    toUInt8(op = 'd') AS is_deleted
FROM reports.crm_clients_kafka
WHERE op IN ('c', 'r', 'u', 'd');

CREATE VIEW IF NOT EXISTS reports.crm_clients_current AS
SELECT
    client_id,
    tupleElement(latest, 1) AS user_id,
    tupleElement(latest, 2) AS full_name,
    tupleElement(latest, 3) AS prosthesis_model,
    tupleElement(latest, 4) AS crm_status
FROM (
    SELECT
        client_id,
        argMax(tuple(user_id, full_name, prosthesis_model, crm_status, is_deleted), version) AS latest
    FROM reports.crm_clients_cdc
    GROUP BY client_id
)
WHERE tupleElement(latest, 5) = 0;

CREATE TABLE IF NOT EXISTS reports.telemetry_daily_cdc_source (
    client_id UInt64,
    report_date Date,
    events_count UInt64,
    avg_battery_level Float64,
    avg_signal_quality Float64,
    movements_count UInt64,
    processed_at DateTime('UTC')
)
ENGINE = MergeTree
ORDER BY (client_id, report_date);

CREATE TABLE IF NOT EXISTS reports.user_report_mart_cdc (
    user_id String,
    client_id UInt64,
    full_name String,
    prosthesis_model String,
    crm_status LowCardinality(String),
    report_date Date,
    events_count UInt64,
    avg_battery_level Float64,
    avg_signal_quality Float64,
    movements_count UInt64,
    processed_at DateTime('UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(report_date)
ORDER BY (user_id, report_date);

CREATE MATERIALIZED VIEW IF NOT EXISTS reports.user_report_mart_cdc_mv
TO reports.user_report_mart_cdc
AS
SELECT
    clients.user_id AS user_id,
    telemetry.client_id AS client_id,
    clients.full_name AS full_name,
    clients.prosthesis_model AS prosthesis_model,
    clients.crm_status AS crm_status,
    telemetry.report_date AS report_date,
    telemetry.events_count AS events_count,
    telemetry.avg_battery_level AS avg_battery_level,
    telemetry.avg_signal_quality AS avg_signal_quality,
    telemetry.movements_count AS movements_count,
    telemetry.processed_at AS processed_at
FROM reports.telemetry_daily_cdc_source AS telemetry
INNER JOIN reports.crm_clients_current AS clients
    ON clients.client_id = telemetry.client_id;
