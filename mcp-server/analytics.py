"""Аналитические запросы к ClickHouse. Отделены от MCP-обвязки, чтобы их было
просто тестировать и переиспользовать."""

from __future__ import annotations

import os
import re
from typing import Any

import clickhouse_connect

_FORBIDDEN = re.compile(
    r"\b(insert|alter|drop|truncate|create|rename|attach|detach|optimize|grant|revoke|"
    r"kill|system|set|settings|delete|update|exchange|undrop)\b",
    re.IGNORECASE,
)


class UnsafeQuery(ValueError):
    pass


def guard_sql(sql: str) -> str:
    """Первая линия защиты для произвольного SQL от LLM. Вторая — пользователь
    ClickHouse с readonly-профилем (см. clickhouse/users.d/mcp_readonly.xml)."""
    q = sql.strip().rstrip(";").strip()
    if not q:
        raise UnsafeQuery("пустой запрос")
    if ";" in q:
        raise UnsafeQuery("разрешён только один запрос")
    if not re.match(r"^(select|with)\b", q, re.IGNORECASE):
        raise UnsafeQuery("разрешены только SELECT/WITH")
    if _FORBIDDEN.search(re.sub(r"'(?:[^'\\]|\\.)*'", "''", q)):
        raise UnsafeQuery("запрос содержит запрещённое ключевое слово")
    return q


def client():
    return clickhouse_connect.get_client(
        host=os.getenv("CLICKHOUSE_HOST", "clickhouse"),
        port=int(os.getenv("CLICKHOUSE_PORT", "8123")),
        username=os.getenv("CLICKHOUSE_USER", "mcp_readonly"),
        password=os.getenv("CLICKHOUSE_PASSWORD", ""),
        database="marketpulse",
    )


def _rows(sql: str, params: dict[str, Any] | None = None) -> list[dict[str, Any]]:
    res = client().query(sql, parameters=params or {})
    out = []
    for row in res.result_rows:
        item = {}
        for k, v in zip(res.column_names, row):
            if isinstance(v, float):
                v = round(v, 2)
            elif hasattr(v, "isoformat"):
                v = v.isoformat()
            item[k] = v
        out.append(item)
    return out


_SELLER_FILTER = "AND ({seller:String} = '' OR seller_id = {seller:String})"


def list_products(seller_id: str = "") -> list[dict[str, Any]]:
    return _rows(
        f"""
        SELECT seller_id, sku, any(offer_id) AS offer_id, any(name) AS name,
               count() AS postings_30d
        FROM postings FINAL
        WHERE in_process_at >= now() - INTERVAL 30 DAY {_SELLER_FILTER}
        GROUP BY seller_id, sku ORDER BY seller_id, sku
        """,
        {"seller": seller_id},
    )


def sales_summary(days: int = 7, seller_id: str = "") -> dict[str, Any]:
    days = max(1, min(days, 90))
    params = {"days": days, "seller": seller_id}
    totals = _rows(
        f"""
        SELECT seller_id,
               countIf(status != 'cancelled')                         AS orders,
               sumIf(quantity, status != 'cancelled')                 AS units,
               sumIf(quantity * price, status != 'cancelled')         AS revenue,
               round(countIf(status = 'cancelled') / count() * 100, 1) AS cancel_rate_pct
        FROM postings FINAL
        WHERE in_process_at >= now() - INTERVAL {{days:UInt32}} DAY {_SELLER_FILTER}
        GROUP BY seller_id ORDER BY seller_id
        """,
        params,
    )
    daily = _rows(
        f"""
        SELECT toDate(in_process_at) AS day, seller_id,
               sumIf(quantity, status != 'cancelled')         AS units,
               sumIf(quantity * price, status != 'cancelled') AS revenue
        FROM postings FINAL
        WHERE in_process_at >= now() - INTERVAL {{days:UInt32}} DAY {_SELLER_FILTER}
        GROUP BY day, seller_id ORDER BY day, seller_id
        """,
        params,
    )
    return {"period_days": days, "totals": totals, "daily": daily}


