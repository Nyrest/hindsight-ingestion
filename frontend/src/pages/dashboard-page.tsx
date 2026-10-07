import {
  Activity,
  AlertTriangle,
  CalendarClock,
  CheckCircle2,
  Database,
  KeyRound,
  Link2,
  ListChecks,
  Loader2,
  Plus,
  XCircle,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { toast } from "sonner";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { CredentialStatusBadge, RunStatusBadge } from "@/components/status-badge";
import { TypeIcon } from "@/components/type-icon";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useOAuthStart } from "@/features/credentials/api";
import { useDashboard } from "@/features/dashboard/api";
import { Panel, StatCard } from "@/features/dashboard/stat-card";
import { errorMessage } from "@/lib/api";
import { formatDateTime, formatDuration, formatNumber, formatRelative, runDuration } from "@/lib/format";
import { useNow } from "@/lib/hooks";

function ListSkeleton() {
  return (
    <div className="space-y-2 p-4">
      {Array.from({ length: 3 }).map((_, i) => (
        <Skeleton key={i} className="h-8 w-full" />
      ))}
    </div>
  );
}

export default function DashboardPage() {
  const { t } = useTranslation();
  const now = useNow(5_000);
  const dash = useDashboard();
  const oauth = useOAuthStart();
  const [reconnecting, setReconnecting] = useState<string | null>(null);
  const d = dash.data;

  async function reconnect(id: string) {
    setReconnecting(id);
    try {
      await oauth.mutateAsync(id);
    } catch (e) {
      toast.error(t("credentials.oauth.startFailed"), { description: errorMessage(e) });
      setReconnecting(null);
    }
  }

  if (dash.isError) {
    return (
      <>
        <PageHeader title={t("dashboard.title")} />
        <EmptyState
          icon={Activity}
          title={t("common.loadFailed")}
          description={errorMessage(dash.error)}
          action={<Button variant="outline" onClick={() => dash.refetch()}>{t("common.retry")}</Button>}
        />
      </>
    );
  }

  const noTasks = d && d.totalTasks === 0;

  return (
    <>
      <PageHeader
        title={t("dashboard.title")}
        description={t("dashboard.description")}
        actions={
          <Button asChild>
            <Link to="/tasks/new">
              <Plus /> {t("tasks.new")}
            </Link>
          </Button>
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          icon={ListChecks}
          label={t("dashboard.stats.enabledTasks")}
          value={d ? `${d.enabledTasks}` : "—"}
          sub={d ? t("dashboard.stats.ofTotal", { total: d.totalTasks }) : undefined}
          loading={dash.isLoading}
        />
        <StatCard
          icon={Activity}
          tone={d?.runningTasks.length ? "blue" : "default"}
          label={t("dashboard.stats.running")}
          value={d?.runningTasks.length ?? "—"}
          sub={d ? t("dashboard.stats.runs24h", { count: d.totals.runs24h }) : undefined}
          loading={dash.isLoading}
        />
        <StatCard
          icon={Database}
          label={t("dashboard.stats.items")}
          value={d ? formatNumber(d.totals.items, true) : "—"}
          sub={d ? t("dashboard.stats.itemsSub") : undefined}
          loading={dash.isLoading}
        />
        <StatCard
          icon={XCircle}
          tone={d?.totals.failed24h ? "red" : "default"}
          label={t("dashboard.stats.failures24h")}
          value={d?.totals.failed24h ?? "—"}
          sub={d ? (d.totals.failed24h ? t("dashboard.stats.needsAttention") : t("dashboard.stats.allGood")) : undefined}
          loading={dash.isLoading}
        />
      </div>

      {noTasks && (
        <EmptyState
          className="mt-6"
          icon={ListChecks}
          title={t("dashboard.getStarted.title")}
          description={t("dashboard.getStarted.description")}
          action={
            <div className="flex flex-wrap justify-center gap-2">
              <Button asChild variant="outline">
                <Link to="/credentials">
                  <KeyRound /> {t("dashboard.getStarted.credentials")}
                </Link>
              </Button>
              <Button asChild>
                <Link to="/tasks/new">
                  <Plus /> {t("tasks.new")}
                </Link>
              </Button>
            </div>
          }
        />
      )}

      {d && d.reauthCredentials.length > 0 && (
        <Panel
          className="mt-6 border-amber-500/40"
          title={
            <span className="flex items-center gap-2">
              <AlertTriangle className="size-4 text-amber-500" /> {t("dashboard.reauth.title")}
            </span>
          }
        >
          <ul className="divide-y">
            {d.reauthCredentials.map((c) => (
              <li key={c.id} className="flex items-center gap-3 px-4 py-3">
                <TypeIcon type={c.type} boxed />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">{c.name}</div>
                  <CredentialStatusBadge status={c.status} className="mt-0.5" />
                </div>
                <Button size="sm" onClick={() => reconnect(c.id)} disabled={reconnecting === c.id}>
                  {reconnecting === c.id ? <Loader2 className="animate-spin" /> : <Link2 />}
                  {t("credentials.actions.reconnect")}
                </Button>
              </li>
            ))}
          </ul>
        </Panel>
      )}

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Panel
          title={t("dashboard.running.title")}
          action={
            <Button asChild variant="link" size="sm" className="h-auto p-0">
              <Link to="/runs?status=running">{t("common.viewAll")}</Link>
            </Button>
          }
        >
          {dash.isLoading ? (
            <ListSkeleton />
          ) : !d?.runningTasks.length ? (
            <EmptyState compact className="m-4" icon={CheckCircle2} title={t("dashboard.running.empty")} />
          ) : (
            <ul className="divide-y">
              {d.runningTasks.map((r) => (
                <li key={r.runId}>
                  <Link to={`/runs/${r.runId}`} className="flex items-center gap-3 px-4 py-3 hover:bg-accent/40">
                    <RunStatusBadge status="running" />
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">{r.taskName}</span>
                    <span className="text-xs text-muted-foreground tabular-nums" title={formatDateTime(r.startedAt)}>
                      {formatDuration(runDuration(r.startedAt, null, now))}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Panel>

        <Panel
          title={t("dashboard.nextRuns.title")}
          action={
            <Button asChild variant="link" size="sm" className="h-auto p-0">
              <Link to="/tasks">{t("nav.tasks")}</Link>
            </Button>
          }
        >
          {dash.isLoading ? (
            <ListSkeleton />
          ) : !d?.nextRuns.length ? (
            <EmptyState compact className="m-4" icon={CalendarClock} title={t("dashboard.nextRuns.empty")} />
          ) : (
            <ul className="divide-y">
              {d.nextRuns.map((r) => (
                <li key={r.taskId}>
                  <Link to={`/tasks/${r.taskId}`} className="flex items-center gap-3 px-4 py-3 hover:bg-accent/40">
                    <CalendarClock className="size-4 text-muted-foreground" />
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">{r.taskName}</span>
                    <span className="text-xs text-muted-foreground" title={formatDateTime(r.nextRunAt)}>
                      {formatRelative(r.nextRunAt, now)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Panel>

        <Panel
          className="lg:col-span-2"
          title={t("dashboard.failures.title")}
          action={
            <Button asChild variant="link" size="sm" className="h-auto p-0">
              <Link to="/runs?status=failed">{t("common.viewAll")}</Link>
            </Button>
          }
        >
          {dash.isLoading ? (
            <ListSkeleton />
          ) : !d?.recentFailures.length ? (
            <EmptyState compact className="m-4" icon={CheckCircle2} title={t("dashboard.failures.empty")} />
          ) : (
            <ul className="divide-y">
              {d.recentFailures.map((r) => (
                <li key={r.id}>
                  <Link to={`/runs/${r.id}`} className="flex flex-col gap-1 px-4 py-3 hover:bg-accent/40 sm:flex-row sm:items-center sm:gap-3">
                    <div className="flex items-center gap-3">
                      <RunStatusBadge status={r.status} />
                      <span className="truncate text-sm font-medium">{r.taskName}</span>
                    </div>
                    <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground" title={r.errorMessage}>
                      {r.errorMessage}
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground" title={formatDateTime(r.startedAt)}>
                      {formatRelative(r.finishedAt ?? r.startedAt, now)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </>
  );
}
