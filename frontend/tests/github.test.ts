import { expect, test } from "bun:test";
import { emptyTaskForm, formToInput, taskToForm } from "../src/features/tasks/task-form-model";
import { providerCopy } from "../src/lib/provider-copy";
import type { Task } from "../src/lib/types";
import en from "../src/locales/en.json";
import zhCN from "../src/locales/zh-CN.json";

test("GitHub task scope and custom ref survive save and reload", () => {
  const form = emptyTaskForm();
  form.sourceType = "github";
  form.sourceConfig = { repository: "owner/repo", ref: "feature/notes", folder: "docs", includeFiles: true, includeIssues: false, includePullRequests: true, respectGitignore: false };
  const input = formToInput(form, form.sourceConfig);
  const reloaded = taskToForm(input as Task);
  expect(reloaded.sourceType).toBe("github");
  expect(reloaded.sourceConfig).toEqual(form.sourceConfig);
  expect(reloaded.sourceConfig.includeIssues).toBe(false);
  expect(reloaded.sourceConfig.respectGitignore).toBe(false);
});

test("GitHub schema copy resolves translated keys", () => {
  for (const resource of [en, zhCN]) {
    const t = (key: string) => resource.providerCopy[key.replace("providerCopy.", "") as keyof typeof resource.providerCopy];
    expect(providerCopy("Apply .gitignore", t)).toBe(resource.providerCopy.githubIgnore);
    expect(providerCopy("Issues and comments", t)).toBe(resource.providerCopy.githubIssues);
    expect(providerCopy("Personal access token", t)).toBe(resource.providerCopy.githubPat);
  }
});
