"use client";

import { useEffect, useState } from "react";
import type { ChatMessage, DailyPoint, Overview } from "./types";

const SELLERS: Record<string, string> = { "": "Все продавцы", "1001": "1001 · Одежда", "2002": "2002 · Дом" };

const rub = (v: number) => new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 0 }).format(v) + " ₽";
const num = (v: number | null | undefined, d = 1) =>
  v == null ? "—" : new Intl.NumberFormat("ru-RU", { maximumFractionDigits: d }).format(v);

// Ответ модели — markdown; нужен только **жирный**, поэтому без библиотеки и без innerHTML.
function Answer({ text }: { text: string }) {
  return (
    <>
      {text.split(/(\*\*[^*]+\*\*)/g).map((part, i) =>
        part.startsWith("**") && part.endsWith("**") ? <strong key={i}>{part.slice(2, -2)}</strong> : part,
      )}
    </>
  );
}

function UnitsChart({ daily }: { daily: DailyPoint[] }) {
  const byDay = new Map<string, number>();
  for (const p of daily) byDay.set(p.day, (byDay.get(p.day) ?? 0) + p.units);
  const days = [...byDay.entries()];
  const max = Math.max(1, ...days.map(([, v]) => v));
  return (
    <div className="chart" role="img" aria-label="Продано штук по дням">
      {days.map(([day, v]) => (
        <div key={day} className="bar-col" title={`${day}: ${v} шт`}>
          <div className="bar" style={{ height: `${(v / max) * 100}%` }} />
          <span>{day.slice(8, 10)}</span>
        </div>
      ))}
    </div>
  );
}

export default function Page() {
  const [seller, setSeller] = useState("");
  const [data, setData] = useState<Overview | null>(null);
  const [error, setError] = useState("");
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("Почему упали продажи и что делать?");
  const [allowActions, setAllowActions] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setData(null);
    setError("");
    fetch(`/api/overview?seller_id=${seller}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then(setData)
      .catch((e) => setError(String(e)));
  }, [seller]);

  async function send() {
    if (!input.trim() || busy) return;
    const question = seller ? `${input} (продавец ${seller})` : input;
    const history = messages.map(({ role, content }) => ({ role, content }));
    setMessages((m) => [...m, { role: "user", content: question }]);
    setInput("");
    setBusy(true);
    try {
      const r = await fetch("/api/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ message: question, history, allow_actions: allowActions }),
      });
      const d = await r.json();
      setMessages((m) => [
        ...m,
        r.ok
          ? { role: "assistant", content: d.answer, trace: d.trace, model: d.model }
          : { role: "assistant", content: `Ошибка: ${d.detail ?? r.status}` },
      ]);
    } finally {
      setBusy(false);
    }
  }

  return (
    <main>
      <header>
        <h1>MarketPulse</h1>
        <select value={seller} onChange={(e) => setSeller(e.target.value)}>
          {Object.entries(SELLERS).map(([id, label]) => (
            <option key={id} value={id}>{label}</option>
          ))}
        </select>
      </header>

      {error && <p className="error">Не удалось загрузить данные: {error}</p>}
      {!data && !error && <p className="muted">Загрузка…</p>}

      {data && (
        <>
          <section className="kpis">
            {data.summary.totals.map((t) => (
              <div key={t.seller_id} className="card">
                <div className="muted">Продавец {t.seller_id} · {data.summary.period_days} дн.</div>
                <div className="kpi">{rub(t.revenue)}</div>
                <div className="muted">{num(t.units, 0)} шт · {num(t.orders, 0)} заказов · отмены {num(t.cancel_rate_pct)}%</div>
              </div>
            ))}
          </section>

          <section className="card">
            <h2>Продано штук по дням</h2>
            <UnitsChart daily={data.summary.daily} />
          </section>

          <section className="grid2">
            <div className="card">
              <h2>Падение продаж</h2>
              {data.drops.length === 0 ? <p className="muted">Аномалий нет</p> : (
                <table>
                  <thead><tr><th>Товар</th><th>Было/день</th><th>Стало/день</th><th>Цена</th></tr></thead>
                  <tbody>
                    {data.drops.map((d) => (
                      <tr key={d.sku}>
                        <td>{d.name}<div className="muted">{d.seller_id} · {d.sku}</div></td>
                        <td>{num(d.baseline_units_per_day)}</td>
                        <td className="bad">{num(d.recent_units_per_day)} ({Math.round((d.ratio - 1) * 100)}%)</td>
                        <td className={Math.abs(d.price_change_pct) > 5 ? "bad" : ""}>
                          {d.price_change_pct > 0 ? "+" : ""}{num(d.price_change_pct)}%
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
            <div className="card">
              <h2>Риск закончиться (7 дней)</h2>
              {data.stock_risks.length === 0 ? <p className="muted">Остатков хватает</p> : (
                <table>
                  <thead><tr><th>Товар</th><th>Остаток</th><th>Продажи/день</th><th>Хватит на</th></tr></thead>
                  <tbody>
                    {data.stock_risks.map((s) => (
                      <tr key={s.sku}>
                        <td>{s.name}<div className="muted">{s.seller_id} · {s.sku}</div></td>
                        <td>{num(s.present, 0)}</td>
                        <td>{num(s.units_per_day)}</td>
                        <td className="bad">{num(s.days_of_cover)} дн.</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </section>
        </>
      )}

      <section className="card chat">
        <h2>AI-аналитик</h2>
        <div className="messages">
          {messages.map((m, i) => (
            <div key={i} className={`msg ${m.role}`}>
              <div className="content">{m.role === "assistant" ? <Answer text={m.content} /> : m.content}</div>
              {m.trace && m.trace.length > 0 && (
                <details>
                  <summary>Инструменты MCP: {m.trace.map((t) => t.tool).join(" → ")} · {m.model}</summary>
                  {m.trace.map((t, j) => (
                    <pre key={j}>{`${t.ok ? "✓" : "✗"} ${t.tool}(${JSON.stringify(t.args)}) · ${t.ms} мс\n${t.result_preview}`}</pre>
                  ))}
                </details>
              )}
            </div>
          ))}
          {busy && <div className="msg assistant muted">Агент думает и вызывает инструменты…</div>}
        </div>
        <div className="composer">
          <input value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => e.key === "Enter" && send()}
            placeholder="Спросите про продажи, остатки, товары…" />
          <button onClick={send} disabled={busy}>Спросить</button>
        </div>
        <label className="muted">
          <input type="checkbox" checked={allowActions} onChange={(e) => setAllowActions(e.target.checked)} />{" "}
          Разрешить агенту создавать задачи в Битрикс24
        </label>
      </section>
    </main>
  );
}
