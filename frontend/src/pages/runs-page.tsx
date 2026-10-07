import { ChevronLeft, ChevronRight, History, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useRuns } from "@/features/runs/api";
import { RunsTable } from "@/features/runs/runs-table";
import { useTasks } from "@/features/tasks/api";
import { errorMessage } from "@/lib/api";
import { formatNumber } from "@/lib/format";
import { RUN_STATUSES } from "@/lib/types";

const PAGE_SIZE = 25;
const ALL = "__all__";

export default function RunsPage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const taskId = params.get("taskId") ?? "";
  const page = Math.max(0, Number(params.get("page") ?? 0) || 0);
  const tasks = useTasks();
  const runs = useRuns({ limit: PAGE_SIZE, offset: page * PAGE_SIZE, status: status || undefined, taskId: taskId || undefined });

  function setParam(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setParams(next, { replace: true });
  }

  const total = runs.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const filtered = !!status || !!taskId;

  return (
    <>
      <PageHeader title={t("runs.title")} description={t("runs.description")} />

      <div className="mb-4 flex flex-col gap-2 sm:flex-row sm:items-center">
        <Select value={status || ALL} onValueChange={(v) => setParam("status", v === ALL ? "" : v)}>
          <SelectTrigger className="w-full sm:w-48" aria-label={t("runs.filters.status")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t("runs.filters.allStatuses")}</SelectItem>
            {RUN_STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`runStatus.${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={taskId || ALL} onValueChange={(v) => setParam("taskId", v === ALL ? "" : v)}>
          <SelectTrigger className="w-full sm:w-64" aria-label={t("runs.filters.task")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t("runs.filters.allTasks")}</SelectItem>
            {(tasks.data ?? []).map((task) => (
              <SelectItem key={task.id} value={task.id}>
                {task.name}
              </SelectItem>
            ))}
            {taskId && tasks.data && !tasks.data.some((x) => x.id === taskId) && (
              <SelectItem value={taskId}>{taskId}</SelectItem>
            )}
          </SelectContent>
        </Select>
        {filtered && (
          <Button variant="ghost" size="sm" onClick={() => setParams({}, { replace: true })}>
            <X /> {t("runs.filters.clear")}
          </Button>
        )}
        <span className="text-xs text-muted-foreground sm:ml-auto">
          {runs.data ? t("runs.total", { count: total, formatted: formatNumber(total) }) : ""}
        </span>
      </div>

      {runs.isLoading ? (
        <Card className="gap-0 p-0">
          {Array.from({ length: 8 }).map((_, i) => (
            <div key={i} className="flex items-center gap-4 border-b p-4 last:border-0">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-5 w-20" />
              <Skeleton className="ml-auto h-4 w-48" />
            </div>
          ))}
        </Card>
      ) : runs.isError ? (
        <EmptyState
          icon={History}
          title={t("common.loadFailed")}
          description={errorMessage(runs.error)}
          action={<Button variant="outline" onClick={() => runs.refetch()}>{t("common.retry")}</Button>}
        />
      ) : !runs.data?.items.length ? (
        <EmptyState
          icon={History}
          title={filtered ? t("runs.empty.filteredTitle") : t("runs.empty.title")}
          description={filtered ? t("runs.empty.filteredDescription") : t("runs.empty.description")}
          action={
            filtered ? (
              <Button variant="outline" onClick={() => setParams({}, { replace: true })}>
                {t("runs.filters.clear")}
              </Button>
            ) : undefined
          }
        />
      ) : (
        <>
          <RunsTable runs={runs.data.items} />
          {pages > 1 && (
            <div className="mt-4 flex items-center justify-end gap-2">
              <span className="text-xs text-muted-foreground">
                {t("common.pageOf", { page: page + 1, pages })}
              </span>
              <Button
                variant="outline"
                size="icon-sm"
                disabled={page === 0}
                onClick={() => setParam("page", String(page - 1))}
                aria-label={t("common.previous")}
              >
                <ChevronLeft />
              </Button>
              <Button
                variant="outline"
                size="icon-sm"
                disabled={page + 1 >= pages}
                onClick={() => setParam("page", String(page + 1))}
                aria-label={t("common.next")}
              >
                <ChevronRight />
              </Button>
            </div>
          )}
        </>
      )}
    </>
  );
}
