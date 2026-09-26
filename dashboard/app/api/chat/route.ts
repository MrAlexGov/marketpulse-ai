const AGENT_API = process.env.AGENT_API_URL ?? "http://agent-api:8080";

export const dynamic = "force-dynamic";

export async function POST(req: Request) {
  const res = await fetch(`${AGENT_API}/api/chat`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: await req.text(),
    cache: "no-store",
  });
  return new Response(await res.text(), { status: res.status, headers: { "Content-Type": "application/json" } });
}
