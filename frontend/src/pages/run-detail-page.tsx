import { AlertCircle, ArrowLeft, ChevronDown, FileQuestion, Loader2, Square } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useParams } from "react-router";
import { CopyButton } from "@/components/copy-button";
import { EmptyState } from "@/components/empty-state";
import { RunStatusBadge } from "@/components/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useRun } from "@/features/runs/api";
import { RunCountersGrid } from "@/features/runs/run-counters";
import { ModeLabel, TriggerLabel } from "@/features/runs/runs-table";
import { useCancelTask } from "@/features/tasks/api";
import { errorMessage, isApiError } from "@/lib/api";
import { formatDateTime, formatDuration, formatRelative, formatTime, runDuration } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { isActiveRun } from "@/lib/types";
import { cn } from "@/lib/utils";

const LEVEL_CLASSES: Record<string, string> = {
  debug: "text-muted-foreground",
  info: "text-sky-600 dark:text-sky-400",
  warn: "text-amber-600 dark:text-amber-400",
  warning: "text-amber-600 dark:text-amber-400",
  error: "text-red-600 dark:text-red-400",
};

export default function RunDetailPage() {
  const { t } = useTranslation();
  const { id } = useParams();
  const run = useRun(id);
  const cancel = useCancelTask();
  const now = useNow(1_000);

  if (run.isLoading) {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-72" />
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
          {Array.from({ length: 7 }).map((_, i) => (
            <Skeleton key={i} className="h-16" />
          ))}
        </div>
        <Skeleton className="h-64" />
      </div>
    );
  }

  if (run.isError || !run.data) {
    const notFound = isApiError(run.error) && run.error.status === 404;
    return (
      <EmptyState
        icon={FileQuestion}
        title={notFound ? t("runDetail.notFound") : t("common.loadFailed")}
        description={notFound ? undefined : errorMessage(run.error)}
        action={
          <Button asChild variant="outline">
            <Link to="/runs">{t("common.backTo", { page: t("nav.runs") })}</Link>
          </Button>
        }
      />
    );
  }

  const r = run.data;
  const active = isActiveRun(r.status);
  const log = r.log ?? [];
  const ops = r.operations ?? [];

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 space-y-1">
          <Link to="/runs" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
            <ArrowLeft className="size-3.5" /> {t("nav.runs")}
          </Link>
          <h1 className="flex flex-wrap items-center gap-3 text-xl font-semibold tracking-tight sm:text-2xl">
            <Link to={`/tasks/${r.taskId}`} className="truncate hover:underline">
              {r.taskName || r.taskId}
            </Link>
            <RunStatusBadge status={r.status} />
          </h1>
          <p className="font-mono text-xs text-muted-foreground">{r.id}</p>
        </div>
        {active && (
          <Button variant="outline" onClick={() => cancel.mutate(r.taskId)} disabled={cancel.isPending}>
            {cancel.isPending ? <Loader2 className="animate-spin" /> : <Square />}
            {t("tasks.actions.cancel")}
          </Button>
        )}
      </div>

      {r.errorMessage && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>{t("runDetail.error")}</AlertTitle>
          <AlertDescription className="font-mono text-xs break-all whitespace-pre-wrap">{r.errorMessage}</AlertDescription>
        </Alert>
      )}

      <Card className="gap-0 p-0">
        <dl className="grid grid-cols-2 gap-x-6 gap-y-4 p-5 text-sm md:grid-cols-3 lg:grid-cols-6">
          <Summary label={t("runs.columns.trigger")}>
            <TriggerLabel trigger={r.triggerType} />
          </Summary>
          <Summary label={t("runs.columns.mode")}>
            <ModeLabel mode={r.syncMode} />
          </Summary>
          <Summary label={t("runDetail.scheduledFor")}>{r.scheduledFor ? formatDateTime(r.scheduledFor) : "—"}</Summary>
          <Summary label={t("runs.columns.started")}>
            <span title={r.startedAt ? formatRelative(r.startedAt, now) : undefined}>{formatDateTime(r.startedAt)}</span>
          </Summary>
          <Summary label={t("runDetail.finished")}>{active ? t("runDetail.inProgress") : formatDateTime(r.finishedAt)}</Summary>
          <Summary label={t("runs.columns.duration")}>
            <span className="tabular-nums">{formatDuration(runDuration(r.startedAt, r.finishedAt, now))}</span>
          </Summary>
        </dl>
      </Card>

      <RunCountersGrid run={r} />

      <div className="grid gap-4 lg:grid-cols-2">
        <CursorBlock label={t("runDetail.cursorBefore")} value={r.cursorBefore} />
        <CursorBlock label={t("runDetail.cursorAfter")} value={r.cursorAfter} />
      </div>

      <Card className="gap-0 p-0">
        <CardHeader className="border-b px-5 py-4 [.border-b]:pb-4">
          <CardTitle className="text-base">
            {t("runDetail.operations")} <span className="font-normal text-muted-foreground">({ops.length})</span>
          </CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {ops.length === 0 ? (
            <p className="px-5 py-6 text-center text-sm text-muted-foreground">{t("runDetail.noOperations")}</p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="pl-5">{t("runDetail.opColumns.remoteId")}</TableHead>
                    <TableHead>{t("runDetail.opColumns.type")}</TableHead>
                    <TableHead>{t("runDetail.opColumns.status")}</TableHead>
                    <TableHead>{t("runDetail.opColumns.retries")}</TableHead>
                    <TableHead>{t("runDetail.opColumns.created")}</TableHead>
                    <TableHead className="pr-5">{t("runDetail.opColumns.updated")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {ops.map((op) => (
                    <TableRow key={op.id}>
                      <TableCell className="max-w-[16rem] truncate pl-5 font-mono text-xs" title={op.remoteOperationId}>
                        {op.remoteOperationId || "—"}
                      </TableCell>
                      <TableCell className="text-sm">{op.type}</TableCell>
                      <TableCell>
                        <span
                          className={cn(
                            "text-xs font-medium",
                            /fail|error/i.test(op.status)
                              ? "text-red-600 dark:text-red-400"
                              : /complete|success|succeeded|done/i.test(op.status)
                                ? "text-emerald-600 dark:text-emerald-400"
                                : "text-sky-600 dark:text-sky-400",
                          )}
                        >
                          {op.status}
                        </span>
                      </TableCell>
                      <TableCell className="tabular-nums">{op.retryCount}</TableCell>
                      <TableCell className="text-xs whitespace-nowrap">{formatDateTime(op.createdAt)}</TableCell>
                      <TableCell className="pr-5 text-xs whitespace-nowrap">{formatDateTime(op.updatedAt)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="gap-0 p-0">
        <CardHeader className="flex flex-row items-center justify-between border-b px-5 py-4 [.border-b]:pb-4">
          <CardTitle className="flex items-center gap-2 text-base">
            {t("runDetail.log")} <span className="font-normal text-muted-foreground">({log.length})</span>
            {active && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
          </CardTitle>
          {log.length > 0 && (
            <CopyButton value={log.map((l) => `${l.time} ${l.level.toUpperCase()} ${l.message}`).join("\n")} className="size-8" />
          )}
        </CardHeader>
        <CardContent className="p-0">
          {log.length === 0 ? (
            <p className="px-5 py-6 text-center text-sm text-muted-foreground">{t("runDetail.noLog")}</p>
          ) : (
            <div className="max-h-[32rem] overflow-auto bg-muted/30 py-2 font-mono text-xs leading-relaxed">
              {log.map((l, i) => (
                <div key={i} className="flex gap-3 px-5 py-px hover:bg-accent/50">
                  <span className="shrink-0 text-muted-foreground tabular-nums" title={formatDateTime(l.time)}>
                    {formatTime(l.time)}
                  </span>
                  <span className={cn("w-12 shrink-0 font-semibold uppercase", LEVEL_CLASSES[l.level.toLowerCase()] ?? "text-muted-foreground")}>
                    {l.level}
                  </span>
                  <span className="min-w-0 break-all whitespace-pre-wrap">{l.message}</span>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function Summary({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 space-y-1">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="truncate">{children}</dd>
    </div>
  );
}

function CursorBlock({ label, value }: { label: string; value: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  let pretty = value;
  try {
    if (value) pretty = JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    /* not JSON; show raw */
  }
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-xl border bg-card">
      <div className="flex items-center justify-between gap-2 px-4 py-3">
        <CollapsibleTrigger className="flex min-w-0 flex-1 items-center gap-2 text-left text-sm font-medium" disabled={!value}>
          <ChevronDown className={cn("size-4 shrink-0 text-muted-foreground transition-transform", !open && "-rotate-90")} />
          {label}
          {!value && <span className="font-normal text-muted-foreground">— {t("runDetail.noCursor")}</span>}
          {value && !open && <span className="min-w-0 truncate font-mono text-xs font-normal text-muted-foreground">{value}</span>}
        </CollapsibleTrigger>
        {value && <CopyButton value={value} className="size-7" />}
      </div>
      <CollapsibleContent>
        <pre className="max-h-72 overflow-auto border-t bg-muted/30 px-4 py-3 font-mono text-xs break-all whitespace-pre-wrap">{pretty}</pre>
      </CollapsibleContent>
    </Collapsible>
  );
}
