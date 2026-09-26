export type SellerTotals = {
  seller_id: string;
  orders: number;
  units: number;
  revenue: number;
  cancel_rate_pct: number;
};

export type DailyPoint = { day: string; seller_id: string; units: number; revenue: number };

export type SalesDrop = {
  seller_id: string;
  sku: number;
  name: string;
  recent_units_per_day: number;
  baseline_units_per_day: number;
  ratio: number;
  price_change_pct: number;
};

export type StockRisk = {
  seller_id: string;
  sku: number;
  name: string;
  present: number;
  units_per_day: number | null;
  days_of_cover: number | null;
};

export type Overview = {
  summary: { period_days: number; totals: SellerTotals[]; daily: DailyPoint[] };
  drops: SalesDrop[];
  stock_risks: StockRisk[];
};

export type TraceStep = { tool: string; args: Record<string, unknown>; ok: boolean; ms: number; result_preview: string };

export type ChatMessage = { role: "user" | "assistant"; content: string; trace?: TraceStep[]; model?: string };
