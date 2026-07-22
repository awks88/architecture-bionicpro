CREATE TABLE telemetry (
    telemetry_id BIGSERIAL PRIMARY KEY,
    client_id BIGINT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    battery_level INTEGER NOT NULL,
    signal_quality DOUBLE PRECISION NOT NULL,
    movements_count INTEGER NOT NULL
);

INSERT INTO telemetry (client_id, recorded_at, battery_level, signal_quality, movements_count) VALUES
    (1, CURRENT_DATE - INTERVAL '1 day' + INTERVAL '8 hours', 92, 0.96, 128),
    (1, CURRENT_DATE - INTERVAL '1 day' + INTERVAL '12 hours', 74, 0.91, 215),
    (1, CURRENT_DATE - INTERVAL '1 day' + INTERVAL '18 hours', 48, 0.88, 172),
    (1, CURRENT_DATE - INTERVAL '2 days' + INTERVAL '10 hours', 86, 0.94, 190),
    (1, CURRENT_DATE - INTERVAL '2 days' + INTERVAL '17 hours', 52, 0.89, 164),
    (2, CURRENT_DATE - INTERVAL '1 day' + INTERVAL '9 hours', 81, 0.93, 143),
    (2, CURRENT_DATE - INTERVAL '1 day' + INTERVAL '16 hours', 57, 0.90, 188),
    (3, CURRENT_DATE - INTERVAL '1 day' + INTERVAL '11 hours', 67, 0.85, 109);
