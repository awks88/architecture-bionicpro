CREATE DATABASE IF NOT EXISTS reports;

CREATE TABLE IF NOT EXISTS reports.user_report_mart (
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

CREATE TABLE IF NOT EXISTS reports.etl_watermark (
    pipeline LowCardinality(String),
    processed_until DateTime('UTC'),
    updated_at DateTime('UTC')
)
ENGINE = MergeTree
ORDER BY pipeline;
