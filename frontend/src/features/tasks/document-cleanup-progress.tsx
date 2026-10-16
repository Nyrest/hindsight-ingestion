import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { api } from "@/lib/api";
import { formatDuration, formatNumber } from "@/lib/format";
import { qk } from "@/lib/query";

/** Mounted only while the delete request is pending; each attempt starts fresh. */
export function DocumentCleanupProgress({ taskId }: { taskId: string }) {
  const { t } = useTranslation();
  const progress = useQuery({
    queryKey: qk.documentCleanupProgress(taskId),
    queryFn: ({ signal }) => api.taskDocumentCleanupProgress(taskId, signal),
    refetchInterval: 1_000,
    staleTime: 0,
    gcTime: 0,
    retry: false,
  });
  const state = progress.data;
  const total = state?.totalCount;
  const completed = state?.completedCount ?? 0;
  const percent = total == null ? undefined : total === 0 ? 100 : Math.floor(completed / total * 100);
  const phase = state?.phase === "deleting" || state?.phase === "finalizing" ? state.phase : "preparing";
  const eta = progress.isError ? null : state?.estimatedRemainingSeconds;
  const remaining = phase === "finalizing" ? "—" : eta == null ? t("tasks.clearDocuments.progress.estimating") : formatDuration(eta * 1_000);

  return (
    <div className="min-w-0 space-y-2 text-sm">
      <div className="flex items-center justify-between gap-3" aria-live="polite" role="status">
        <span>{t(`tasks.clearDocuments.progress.${phase}`)}</span>
        {percent !== undefined && <span className="shrink-0 tabular-nums">{formatNumber(percent)}%</span>}
      </div>
      <div role="progressbar" aria-label={t("tasks.clearDocuments.progress.label")}
        aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}
        className="h-2 overflow-hidden rounded-full bg-muted">
        <div className={`h-full rounded-full bg-primary transition-[width] duration-300 motion-reduce:transition-none ${percent === undefined ? "w-1/3 animate-pulse motion-reduce:animate-none" : ""}`}
          style={percent === undefined ? undefined : { width: `${percent}%` }} />
      </div>
      {total != null && state && (
        <p className="text-muted-foreground tabular-nums">{t("tasks.clearDocuments.progress.count", {
          completed: formatNumber(state.completedCount), total: formatNumber(total), deleted: formatNumber(state.deletedCount),
        })}</p>
      )}
      <p className="text-muted-foreground tabular-nums">
        {t("tasks.clearDocuments.progress.eta", { time: remaining })}
      </p>
      {progress.isError && <p className="text-muted-foreground" role="status">{t("tasks.clearDocuments.progress.unavailable")}</p>}
    </div>
  );
}
