// The two public reads the shell makes with no bearer: the version line for
// the sidebar footer and the readiness probe behind the header dot. They
// live apart from api.ts because that layer attaches the session and
// treats a 401 as the session ending; neither applies here.

const TIMEOUT_MS = 10000;

export type VersionInfo = { version: string; commit: string; go: string; profile: string; runtimes?: string[] };

export type Readiness = { state: "ok" | "degraded" | "unreachable"; components: Record<string, string> };

async function read(path: string): Promise<{ ok: boolean; body: unknown }> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  try {
    const resp = await fetch(path, { signal: ctrl.signal });
    const body = await resp.json().catch(() => null);
    return { ok: resp.ok, body };
  } finally {
    clearTimeout(timer);
  }
}

// version reads GET /version, or null when strazad did not answer.
export async function version(): Promise<VersionInfo | null> {
  try {
    const { ok, body } = await read("/version");
    if (!ok || !body || typeof body !== "object") return null;
    const v = body as Record<string, unknown>;
    const runtimes = Array.isArray(v.runtimes) ? v.runtimes.map(String) : undefined;
    return { version: String(v.version || ""), commit: String(v.commit || ""), go: String(v.go || ""), profile: String(v.profile || ""), runtimes };
  } catch {
    return null;
  }
}

// readiness reads GET /readyz into the three states the header dot shows:
// ok, degraded with the failing component names, or unreachable.
export async function readiness(): Promise<Readiness> {
  try {
    const { ok, body } = await read("/readyz");
    const found = body && typeof body === "object" ? (body as { components?: Record<string, string> }).components : undefined;
    return { state: ok ? "ok" : "degraded", components: found || ({} as Record<string, string>) };
  } catch {
    return { state: "unreachable", components: {} };
  }
}
