import {
  History,
  ListChecks,
  Loader2,
  MoreHorizontal,
  Pencil,
  Play,
  Plus,
  Power,
  RefreshCcw,
  RotateCcw,
  Square,
  Trash2,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { RunStatusBadge } from "@/components/status-badge";
import { TypeIcon } from "@/components/type-icon";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useCredentials } from "@/features/credentials/api";
import {
  useCancelTask,
  useDeleteTask,
  useTasks,
  useToggleTask,
  useTriggerTask,
  type TriggerKind,
} from "@/features/tasks/api";
import { errorMessage, isApiError } from "@/lib/api";
import { typeName, useConnectors } from "@/lib/connectors";
import { describeCron, formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import type { Task } from "@/lib/types";
import { ToneBadge } from "@/components/status-badge";

export default function TasksPage() {
  const { t } = useTranslation();
  const now = useNow();
  const navigate = useNavigate();
  const tasks = useTasks();
  const creds = useCredentials();
  const connectors = useConnectors();
  const toggle = useToggleTask();
  const trigger = useTriggerTask();
  const cancel = useCancelTask();
  const del = useDeleteTask();
  const [toDelete, setToDelete] = useState<Task | null>(null);
  const [toReingest, setToReingest] = useState<Task | null>(null);
  const [pendingTrigger, setPendingTrigger] = useState<string | null>(null);

  const credName = (id: string) => creds.data?.find((c) => c.id === id)?.name ?? "—";

  function fire(task: Task, kind: TriggerKind) {
    setPendingTrigger(`${kind}-${task.id}`);
    return trigger.mutateAsync({ id: task.id, kind, name: task.name }).finally(() => setPendingTrigger(null));
  }

  function onToggle(task: Task, enabled: boolean) {
    toggle.mutate(
      { id: task.id, enabled },
      {
        onSuccess: () => toast.success(enabled ? t("tasks.toast.enabled") : t("tasks.toast.disabled"), { description: task.name }),
        onError: (e) => toast.error(t("tasks.toast.updateFailed"), { description: errorMessage(e) }),
      },
    );
  }

  async function onDelete() {
    if (!toDelete) return;
    try {
      await del.mutateAsync(toDelete.id);
      toast.success(t("tasks.toast.deleted"), { description: toDelete.name });
    } catch (e) {
      toast.error(isApiError(e) && e.status === 409 ? t("tasks.toast.deleteRunning") : t("tasks.toast.deleteFailed"), {
        description: errorMessage(e),
      });
      throw e;
    }
  }

  function statusCell(task: Task) {
    if (task.running) return <RunStatusBadge status="running" />;
    if (!task.enabled) return <ToneBadge tone="gray">{t("tasks.status.disabled")}</ToneBadge>;
    if (task.reconcileRequired) return <ToneBadge tone="amber">{t("tasks.status.reconcilePending")}</ToneBadge>;
    return <ToneBadge tone="green">{t("tasks.status.idle")}</ToneBadge>;
  }

  function scheduleCell(task: Task) {
    const desc = describeCron(task.cronExpression);
    return (
      <div className="min-w-0">
        <div className="font-mono text-xs">{task.cronExpression}</div>
        <div className="truncate text-xs text-muted-foreground" title={desc ?? undefined}>
          {desc ?? ""} {task.cronTimezone && task.cronTimezone !== "UTC" ? `(${task.cronTimezone})` : task.cronTimezone ? "(UTC)" : ""}
        </div>
      </div>
    );
  }

  function lastRunCell(task: Task) {
    if (!task.lastRun) return <span className="text-xs text-muted-foreground">{t("tasks.neverRun")}</span>;
    return (
      <Link to={`/runs/${task.lastRun.id}`} className="flex flex-col items-start gap-0.5" onClick={(e) => e.stopPropagation()}>
        <RunStatusBadge status={task.lastRun.status} />
        <span className="text-xs text-muted-foreground" title={formatDateTime(task.lastRun.startedAt)}>
          {formatRelative(task.lastRun.finishedAt ?? task.lastRun.startedAt, now)}
        </span>
      </Link>
    );
  }

  function nextRunCell(task: Task) {
    if (!task.enabled || !task.nextRunAt) return <span className="text-xs text-muted-foreground">—</span>;
    return (
      <span className="text-sm" title={formatDateTime(task.nextRunAt)}>
        {formatRelative(task.nextRunAt, now)}
      </span>
    );
  }

  function actions(task: Task) {
    const busy = pendingTrigger?.endsWith(task.id);
    return (
      <div className="flex items-center justify-end gap-1" onClick={(e) => e.stopPropagation()}>
        {task.running ? (
          <Button size="sm" variant="ghost" onClick={() => cancel.mutate(task.id)} disabled={cancel.isPending}>
            <Square /> <span className="hidden xl:inline">{t("tasks.actions.cancel")}</span>
          </Button>
        ) : (
          <Button size="sm" variant="ghost" onClick={() => fire(task, "run")} disabled={!!busy}>
            {pendingTrigger === `run-${task.id}` ? <Loader2 className="animate-spin" /> : <Play />}
            <span className="hidden xl:inline">{t("tasks.actions.runNow")}</span>
          </Button>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="icon-sm" variant="ghost" aria-label={t("common.moreActions")}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-52">
            <DropdownMenuItem onSelect={() => fire(task, "run")} disabled={task.running}>
              <Play /> {t("tasks.actions.runNow")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => fire(task, "fullReconcile")} disabled={task.running}>
              <RefreshCcw /> {t("tasks.actions.fullReconcile")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setToReingest(task)} disabled={task.running}>
              <RotateCcw /> {t("tasks.actions.fullReingest")}
            </DropdownMenuItem>
            {task.running && (
              <DropdownMenuItem onSelect={() => cancel.mutate(task.id)}>
                <Square /> {t("tasks.actions.cancel")}
              </DropdownMenuItem>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => onToggle(task, !task.enabled)}>
              <Power /> {task.enabled ? t("tasks.actions.disable") : t("tasks.actions.enable")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => navigate(`/tasks/${task.id}`)}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => navigate(`/runs?taskId=${encodeURIComponent(task.id)}`)}>
              <History /> {t("tasks.actions.viewRuns")}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => setToDelete(task)}>
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    );
  }

  const list = tasks.data ?? [];

  return (
    <>
      <PageHeader
        title={t("tasks.title")}
        description={t("tasks.description")}
        actions={
          <Button asChild>
            <Link to="/tasks/new">
              <Plus /> {t("tasks.new")}
            </Link>
          </Button>
        }
      />

      {tasks.isLoading ? (
        <Card className="gap-0 p-0">
          {Array.from({ length: 5 }).map((_, i) => (
            <div key={i} className="flex items-center gap-4 border-b p-4 last:border-0">
              <Skeleton className="h-5 w-9 rounded-full" />
              <Skeleton className="h-4 w-48" />
              <Skeleton className="ml-auto h-5 w-24" />
            </div>
          ))}
        </Card>
      ) : tasks.isError ? (
        <EmptyState
          icon={ListChecks}
          title={t("common.loadFailed")}
          description={errorMessage(tasks.error)}
          action={<Button variant="outline" onClick={() => tasks.refetch()}>{t("common.retry")}</Button>}
        />
      ) : list.length === 0 ? (
        <EmptyState
          icon={ListChecks}
          title={t("tasks.empty.title")}
          description={t("tasks.empty.description")}
          action={
            <Button asChild>
              <Link to="/tasks/new">
                <Plus /> {t("tasks.new")}
              </Link>
            </Button>
          }
        />
      ) : (
        <>
          <Card className="hidden gap-0 overflow-hidden p-0 lg:block">
            <Table>
              <TableHeader>
                <TableRow className="bg-muted/40 hover:bg-muted/40">
                  <TableHead className="w-14 pl-4">{t("tasks.columns.enabled")}</TableHead>
                  <TableHead>{t("tasks.columns.name")}</TableHead>
                  <TableHead>{t("tasks.columns.source")}</TableHead>
                  <TableHead>{t("tasks.columns.destination")}</TableHead>
                  <TableHead>{t("tasks.columns.schedule")}</TableHead>
                  <TableHead>{t("tasks.columns.lastRun")}</TableHead>
                  <TableHead>{t("tasks.columns.nextRun")}</TableHead>
                  <TableHead>{t("tasks.columns.status")}</TableHead>
                  <TableHead className="pr-4 text-right">
                    <span className="sr-only">{t("common.actions")}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((task) => (
                  <TableRow key={task.id} className="cursor-pointer" onClick={() => navigate(`/tasks/${task.id}`)}>
                    <TableCell className="pl-4" onClick={(e) => e.stopPropagation()}>
                      <Switch
                        checked={task.enabled}
                        onCheckedChange={(c) => onToggle(task, c)}
                        aria-label={task.enabled ? t("tasks.actions.disable") : t("tasks.actions.enable")}
                      />
                    </TableCell>
                    <TableCell className="max-w-[14rem]">
                      <div className="truncate font-medium">{task.name}</div>
                      <div className="text-xs text-muted-foreground">{t("tasks.itemCount", { count: task.itemCount })}</div>
                    </TableCell>
                    <TableCell className="max-w-[12rem]">
                      <div className="flex items-center gap-2">
                        <TypeIcon type={task.sourceType} className="text-muted-foreground" />
                        <div className="min-w-0">
                          <div className="truncate text-sm">{typeName(connectors.data, task.sourceType)}</div>
                          <div className="truncate text-xs text-muted-foreground">{credName(task.sourceCredentialId)}</div>
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className="max-w-[12rem]">
                      <div className="truncate text-sm">{credName(task.destinationCredentialId)}</div>
                      <div className="truncate font-mono text-xs text-muted-foreground">{task.destinationBankId}</div>
                    </TableCell>
                    <TableCell className="max-w-[13rem]">{scheduleCell(task)}</TableCell>
                    <TableCell>{lastRunCell(task)}</TableCell>
                    <TableCell className="whitespace-nowrap">{nextRunCell(task)}</TableCell>
                    <TableCell>{statusCell(task)}</TableCell>
                    <TableCell className="pr-4">{actions(task)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>

          <div className="grid gap-3 lg:hidden">
            {list.map((task) => (
              <Card key={task.id} className="gap-3 p-4">
                <div className="flex items-start gap-3">
                  <TypeIcon type={task.sourceType} boxed />
                  <Link to={`/tasks/${task.id}`} className="min-w-0 flex-1">
                    <div className="truncate font-medium">{task.name}</div>
                    <div className="truncate text-xs text-muted-foreground">
                      {credName(task.sourceCredentialId)} → {credName(task.destinationCredentialId)} /{" "}
                      <span className="font-mono">{task.destinationBankId}</span>
                    </div>
                  </Link>
                  <Switch
                    checked={task.enabled}
                    onCheckedChange={(c) => onToggle(task, c)}
                    aria-label={task.enabled ? t("tasks.actions.disable") : t("tasks.actions.enable")}
                  />
                </div>
                <div className="grid grid-cols-2 gap-3 text-sm">
                  <div>
                    <div className="mb-0.5 text-xs text-muted-foreground">{t("tasks.columns.schedule")}</div>
                    {scheduleCell(task)}
                  </div>
                  <div>
                    <div className="mb-0.5 text-xs text-muted-foreground">{t("tasks.columns.nextRun")}</div>
                    {nextRunCell(task)}
                  </div>
                  <div>
                    <div className="mb-0.5 text-xs text-muted-foreground">{t("tasks.columns.lastRun")}</div>
                    {lastRunCell(task)}
                  </div>
                  <div>
                    <div className="mb-0.5 text-xs text-muted-foreground">{t("tasks.columns.status")}</div>
                    {statusCell(task)}
                  </div>
                </div>
                {actions(task)}
              </Card>
            ))}
          </div>
        </>
      )}

      <ConfirmDialog
        open={!!toDelete}
        onOpenChange={(o) => !o && setToDelete(null)}
        title={t("tasks.delete.title", { name: toDelete?.name ?? "" })}
        description={t("tasks.delete.description")}
        confirmLabel={t("common.delete")}
        destructive
        onConfirm={onDelete}
      />
      <ConfirmDialog
        open={!!toReingest}
        onOpenChange={(o) => !o && setToReingest(null)}
        title={t("tasks.reingest.title", { name: toReingest?.name ?? "" })}
        description={t("tasks.reingest.description")}
        confirmLabel={t("tasks.actions.fullReingest")}
        onConfirm={() => (toReingest ? fire(toReingest, "fullReingest").catch(() => undefined) : undefined)}
      />
    </>
  );
}
