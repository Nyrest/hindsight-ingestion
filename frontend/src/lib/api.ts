import type {
  BrowseRequest,
  BrowseResult,
  Connectors,
  CronValidation,
  Credential,
  CredentialCreate,
  CredentialPatch,
  Dashboard,
  ErrorEnvelope,
  Health,
  Paginated,
  Run,
  RunDetail,
  RunsQuery,
  RunTriggered,
  Settings,
  SettingsPatch,
  StrategiesResult,
  Task,
  TaskInput,
  TaskPatch,
  TestResult,
} from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields?: Record<string, string>) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields ?? {};
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError;
}

export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

type Method = "GET" | "POST" | "PATCH" | "PUT" | "DELETE";

async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    // Resolve against the origin (no userinfo) so pages opened as
    // http://user:pass@host/ can still call the API.
    res = await fetch(`${window.location.origin}/api${path}`, {
      method,
      credentials: "same-origin",
      headers: {
        Accept: "application/json",
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (e) {
    throw new ApiError(0, "network", e instanceof Error ? e.message : "Network error");
  }

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }

  if (!res.ok) {
    const env = (data ?? {}) as Partial<ErrorEnvelope>;
    throw new ApiError(
      res.status,
      env.code ?? defaultCode(res.status),
      env.error || res.statusText || `HTTP ${res.status}`,
      env.fields,
    );
  }
  return data as T;
}

function defaultCode(status: number): string {
  if (status === 404) return "not_found";
  if (status === 409) return "conflict";
  if (status === 400 || status === 422) return "bad_request";
  if (status === 502 || status === 503) return "upstream";
  return "internal";
}

function qs(params: Record<string, string | number | undefined | null>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") sp.set(k, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}

const enc = encodeURIComponent;

export const api = {
  health: () => request<Health>("GET", "/health"),

  getSettings: () => request<Settings>("GET", "/settings"),
  patchSettings: (body: SettingsPatch) => request<Settings>("PATCH", "/settings", body),

  getConnectors: () => request<Connectors>("GET", "/connectors"),

  listCredentials: () => request<Credential[]>("GET", "/credentials"),
  getCredential: (id: string) => request<Credential>("GET", `/credentials/${enc(id)}`),
  createCredential: (body: CredentialCreate) => request<Credential>("POST", "/credentials", body),
  updateCredential: (id: string, body: CredentialPatch) =>
    request<Credential>("PATCH", `/credentials/${enc(id)}`, body),
  deleteCredential: (id: string) => request<void>("DELETE", `/credentials/${enc(id)}`),
  testCredential: (id: string) => request<TestResult>("POST", `/credentials/${enc(id)}/test`),
  oauthStart: (id: string) => request<{ authUrl: string }>("POST", `/credentials/${enc(id)}/oauth/start`),
  refreshCredential: (id: string) => request<Credential>("POST", `/credentials/${enc(id)}/refresh`),
  browse: (id: string, body: BrowseRequest) =>
    request<BrowseResult>("POST", `/credentials/${enc(id)}/browse`, body),
  strategies: (id: string, bankId: string) =>
    request<StrategiesResult>("GET", `/credentials/${enc(id)}/strategies${qs({ bankId })}`),

  listTasks: () => request<Task[]>("GET", "/tasks"),
  getTask: (id: string) => request<Task>("GET", `/tasks/${enc(id)}`),
  createTask: (body: TaskInput) => request<Task>("POST", "/tasks", body),
  updateTask: (id: string, body: TaskPatch) => request<Task>("PATCH", `/tasks/${enc(id)}`, body),
  deleteTask: (id: string) => request<void>("DELETE", `/tasks/${enc(id)}`),
  runTask: (id: string) => request<RunTriggered>("POST", `/tasks/${enc(id)}/run`),
  fullReconcileTask: (id: string) => request<RunTriggered>("POST", `/tasks/${enc(id)}/full-reconcile`),
  fullReingestTask: (id: string) => request<RunTriggered>("POST", `/tasks/${enc(id)}/full-reingest`),
  cancelTask: (id: string) => request<unknown>("POST", `/tasks/${enc(id)}/cancel`),
  taskRuns: (id: string, limit = 50, offset = 0) =>
    request<Paginated<Run>>("GET", `/tasks/${enc(id)}/runs${qs({ limit, offset })}`),
  validateCron: (cronExpression: string, cronTimezone: string) =>
    request<CronValidation>("POST", "/tasks/validate-cron", { cronExpression, cronTimezone }),

  listRuns: (q: RunsQuery) =>
    request<Paginated<Run>>(
      "GET",
      `/runs${qs({ limit: q.limit, offset: q.offset, status: q.status, taskId: q.taskId })}`,
    ),
  getRun: (id: string) => request<RunDetail>("GET", `/runs/${enc(id)}`),

  dashboard: () => request<Dashboard>("GET", "/dashboard"),
};
