import { expect, test } from "bun:test";
import { runEnabledTasks } from "../src/features/tasks/run-all";

test("Run all starts only enabled idle tasks, continues after failures and skips conflicts", async () => {
  const calls: string[] = [];
  const result = await runEnabledTasks([
    { id: "disabled", enabled: false, running: false },
    { id: "running", enabled: true, running: true },
    { id: "first", enabled: true, running: false },
    { id: "failed", enabled: true, running: false },
    { id: "conflict", enabled: true, running: false },
    { id: "last", enabled: true, running: false },
  ], async (id) => {
    calls.push(id);
    if (id === "failed") throw new Error("Network unavailable");
    if (id === "conflict") throw Object.assign(new Error("Already running"), { status: 409 });
  });
  expect(calls).toEqual(["first", "failed", "conflict", "last"]);
  expect(result).toEqual({ started: 2, failed: 1, skipped: 2 });
});

test("Run all does nothing when no enabled idle tasks exist", async () => {
  const result = await runEnabledTasks([{ id: "disabled", enabled: false, running: false }], async () => {
    throw new Error("Must not run");
  });
  expect(result).toEqual({ started: 0, failed: 0, skipped: 0 });
});
