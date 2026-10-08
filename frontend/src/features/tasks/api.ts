import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { api, errorMessage, isApiError } from "@/lib/api";
import { POLL_ACTIVE_MS, POLL_IDLE_MS, qk } from "@/lib/query";
import type { RunTriggered, Task, TaskInput, TaskPatch } from "@/lib/types";

export function useTasks() {
  return useQuery({
    queryKey: qk.tasks,
    queryFn: api.listTasks,
    refetchInterval: (q) => ((q.state.data ?? []).some((t) => t.running) ? POLL_ACTIVE_MS : POLL_IDLE_MS),
  });
}

export function useTask(id: string | undefined) {
  return useQuery({
    queryKey: qk.task(id ?? ""),
    queryFn: () => api.getTask(id!),
    enabled: !!id,
    refetchInterval: (q) => (q.state.data?.running ? POLL_ACTIVE_MS : POLL_IDLE_MS),
  });
}

export function useTaskRuns(id: string | undefined, limit = 10, offset = 0, active = false) {
  return useQuery({
    queryKey: qk.taskRuns(id ?? "", limit, offset),
    queryFn: () => api.taskRuns(id!, limit, offset),
    enabled: !!id,
    refetchInterval: active ? POLL_ACTIVE_MS : POLL_IDLE_MS,
  });
}

function useInvalidateTasks() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: qk.tasks });
    void qc.invalidateQueries({ queryKey: qk.runs });
    void qc.invalidateQueries({ queryKey: qk.dashboard });
    void qc.invalidateQueries({ queryKey: qk.credentials });
  };
}

export function useCreateTask() {
  const invalidate = useInvalidateTasks();
  return useMutation({ mutationFn: (body: TaskInput) => api.createTask(body), onSuccess: invalidate });
}

export function useUpdateTask() {
  const qc = useQueryClient();
  const invalidate = useInvalidateTasks();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: TaskPatch }) => api.updateTask(id, body),
    onSuccess: (task) => {
      qc.setQueryData(qk.task(task.id), task);
      invalidate();
    },
  });
}

/** Optimistic enable/disable toggle from the task list. */
export function useToggleTask() {
  const qc = useQueryClient();
  const invalidate = useInvalidateTasks();
  return useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => api.updateTask(id, { enabled }),
    onMutate: async ({ id, enabled }) => {
      await qc.cancelQueries({ queryKey: qk.tasks });
      const prev = qc.getQueryData<Task[]>(qk.tasks);
      qc.setQueryData<Task[]>(qk.tasks, (old) => old?.map((t) => (t.id === id ? { ...t, enabled } : t)));
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(qk.tasks, ctx.prev);
    },
    onSettled: invalidate,
  });
}

export function useDeleteTask() {
  const invalidate = useInvalidateTasks();
  return useMutation({ mutationFn: ({ id, deleteDocuments }: { id: string; deleteDocuments: boolean }) => api.deleteTask(id, deleteDocuments), onSettled: invalidate });
}

export function useDeleteDocuments() {
  const invalidate = useInvalidateTasks();
  return useMutation({ mutationFn: api.deleteTaskDocuments, onSettled: invalidate });
}

export type TriggerKind = "run" | "fullReconcile" | "fullReingest";

/** Trigger a run (run now / full reconcile / full re-ingest) with standard toasts. */
export function useTriggerTask() {
  const { t } = useTranslation();
  const invalidate = useInvalidateTasks();
  return useMutation<RunTriggered, Error, { id: string; kind: TriggerKind; name?: string }>({
    mutationFn: ({ id, kind }) =>
      kind === "run" ? api.runTask(id) : kind === "fullReconcile" ? api.fullReconcileTask(id) : api.fullReingestTask(id),
    onSuccess: (_res, { kind, name }) => {
      toast.success(t(`tasks.trigger.${kind}.success`), { description: name });
    },
    onError: (e, { name }) => {
      if (isApiError(e) && e.status === 409) toast.warning(t("tasks.trigger.alreadyRunning"), { description: name });
      else toast.error(t("tasks.trigger.failed"), { description: errorMessage(e) });
    },
    onSettled: invalidate,
  });
}

export function useCancelTask() {
  const { t } = useTranslation();
  const invalidate = useInvalidateTasks();
  return useMutation({
    mutationFn: (id: string) => api.cancelTask(id),
    onSuccess: () => toast.success(t("tasks.cancel.success")),
    onError: (e) => {
      if (isApiError(e) && e.status === 409) toast.info(t("tasks.cancel.notRunning"));
      else toast.error(t("tasks.cancel.failed"), { description: errorMessage(e) });
    },
    onSettled: invalidate,
  });
}

export function useValidateCron(expr: string, tz: string) {
  return useQuery({
    queryKey: qk.cron(expr, tz),
    queryFn: () => api.validateCron(expr, tz),
    enabled: !!expr.trim(),
    staleTime: 60_000,
    retry: false,
  });
}
