import createClient from "openapi-fetch";
import type { paths } from "./openapi";
import type { SQLPreviewRequest, BrowseRequest, CredentialCreate, CredentialPatch, ErrorEnvelope, RunsQuery, SettingsPatch, PostProcessTestRequest, TaskInput, TaskPatch } from "./types";

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

const client = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "same-origin",
  fetch: async (request) => {
    if (!["GET", "HEAD", "OPTIONS"].includes(request.method)) {
      request.headers.set("X-CSRF-Protection", "1");
    }
    try { return await fetch(request); }
    catch (error) {
      if (request.signal.aborted) throw error;
      throw new ApiError(0, "network", errorMessage(error));
    }
  },
});

async function unwrap<T>(pending: Promise<{ data?: T; error?: ErrorEnvelope | string | { status: string }; response: Response }>): Promise<T> {
  const { data, error, response } = await pending;
  if (!response.ok) {
    if (typeof error === "string") throw new ApiError(response.status, "unauthorized", error.trim());
    if (error && "status" in error) throw new ApiError(response.status, error.status, response.statusText);
    throw new ApiError(response.status, error?.code ?? "internal", error?.error ?? response.statusText, error?.fields);
  }
  return data!;
}

const pathParams = (id: string) => ({ path: { id } });

export const api = {
  health: () => unwrap(client.GET("/api/health")),
  getSettings: () => unwrap(client.GET("/api/settings")),
  patchSettings: (body: SettingsPatch) => unwrap(client.PATCH("/api/settings", { body })),
  getConnectors: () => unwrap(client.GET("/api/connectors")),
  listCredentials: () => unwrap(client.GET("/api/credentials")),
  getCredential: (id: string) => unwrap(client.GET("/api/credentials/{id}", { params: pathParams(id) })),
  createCredential: (body: CredentialCreate) => unwrap(client.POST("/api/credentials", { body })),
  updateCredential: (id: string, body: CredentialPatch) => unwrap(client.PATCH("/api/credentials/{id}", { params: pathParams(id), body })),
  deleteCredential: (id: string) => unwrap(client.DELETE("/api/credentials/{id}", { params: pathParams(id) })),
  testCredential: (id: string) => unwrap(client.POST("/api/credentials/{id}/test", { params: pathParams(id) })),
  oauthStart: (id: string) => unwrap(client.POST("/api/credentials/{id}/oauth/start", { params: pathParams(id) })),
  refreshCredential: (id: string) => unwrap(client.POST("/api/credentials/{id}/refresh", { params: pathParams(id) })),
  browse: (id: string, body: BrowseRequest, signal?: AbortSignal) => unwrap(client.POST("/api/credentials/{id}/browse", { params: pathParams(id), body, signal })),
  previewSQL: (id: string, body: SQLPreviewRequest, signal?: AbortSignal) => unwrap(client.POST("/api/credentials/{id}/sql/preview", { params: pathParams(id), body, signal })),
  strategies: (id: string, bankId: string) => unwrap(client.GET("/api/credentials/{id}/strategies", { params: { ...pathParams(id), query: { bankId } } })),
  tags: (id: string, bankId: string, q = "") => unwrap(client.GET("/api/credentials/{id}/tags", { params: { ...pathParams(id), query: { bankId, q, limit: 100, offset: 0 } } })),
  deleteTaskDocuments: (id: string) => unwrap(client.DELETE("/api/tasks/{id}/documents", { params: pathParams(id) })),
  taskDocumentCleanupProgress: (id: string, signal?: AbortSignal) => unwrap(client.GET("/api/tasks/{id}/documents/progress", { params: pathParams(id), signal })),
  listTasks: () => unwrap(client.GET("/api/tasks")),
  getTask: (id: string) => unwrap(client.GET("/api/tasks/{id}", { params: pathParams(id) })),
  createTask: (body: TaskInput) => unwrap(client.POST("/api/tasks", { body })),
  updateTask: (id: string, body: TaskPatch) => unwrap(client.PATCH("/api/tasks/{id}", { params: pathParams(id), body })),
  deleteTask: (id: string, deleteDocuments = false) => unwrap(client.DELETE("/api/tasks/{id}", { params: { ...pathParams(id), query: { deleteDocuments } } })),
  runTask: (id: string) => unwrap(client.POST("/api/tasks/{id}/run", { params: pathParams(id) })),
  dryRunTask: (id: string, signal?: AbortSignal) => unwrap(client.POST("/api/tasks/{id}/dry-run", { params: pathParams(id), signal })),
  fullReconcileTask: (id: string) => unwrap(client.POST("/api/tasks/{id}/full-reconcile", { params: pathParams(id) })),
  fullReingestTask: (id: string) => unwrap(client.POST("/api/tasks/{id}/full-reingest", { params: pathParams(id) })),
  cancelTask: (id: string) => unwrap(client.POST("/api/tasks/{id}/cancel", { params: pathParams(id) })),
  taskRuns: (id: string, limit = 50, offset = 0) => unwrap(client.GET("/api/tasks/{id}/runs", { params: { ...pathParams(id), query: { limit, offset } } })),
  testPostProcessing: (body: PostProcessTestRequest) => unwrap(client.POST("/api/post-processing/test", { body })),
  validateCron: (cronExpression: string) => unwrap(client.POST("/api/tasks/validate-cron", { body: { cronExpression } })),
  listRuns: (query: RunsQuery) => unwrap(client.GET("/api/runs", { params: { query } })),
  getRun: (id: string) => unwrap(client.GET("/api/runs/{id}", { params: pathParams(id) })),
  dashboard: () => unwrap(client.GET("/api/dashboard")),
};
