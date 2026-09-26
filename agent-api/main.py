from __future__ import annotations

import asyncio
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from harness import call_tool_json, mcp_session, run_agent

app = FastAPI(title="MarketPulse agent API")


class ChatRequest(BaseModel):
    message: str = Field(min_length=1, max_length=4000)
    history: list[dict[str, str]] = []
    allow_actions: bool = False


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/api/overview")
async def overview(seller_id: str = "") -> dict[str, Any]:
    """Данные для дашборда — те же MCP-инструменты, но без LLM."""
    async with mcp_session() as s:
        summary, drops, stock = await asyncio.gather(
            call_tool_json(s, "sales_summary", {"days": 14, "seller_id": seller_id}),
            call_tool_json(s, "detect_sales_drops", {"seller_id": seller_id}),
            call_tool_json(s, "stock_forecast", {"horizon_days": 7, "seller_id": seller_id}),
        )
    return {"summary": summary, "drops": drops, "stock_risks": stock}


@app.post("/api/chat")
async def chat(req: ChatRequest) -> dict[str, Any]:
    try:
        res = await run_agent(req.message, req.history, req.allow_actions)
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"агент недоступен: {e}") from e
    return {"answer": res.answer, "trace": res.trace, "steps": res.steps, "model": res.model}
