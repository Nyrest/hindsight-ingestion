import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { POLL_ACTIVE_MS, POLL_IDLE_MS, qk } from "@/lib/query";
import { isActiveRun, type RunsQuery } from "@/lib/types";

export function useRuns(q: RunsQuery) {
  return useQuery({
    queryKey: qk.runList(q),
    queryFn: () => api.listRuns(q),
    placeholderData: (prev) => prev,
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some((r) => isActiveRun(r.status)) ? POLL_ACTIVE_MS : POLL_IDLE_MS,
  });
}

export function useRun(id: string | undefined) {
  return useQuery({
    queryKey: qk.run(id ?? ""),
    queryFn: () => api.getRun(id!),
    enabled: !!id,
    refetchInterval: (query) => (isActiveRun(query.state.data?.status) ? POLL_ACTIVE_MS : false),
  });
}