def sku_sales_trend(sku: int, days: int = 14) -> dict[str, Any]:
    days = max(1, min(days, 90))
    rows = _rows(
        """
        SELECT toDate(in_process_at) AS day,
               sumIf(quantity, status != 'cancelled') AS units,
               round(avg(price), 2)                   AS avg_price,
               sumIf(quantity * price, status != 'cancelled') AS revenue
        FROM postings FINAL
        WHERE sku = {sku:UInt64} AND in_process_at >= now() - INTERVAL {days:UInt32} DAY
        GROUP BY day ORDER BY day
        """,
        {"sku": sku, "days": days},
    )
    info = _rows("SELECT any(seller_id) AS seller_id, any(name) AS name FROM postings WHERE sku = {sku:UInt64}", {"sku": sku})
    return {"sku": sku, **(info[0] if info else {}), "days": rows}


def detect_sales_drops(
    recent_days: int = 3,
    baseline_days: int = 14,
    threshold: float = 0.7,
    min_baseline_units_per_day: float = 3.0,
    seller_id: str = "",
) -> list[dict[str, Any]]:
    """Товары, у которых средние продажи за последние recent_days дней упали ниже
    threshold от базового периода. Возвращает и изменение цены — чтобы отличать
    "подняли цену" от внешних причин."""
    return _rows(
        f"""
        WITH
            now() - INTERVAL {{recent:UInt32}} DAY AS recent_from,
            recent_from - INTERVAL {{base:UInt32}} DAY AS base_from
        SELECT seller_id, sku, any(name) AS name,
               sumIf(quantity, in_process_at >= recent_from) / {{recent:UInt32}} AS recent_units_per_day,
               sumIf(quantity, in_process_at < recent_from) / {{base:UInt32}}    AS baseline_units_per_day,
               round(recent_units_per_day / baseline_units_per_day, 2)           AS ratio,
               avgIf(price, in_process_at >= recent_from) AS recent_avg_price,
               avgIf(price, in_process_at < recent_from)  AS baseline_avg_price,
               round((recent_avg_price / baseline_avg_price - 1) * 100, 1) AS price_change_pct
        FROM postings FINAL
        WHERE status != 'cancelled' AND in_process_at >= base_from {_SELLER_FILTER}
        GROUP BY seller_id, sku
        HAVING baseline_units_per_day >= {{min_base:Float64}} AND ratio < {{threshold:Float64}}
        ORDER BY ratio
        """,
        {
            "recent": max(1, recent_days),
            "base": max(1, baseline_days),
            "threshold": threshold,
            "min_base": min_baseline_units_per_day,
            "seller": seller_id,
        },
    )


def stock_forecast(horizon_days: int = 7, seller_id: str = "") -> list[dict[str, Any]]:
    """Товары, которые закончатся в пределах horizon_days при текущем темпе продаж
    (средний за 7 дней). Товар с нулевым остатком тоже попадает в список."""
    return _rows(
        f"""
        WITH latest AS (
            SELECT seller_id, sku, argMax(present, observed_at) AS present, max(observed_at) AS stock_at
            FROM stocks WHERE 1 {_SELLER_FILTER} GROUP BY seller_id, sku
        ),
        velocity AS (
            SELECT seller_id, sku, any(name) AS name, sum(quantity) / 7 AS units_per_day
            FROM postings FINAL
            WHERE status != 'cancelled' AND in_process_at >= now() - INTERVAL 7 DAY {_SELLER_FILTER}
            GROUP BY seller_id, sku
        )
        SELECT l.seller_id AS seller_id, l.sku AS sku, v.name AS name, l.present AS present,
               v.units_per_day AS units_per_day,
               if(v.units_per_day > 0, round(l.present / v.units_per_day, 1), NULL) AS days_of_cover,
               l.stock_at AS stock_at
        FROM latest AS l
        LEFT JOIN velocity AS v ON v.seller_id = l.seller_id AND v.sku = l.sku
        WHERE l.present <= 0 OR (v.units_per_day > 0 AND l.present / v.units_per_day <= {{horizon:Float64}})
        ORDER BY days_of_cover ASC NULLS FIRST
        """,
        {"horizon": float(horizon_days), "seller": seller_id},
    )


def run_readonly_sql(sql: str, max_rows: int = 200) -> dict[str, Any]:
    q = guard_sql(sql)
    res = client().query(q, settings={"max_result_rows": max(1, min(max_rows, 1000)), "result_overflow_mode": "break"})
    rows = [dict(zip(res.column_names, [v.isoformat() if hasattr(v, "isoformat") else v for v in r])) for r in res.result_rows]
    return {"columns": list(res.column_names), "rows": rows, "row_count": len(rows)}
