import { NextRequest } from "next/server";

const AGENT_API = process.env.AGENT_API_URL ?? "http://agent-api:8080";

export const dynamic = "force-dynamic";

export async function GET(req: NextRequest) {
  const seller = req.nextUrl.searchParams.get("seller_id") ?? "";
  const res = await fetch(`${AGENT_API}/api/overview?seller_id=${encodeURIComponent(seller)}`, { cache: "no-store" });
  return new Response(await res.text(), { status: res.status, headers: { "Content-Type": "application/json" } });
}
