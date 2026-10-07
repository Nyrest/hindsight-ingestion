import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { POLL_ACTIVE_MS, POLL_IDLE_MS, qk } from "@/lib/query";

export function useDashboard() {
  return useQuery({
    queryKey: qk.dashboard,
    queryFn: api.dashboard,
    refetchInterval: (q) => ((q.state.data?.runningTasks.length ?? 0) > 0 ? POLL_ACTIVE_MS : POLL_IDLE_MS),
  });
}
