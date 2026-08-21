// Client for the wizard's own HTTP API (internal/setup/api.go).
//
// Separate from src/lib/api.ts on purpose: that one talks to a running
// quartermaster, and at the point this code executes nothing is installed yet.
// The only server in the picture is the one inside quartermaster-setup.

/** Injected into index.html by the Go server, per run. */
declare global {
  interface Window {
    __QM_SETUP_TOKEN?: string;
  }
}

const token = window.__QM_SETUP_TOKEN ?? "";

export type VariantOption = { id: string; label: string; note: string };
export type ComponentOption = {
  id: string;
  name: string;
  summary: string;
  selected: boolean;
};

export type Probe = {
  os: string;
  defaultDir: string;
  gpus: string[] | null;
  variant: string;
  variants: VariantOption[] | null;
  components: ComponentOption[] | null;
  homeDir: string;
};

export type ScanResult = {
  path: string;
  exists: boolean;
  count: number;
  sizeGB: number;
  error?: string;
};

export type Phase =
  | "idle"
  | "placing"
  | "configuring"
  | "backends"
  | "done"
  | "error";

export type Status = {
  phase: Phase;
  step: string;
  detail: string;
  downloaded: number;
  total: number;
  warnings: string[] | null;
  error: string;
  installDir: string;
};

export type Choices = {
  dir: string;
  modelsRoot: string;
  variant: string;
  components: string[];
  autostart: boolean;
};

/**
 * The token travels as a custom header rather than a query parameter or a
 * cookie: a header forces any cross-origin caller through a preflight the
 * server answers with no CORS headers at all, and it keeps the secret out of
 * URLs, where a referrer could carry it somewhere it does not belong.
 */
async function call<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
    headers: {
      "X-QM-Setup-Token": token,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let parsed: any = null;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    /* a non-JSON body means the server errored below the handler */
  }
  if (!res.ok) {
    throw new Error(parsed?.error || text || `HTTP ${res.status}`);
  }
  return parsed as T;
}

export const probe = () => call<Probe>("/api/setup/probe");
export const scan = (path: string) => call<ScanResult>("/api/setup/scan", { path });
export const install = (c: Choices) => call<Status>("/api/setup/install", c);
export const status = () => call<Status>("/api/setup/status");
export const finish = (launch: boolean) =>
  call<{ ok: boolean }>("/api/setup/finish", { launch });
