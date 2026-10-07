import { CalendarClock, Hand } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router";
import { RunStatusBadge } from "@/components/status-badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, formatDuration, formatRelative, runDuration } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import type { Run } from "@/lib/types";
import { RunCountersCompact } from "./run-counters";

export function TriggerLabel({ trigger }: { trigger: string }) {
  const { t } = useTranslation();
  const Icon = trigger === "manual" ? Hand : CalendarClock;
  return (
    <span className="inline-flex items-center gap-1.5 text-muted-foreground">
      <Icon className="size-3.5" />
      {t(`runs.trigger.${trigger}`, { defaultValue: trigger })}
    </span>
  );
}

export function ModeLabel({ mode }: { mode: string }) {
  const { t } = useTranslation();
  return (
    <span className="rounded border px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground">
      {t(`runs.mode.${mode}`, { defaultValue: mode })}
    </span>
  );
}

/** Responsive runs table (cards on mobile). */
export function RunsTable({ runs, showTask = true }: { runs: Run[]; showTask?: boolean }) {
  const { t } = useTranslation();
  const now = useNow(5_000);
  const navigate = useNavigate();

  return (
    <>
      <Card className="hidden gap-0 overflow-hidden p-0 md:block">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40 hover:bg-muted/40">
              {showTask && <TableHead className="pl-4">{t("runs.columns.task")}</TableHead>}
              <TableHead className={showTask ? undefined : "pl-4"}>{t("runs.columns.status")}</TableHead>
              <TableHead>{t("runs.columns.trigger")}</TableHead>
              <TableHead>{t("runs.columns.mode")}</TableHead>
              <TableHead>{t("runs.columns.started")}</TableHead>
              <TableHead>{t("runs.columns.duration")}</TableHead>
              <TableHead className="pr-4">{t("runs.columns.counters")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((r) => (
              <TableRow key={r.id} className="cursor-pointer" onClick={() => navigate(`/runs/${r.id}`)}>
                {showTask && (
                  <TableCell className="max-w-[14rem] pl-4">
                    <Link
                      to={`/runs/${r.id}`}
                      className="block truncate font-medium hover:underline"
                      onClick={(e) => e.stopPropagation()}
                    >
                      {r.taskName || r.taskId}
                    </Link>
                  </TableCell>
                )}
                <TableCell className={showTask ? undefined : "pl-4"}>
                  <RunStatusBadge status={r.status} />
                </TableCell>
                <TableCell className="text-sm">
                  <TriggerLabel trigger={r.triggerType} />
                </TableCell>
                <TableCell>
                  <ModeLabel mode={r.syncMode} />
                </TableCell>
                <TableCell className="text-sm whitespace-nowrap" title={formatDateTime(r.startedAt)}>
                  {r.startedAt ? formatRelative(r.startedAt, now) : t("runs.notStarted")}
                </TableCell>
                <TableCell className="text-sm tabular-nums">
                  {formatDuration(runDuration(r.startedAt, r.finishedAt, now))}
                </TableCell>
                <TableCell className="pr-4">
                  <RunCountersCompact run={r} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      <div className="grid gap-2 md:hidden">
        {runs.map((r) => (
          <Link key={r.id} to={`/runs/${r.id}`}>
            <Card className="gap-2 p-3.5 transition-colors hover:bg-accent/40">
              <div className="flex items-center justify-between gap-2">
                <span className="truncate text-sm font-medium">{showTask ? r.taskName || r.taskId : formatDateTime(r.startedAt)}</span>
                <RunStatusBadge status={r.status} />
              </div>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                <TriggerLabel trigger={r.triggerType} />
                <ModeLabel mode={r.syncMode} />
                <span>{r.startedAt ? formatRelative(r.startedAt, now) : t("runs.notStarted")}</span>
                <span className="tabular-nums">{formatDuration(runDuration(r.startedAt, r.finishedAt, now))}</span>
              </div>
              <RunCountersCompact run={r} />
            </Card>
          </Link>
        ))}
      </div>
    </>
  );
}
