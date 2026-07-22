import os
from collections import defaultdict
from datetime import date, datetime, time, timezone

import clickhouse_connect
import pendulum
import psycopg2
from airflow.sdk import dag, task


@dag(
    dag_id="bionicpro_reports_etl",
    schedule="0 1 * * *",
    start_date=pendulum.datetime(2026, 1, 1, tz="UTC"),
    catchup=False,
    tags=["bionicpro", "reports"],
)
def reports_etl():
    @task
    def extract_telemetry():
        with psycopg2.connect(os.environ["TELEMETRY_DATABASE_URL"]) as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """
                    SELECT client_id, recorded_at, battery_level, signal_quality, movements_count
                    FROM telemetry
                    WHERE recorded_at < CURRENT_DATE
                    ORDER BY recorded_at
                    """
                )
                return [
                    {
                        "client_id": row[0],
                        "recorded_at": row[1].isoformat(),
                        "battery_level": row[2],
                        "signal_quality": row[3],
                        "movements_count": row[4],
                    }
                    for row in cursor.fetchall()
                ]

    @task
    def aggregate_telemetry(telemetry):
        groups = defaultdict(
            lambda: {
                "events_count": 0,
                "battery_total": 0.0,
                "signal_total": 0.0,
                "movements_count": 0,
            }
        )

        for event in telemetry:
            report_date = datetime.fromisoformat(event["recorded_at"]).date().isoformat()
            group = groups[(event["client_id"], report_date)]
            group["events_count"] += 1
            group["battery_total"] += event["battery_level"]
            group["signal_total"] += event["signal_quality"]
            group["movements_count"] += event["movements_count"]

        rows = []
        for (client_id, report_date), values in sorted(groups.items()):
            count = values["events_count"]
            rows.append(
                {
                    "client_id": client_id,
                    "report_date": report_date,
                    "events_count": count,
                    "avg_battery_level": round(values["battery_total"] / count, 2),
                    "avg_signal_quality": round(values["signal_total"] / count, 4),
                    "movements_count": values["movements_count"],
                }
            )
        return rows

    @task
    def load_mart(rows):
        client = clickhouse_connect.get_client(
            host=os.environ["CLICKHOUSE_HOST"],
            port=int(os.environ.get("CLICKHOUSE_PORT", "8123")),
            username=os.environ["CLICKHOUSE_USER"],
            password=os.environ["CLICKHOUSE_PASSWORD"],
            database="reports",
        )
        processed_at = datetime.now(timezone.utc).replace(microsecond=0)
        processed_until = datetime.combine(date.today(), time.min, tzinfo=timezone.utc)

        crm_rows = client.query("SELECT count() FROM reports.crm_clients_current").result_rows[0][0]
        if crm_rows == 0:
            raise RuntimeError("CRM CDC snapshot is not loaded yet")

        client.command("TRUNCATE TABLE reports.user_report_mart_cdc")
        client.command("TRUNCATE TABLE reports.telemetry_daily_cdc_source")
        if rows:
            client.insert(
                "reports.telemetry_daily_cdc_source",
                [
                    [
                        row["client_id"],
                        date.fromisoformat(row["report_date"]),
                        row["events_count"],
                        row["avg_battery_level"],
                        row["avg_signal_quality"],
                        row["movements_count"],
                        processed_at,
                    ]
                    for row in rows
                ],
                column_names=[
                    "client_id",
                    "report_date",
                    "events_count",
                    "avg_battery_level",
                    "avg_signal_quality",
                    "movements_count",
                    "processed_at",
                ],
            )

        client.command("TRUNCATE TABLE reports.etl_watermark")
        client.insert(
            "reports.etl_watermark",
            [["reports", processed_until, processed_at]],
            column_names=["pipeline", "processed_until", "updated_at"],
        )
        return len(rows)

    load_mart(aggregate_telemetry(extract_telemetry()))


reports_etl()
