import { afterAll, afterEach, beforeAll, expect, test } from "bun:test";

const browserWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
const originalFetch = globalThis.fetch;
let api: typeof import("../src/lib/api").api;
let ApiError: typeof import("../src/lib/api").ApiError;

beforeAll(async () => {
  Object.defineProperty(globalThis, "window", { configurable: true, value: { location: { origin: "http://localhost" } } });
  ({ api, ApiError } = await import("../src/lib/api"));
});
afterEach(() => { globalThis.fetch = originalFetch; });
afterAll(() => {
  if (browserWindow) Object.defineProperty(globalThis, "window", browserWindow);
  else Reflect.deleteProperty(globalThis, "window");
});

test("unavailable health response preserves its degraded status", async () => {
  globalThis.fetch = (async () => Response.json({ status: "degraded", authEnabled: true, dbType: "sqlite", version: "test" }, {
    status: 503, statusText: "Service Unavailable",
  })) as typeof fetch;
  await expect(api.health()).rejects.toMatchObject({ status: 503, code: "degraded", message: "Service Unavailable" });
});

test("browse cancellation reaches fetch and keeps the abort error", async () => {
  const controller = new AbortController();
  const aborted = new DOMException("browse closed", "AbortError");
  let requestStarted!: () => void;
  const started = new Promise<void>((resolve) => { requestStarted = resolve; });
  globalThis.fetch = ((request: Request) => new Promise<Response>((_resolve, reject) => {
    expect(request.headers.get("X-CSRF-Protection")).toBe("1");
    request.signal.addEventListener("abort", () => reject(request.signal.reason), { once: true });
    requestStarted();
  })) as typeof fetch;
  const pending = api.browse("account", {}, controller.signal);
  await started;
  controller.abort(aborted);
  await expect(pending).rejects.toBe(aborted);
});

test("transport failures produce a network API error", async () => {
  globalThis.fetch = (async () => { throw new TypeError("connection unavailable"); }) as typeof fetch;
  try {
    await api.listTasks();
    throw new Error("request unexpectedly succeeded");
  } catch (error) {
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 0, code: "network", message: "connection unavailable" });
  }
});
