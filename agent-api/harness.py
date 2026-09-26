"""Минимальный харнесс агента: LLM с tool calling поверх инструментов MCP-сервера.

Намеренно без фреймворка, чтобы было видно, из чего состоит агентный цикл:
  - инструменты берутся из MCP (list_tools) и конвертируются в OpenAI tools-схему;
  - инструменты с побочными эффектами скрыты от модели, пока пользователь явно не
    разрешил действия (allow_actions) — модель не может "случайно" завести задачу;
  - результаты инструментов обрезаются перед возвратом в контекст (бюджет контекста);
  - ошибка инструмента возвращается модели как текст, а не роняет запрос;
  - лимит шагов: по исчерпании модель обязана ответить без инструментов;
  - полный trace вызовов отдаётся клиенту для отладки и UI.
"""

from __future__ import annotations

import json
import os
import time
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from typing import Any, AsyncIterator

from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client
from openai import AsyncOpenAI

MCP_URL = os.getenv("MCP_URL", "http://mcp-server:8000/mcp")
SIDE_EFFECT_TOOLS = {"create_bitrix24_task"}
MAX_STEPS = int(os.getenv("AGENT_MAX_STEPS", "8"))
TOOL_RESULT_BUDGET = int(os.getenv("AGENT_TOOL_RESULT_CHARS", "6000"))

SYSTEM_PROMPT = """Ты — аналитик продаж для продавцов на маркетплейсе. Отвечай по-русски, коротко и по делу.
Правила:
- Любые цифры бери только из инструментов, ничего не выдумывай. Если данных нет — так и скажи.
- На любой вопрос про продажи продавца сначала вызови detect_sales_drops и stock_forecast по нему:
  общие итоги (sales_summary) скрывают падение отдельных товаров.
- Объясняя падение продаж, проверь цену (price_change_pct) и остатки (stock_forecast): падение из-за
  роста цены, из-за того что товар закончился, и падение без видимой причины — это разные выводы.
- В конце дай 1–3 конкретных действия.
- Задачу в Битрикс24 создавай только если пользователь прямо попросил и инструмент доступен."""


@asynccontextmanager
async def mcp_session() -> AsyncIterator[ClientSession]:
    async with streamablehttp_client(MCP_URL) as (read, write, _):
        async with ClientSession(read, write) as session:
            await session.initialize()
            yield session


def tool_result_text(result: Any) -> str:
    parts = [c.text for c in result.content if getattr(c, "type", "") == "text"]
    text = "\n".join(parts) if parts else json.dumps(getattr(result, "structuredContent", None), ensure_ascii=False)
    return text


def truncate(text: str, limit: int = TOOL_RESULT_BUDGET) -> str:
    if len(text) <= limit:
        return text
    return text[:limit] + f"\n…[обрезано: {len(text) - limit} символов; уточни запрос или сузь период]"


async def call_tool_json(session: ClientSession, name: str, args: dict[str, Any]) -> Any:
    res = await session.call_tool(name, args)
    if res.isError:
        raise RuntimeError(tool_result_text(res))
    if res.structuredContent is not None:
        sc = res.structuredContent
        # FastMCP заворачивает не-dict результаты (списки) в {"result": ...}
        return sc.get("result", sc) if isinstance(sc, dict) and set(sc) == {"result"} else sc
    return json.loads(tool_result_text(res))


@dataclass
class AgentResult:
    answer: str
    trace: list[dict[str, Any]] = field(default_factory=list)
    steps: int = 0
    model: str = ""


def llm() -> AsyncOpenAI:
    return AsyncOpenAI(
        base_url=os.getenv("LLM_BASE_URL", "https://openrouter.ai/api/v1"),
        api_key=os.getenv("LLM_API_KEY", ""),
    )


async def run_agent(message: str, history: list[dict[str, str]] | None = None, allow_actions: bool = False) -> AgentResult:
    model = os.getenv("LLM_MODEL", "qwen/qwen3.8-27b:free")
    fallbacks = [m.strip() for m in os.getenv("LLM_FALLBACK_MODELS", "").split(",") if m.strip()]
    # OpenRouter сам переключается на следующую модель из списка при ошибке/лимите
    extra = {"models": [model, *fallbacks]} if fallbacks else None
    client = llm()
    result = AgentResult(answer="", model=model)
    messages: list[dict[str, Any]] = [{"role": "system", "content": SYSTEM_PROMPT}]
    for h in (history or [])[-10:]:
        if h.get("role") in ("user", "assistant") and h.get("content"):
            messages.append({"role": h["role"], "content": h["content"]})
    messages.append({"role": "user", "content": message})

    async with mcp_session() as session:
        listed = await session.list_tools()
        tools = [
            {"type": "function", "function": {"name": t.name, "description": t.description or "", "parameters": t.inputSchema}}
            for t in listed.tools
            if allow_actions or t.name not in SIDE_EFFECT_TOOLS
        ]

        for step in range(MAX_STEPS + 1):
            result.steps = step + 1
            final_step = step == MAX_STEPS
            resp = await client.chat.completions.create(
                model=model,
                messages=messages,
                tools=None if final_step else tools,
                temperature=0.2,
                extra_body=extra,
            )
            result.model = resp.model or model
            msg = resp.choices[0].message
            if not msg.tool_calls or final_step:
                result.answer = msg.content or ""
                return result

            messages.append({
                "role": "assistant",
                "content": msg.content or "",
                "tool_calls": [tc.model_dump() for tc in msg.tool_calls],
            })
            for tc in msg.tool_calls:
                name = tc.function.name
                started = time.perf_counter()
                args: dict[str, Any] = {}
                try:
                    args = json.loads(tc.function.arguments or "{}")
                    if name in SIDE_EFFECT_TOOLS and not allow_actions:
                        raise PermissionError("действия запрещены: пользователь не разрешил создавать задачи")
                    res = await session.call_tool(name, args)
                    text = tool_result_text(res)
                    ok = not res.isError
                except Exception as e:  # ошибка инструмента — это данные для модели
                    text, ok = f"ОШИБКА инструмента {name}: {e}", False
                result.trace.append({
                    "tool": name, "args": args, "ok": ok,
                    "ms": round((time.perf_counter() - started) * 1000),
                    "result_preview": text[:600],
                })
                messages.append({"role": "tool", "tool_call_id": tc.id, "content": truncate(text)})
    return result
