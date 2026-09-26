"""MCP-сервер MarketPulse: инструменты аналитики продаж маркетплейса для LLM-агентов.

Любой MCP-клиент (agent-api из этого репозитория, Claude Desktop, Cursor и т.п.)
подключается по Streamable HTTP: http://<host>:8000/mcp"""

from __future__ import annotations

import os
from typing import Any

from mcp.server.fastmcp import FastMCP

import analytics
import bitrix24

mcp = FastMCP(
    "marketpulse",
    instructions=(
        "Аналитика продаж продавцов на маркетплейсе (формат Ozon Seller API). "
        "seller_id: '1001' — одежда, '2002' — товары для дома. Все даты в UTC."
    ),
    host="0.0.0.0",
    port=int(os.getenv("PORT", "8000")),
    stateless_http=True,
)


@mcp.tool()
def list_products(seller_id: str = "") -> list[dict[str, Any]]:
    """Каталог товаров (SKU, артикул, название) с числом отправлений за 30 дней.
    seller_id пустой — все продавцы."""
    return analytics.list_products(seller_id)


@mcp.tool()
def sales_summary(days: int = 7, seller_id: str = "") -> dict[str, Any]:
    """Итоги продаж за последние `days` дней: заказы, штуки, выручка, доля отмен,
    плюс разбивка по дням."""
    return analytics.sales_summary(days, seller_id)


@mcp.tool()
def sku_sales_trend(sku: int, days: int = 14) -> dict[str, Any]:
    """Продажи одного товара по дням: штуки, средняя цена, выручка."""
    return analytics.sku_sales_trend(sku, days)


@mcp.tool()
def detect_sales_drops(
    recent_days: int = 3,
    baseline_days: int = 14,
    threshold: float = 0.7,
    seller_id: str = "",
) -> list[dict[str, Any]]:
    """Товары, у которых продажи за последние `recent_days` дней упали ниже `threshold`
    (доля) от среднего за предыдущие `baseline_days`. price_change_pct показывает,
    менялась ли цена: падение без изменения цены — повод искать внешнюю причину,
    падение до нуля — проверить остаток (stock_forecast)."""
    return analytics.detect_sales_drops(recent_days, baseline_days, threshold, seller_id=seller_id)


@mcp.tool()
def stock_forecast(horizon_days: int = 7, seller_id: str = "") -> list[dict[str, Any]]:
    """Товары, которые закончатся в ближайшие `horizon_days` дней при текущем темпе
    продаж (days_of_cover = остаток / продажи в день за 7 дней)."""
    return analytics.stock_forecast(horizon_days, seller_id)


@mcp.tool()
def run_readonly_sql(sql: str, max_rows: int = 200) -> dict[str, Any]:
    """Произвольный SELECT к ClickHouse (база marketpulse) — если готовых инструментов
    не хватает. Таблицы: postings (seller_id, posting_number, sku, offer_id, name, status,
    quantity, price, in_process_at) — используйте FROM postings FINAL; stocks (seller_id,
    sku, offer_id, present, reserved, observed_at). Только чтение."""
    return analytics.run_readonly_sql(sql, max_rows)


@mcp.tool()
def create_bitrix24_task(title: str, description: str, deadline_days: int = 1) -> dict[str, Any]:
    """Создаёт задачу в Битрикс24 (tasks.task.add). Действие с побочным эффектом —
    вызывать только если пользователь явно попросил завести задачу."""
    return bitrix24.create_task(title, description, deadline_days)


if __name__ == "__main__":
    mcp.run(transport="streamable-http")
