"""Интеграция с Битрикс24 через входящий вебхук (REST API, метод tasks.task.add).

Если BITRIX24_WEBHOOK_URL не задан, работает в режиме dry-run: возвращает ровно тот
запрос, который был бы отправлен. Так демо работает без портала, а при подключении
реального портала код не меняется."""

from __future__ import annotations

import os
from datetime import datetime, timedelta, timezone
from typing import Any

import httpx


def build_task_payload(title: str, description: str, deadline_days: int | None, responsible_id: int) -> dict[str, Any]:
    fields: dict[str, Any] = {
        "TITLE": title[:255],
        "DESCRIPTION": description,
        "RESPONSIBLE_ID": responsible_id,
    }
    if deadline_days:
        deadline = datetime.now(timezone.utc) + timedelta(days=deadline_days)
        fields["DEADLINE"] = deadline.replace(microsecond=0).isoformat()
    return {"fields": fields}


def create_task(title: str, description: str, deadline_days: int | None = None) -> dict[str, Any]:
    webhook = os.getenv("BITRIX24_WEBHOOK_URL", "").rstrip("/")
    payload = build_task_payload(title, description, deadline_days, int(os.getenv("BITRIX24_RESPONSIBLE_ID", "1")))
    if not webhook:
        return {"dry_run": True, "method": "tasks.task.add", "payload": payload,
                "note": "BITRIX24_WEBHOOK_URL не задан — задача не отправлена"}
    resp = httpx.post(f"{webhook}/tasks.task.add.json", json=payload, timeout=15)
    resp.raise_for_status()
    data = resp.json()
    if "error" in data:
        raise RuntimeError(f"Bitrix24: {data.get('error')}: {data.get('error_description')}")
    task = data.get("result", {}).get("task", {})
    return {"dry_run": False, "task_id": task.get("id"), "title": task.get("title")}
