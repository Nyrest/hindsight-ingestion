import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "./api";
import type { RunsQuery } from "./types";

export const POLL_ACTIVE_MS = 3_000;
export const POLL_IDLE_MS = 30_000;

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: true,
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false;
        return failureCount < 2;
      },
    },
    mutations: { retry: false },
  },
});

export const qk = {
  health: ["health"] as const,
  settings: ["settings"] as const,
  connectors: ["connectors"] as const,
  credentials: ["credentials"] as const,
  credential: (id: string) => ["credentials", id] as const,
  browse: (id: string, parentId: string, kind: string) => ["credentials", id, "browse", parentId, kind] as const,
  strategies: (id: string, bankId: string) => ["credentials", id, "strategies", bankId] as const,
  tasks: ["tasks"] as const,
  task: (id: string) => ["tasks", id] as const,
  taskRuns: (id: string, limit: number, offset: number) => ["tasks", id, "runs", limit, offset] as const,
  cron: (expr: string, tz: string) => ["cron", expr, tz] as const,
  runs: ["runs"] as const,
  runList: (q: RunsQuery) => ["runs", "list", q] as const,
  run: (id: string) => ["runs", id] as const,
  dashboard: ["dashboard"] as const,
};
