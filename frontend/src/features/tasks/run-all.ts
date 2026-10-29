import type { Task } from "@/lib/types";

export async function runEnabledTasks(
  tasks: Pick<Task, "id" | "enabled" | "running">[],
  run: (id: string) => Promise<unknown>,
) {
  const result = { started: 0, failed: 0, skipped: 0 };
  // Submit one at a time to avoid a request burst; runs execute in the backend queue.
  for (const task of tasks) {
    if (!task.enabled) continue;
    if (task.running) {
      result.skipped++;
      continue;
    }
    try {
      await run(task.id);
      result.started++;
    } catch (error) {
      if (error instanceof Error && "status" in error && error.status === 409) result.skipped++;
      else result.failed++;
    }
  }
  return result;
}
