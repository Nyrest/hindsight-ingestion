import { afterAll, afterEach, beforeAll, expect, test } from "bun:test";
import { InfiniteQueryObserver, QueryClient } from "@tanstack/react-query";
import type { BrowseItem, BrowseRequest, BrowseResult } from "../src/lib/types";

const browserWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
const originalFetch = globalThis.fetch;
let infiniteBrowseOptions: typeof import("../src/features/credentials/api").infiniteBrowseOptions;
const clients: QueryClient[] = [];

beforeAll(async () => {
  Object.defineProperty(globalThis, "window", { configurable: true, value: { location: { origin: "http://localhost" } } });
  ({ infiniteBrowseOptions } = await import("../src/features/credentials/api"));
});
afterEach(() => {
  clients.splice(0).forEach((client) => client.clear());
  globalThis.fetch = originalFetch;
});
afterAll(() => {
  if (browserWindow) Object.defineProperty(globalThis, "window", browserWindow);
  else Reflect.deleteProperty(globalThis, "window");
});

function item(id: string): BrowseItem {
  return { id, name: id, path: id, kind: "page", selectable: true, hasChildren: false };
}

function batch(ids: string[], nextCursor?: string): BrowseResult {
  return { items: ids.map(item), parentId: "", breadcrumbs: [{ id: "", name: "Pages" }], supportsSearch: true, nextCursor };
}

function observe(request: BrowseRequest = { kind: "page" }) {
  const client = new QueryClient();
  clients.push(client);
  const options = infiniteBrowseOptions("notion", request);
  return { client, options, observer: new InfiniteQueryObserver(client, { ...options, enabled: false }) };
}

test("browse fetches one batch at a time and deduplicates overlapping results", async () => {
  const requests: BrowseRequest[] = [];
  globalThis.fetch = (async (request: Request) => {
    const body = await request.json() as BrowseRequest;
    requests.push(body);
    return Response.json(body.cursor ? batch(["first", "second"]) : batch(["first"], "next"));
  }) as typeof fetch;
  const { observer } = observe();
  let result = await observer.refetch();
  expect(requests).toHaveLength(1);
  expect(result.data?.items.map((entry) => entry.id)).toEqual(["first"]);
  expect(result.hasNextPage).toBe(true);
  result = await observer.fetchNextPage();
  expect(requests.map((request) => request.cursor)).toEqual(["", "next"]);
  expect(result.data?.items.map((entry) => entry.id)).toEqual(["first", "second"]);
  expect(result.hasNextPage).toBe(false);
  await observer.fetchNextPage();
  expect(requests).toHaveLength(2);
});

test("a next-batch failure preserves results and can retry the same cursor", async () => {
  let nextAttempts = 0;
  globalThis.fetch = (async (request: Request) => {
    const body = await request.json() as BrowseRequest;
    if (!body.cursor) return Response.json(batch(["selected"], "next"));
    if (++nextAttempts === 1) return Response.json({ code: "upstream", error: "Notion unavailable" }, { status: 502 });
    return Response.json(batch(["later"]));
  }) as typeof fetch;
  const { observer } = observe();
  await observer.refetch();
  let result = await observer.fetchNextPage();
  expect(result.isFetchNextPageError).toBe(true);
  expect(result.error?.message).toBe("Notion unavailable");
  expect(result.data?.items.map((entry) => entry.id)).toEqual(["selected"]);
  result = await observer.fetchNextPage();
  expect(nextAttempts).toBe(2);
  expect(result.isError).toBe(false);
  expect(result.data?.items.map((entry) => entry.id)).toEqual(["selected", "later"]);
});

test("empty batches remain pageable and repeated cursors stop pagination", async () => {
  globalThis.fetch = (async () => Response.json(batch([], "same"))) as typeof fetch;
  const { observer } = observe();
  let result = await observer.refetch();
  expect(result.hasNextPage).toBe(true);
  result = await observer.fetchNextPage();
  expect(result.data?.items).toEqual([]);
  expect(result.hasNextPage).toBe(false);
});

test("search and parent changes start a separate cursor chain", async () => {
  const requests: BrowseRequest[] = [];
  globalThis.fetch = (async (request: Request) => {
    const body = await request.json() as BrowseRequest;
    requests.push(body);
    return Response.json(batch([body.query || "root"], "next"));
  }) as typeof fetch;
  const { client, observer } = observe();
  await observer.refetch();
  await observer.fetchNextPage();
  observer.setOptions({ ...infiniteBrowseOptions("notion", { kind: "page", parentId: "parent", query: "notes" }), enabled: false });
  const result = await observer.refetch();
  expect(requests.at(-1)).toMatchObject({ cursor: "", query: "notes", parentId: "parent" });
  expect(result.data?.items.map((entry) => entry.id)).toEqual(["notes"]);
  // Normal browse queries (for inline mail folders) must not share an infinite-query cache.
  expect(client.getQueryCache().getAll().every((entry) => entry.queryKey.at(-1) === "infinite")).toBe(true);
});

test("refresh resets to the first batch instead of reloading the whole list", async () => {
  const requests: BrowseRequest[] = [];
  globalThis.fetch = (async (request: Request) => {
    const body = await request.json() as BrowseRequest;
    requests.push(body);
    return Response.json(body.cursor ? batch(["later"]) : batch(["first"], "next"));
  }) as typeof fetch;
  const { client, options, observer } = observe();
  await observer.refetch();
  await observer.fetchNextPage();
  await client.resetQueries({ queryKey: options.queryKey, exact: true });
  const result = await observer.refetch();
  expect(requests.map((request) => request.cursor)).toEqual(["", "next", ""]);
  expect(result.data?.items.map((entry) => entry.id)).toEqual(["first"]);
});

test("reopening a previously browsed list does not download all cached batches again", async () => {
  let requests = 0;
  globalThis.fetch = (async (request: Request) => {
    requests++;
    const body = await request.json() as BrowseRequest;
    return Response.json(body.cursor ? batch(["later"]) : batch(["first"], "next"));
  }) as typeof fetch;
  const { client, options, observer } = observe();
  const unsubscribe = observer.subscribe(() => {});
  try {
    await observer.refetch();
    await observer.fetchNextPage();
    const cached = client.getQueryData(options.queryKey);
    client.setQueryData(options.queryKey, cached, { updatedAt: Date.now() - 60_000 });
    observer.setOptions({ ...options, enabled: true });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(requests).toBe(2);
    expect(observer.getCurrentResult().data?.items.map((entry) => entry.id)).toEqual(["first", "later"]);
  } finally {
    unsubscribe();
  }
});
