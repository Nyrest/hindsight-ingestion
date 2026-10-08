import { z } from "zod";
import type { Task } from "@/lib/types";

export const defaultProxy = {
  type: "default" as const,
  address: "",
  username: "",
  password: "",
};
export const defaultObservationScope = {
  rule: "combined" as const,
  scopes: [],
};

export const proxySchema = z
  .object({
    type: z.enum(["default", "none", "http", "https", "socks5"]),
    address: z.string(),
    username: z.string(),
    password: z.string(),
  })
  .superRefine((proxy, ctx) => {
    if (["default", "none"].includes(proxy.type)) return;
    const address = proxy.address.trim();
    const match = /^(\[[^\]\s]+\]|[^:/?#@\s]+):(\d+)$/.exec(address);
    if (!match || Number(match[2]) < 1 || Number(match[2]) > 65535) {
      ctx.addIssue({
        code: "custom",
        path: ["address"],
        message: "proxy.invalidAddress",
      });
    }
  });

export const observationScopeSchema = z
  .object({
    rule: z.enum([
      "combined",
      "shared",
      "per_tag",
      "all_combinations",
      "custom",
    ]),
    scopes: z.array(
      z.array(
        z.object({
          kind: z.enum(["literal", "dynamic"]),
          value: z.string().trim().min(1, "validation.required"),
        }),
      ),
    ),
  })
  .superRefine((scope, ctx) => {
    if (
      scope.rule === "custom" &&
      (!scope.scopes.length || scope.scopes.some((group) => !group.length))
    ) {
      ctx.addIssue({
        code: "custom",
        path: ["scopes"],
        message: "observationScope.emptyGroup",
      });
    }
  });

export function sourceGroupTags(
  sourceType: string,
  config: Record<string, unknown>,
): string[] {
  const keys: Record<string, [string, string]> = {
    notion: ["dataSourceId", "notion_data_source_id"],
    siyuan: ["notebookId", "siyuan_notebook_id"],
    s3: ["bucket", "s3_bucket"],
    google_drive: ["driveId", "google_drive_id"],
    onedrive: ["driveId", "onedrive_drive_id"],
  };
  const key = keys[sourceType];
  return key && typeof config[key[0]] === "string" && config[key[0]]
    ? [`${key[1]}:${config[key[0]]}`]
    : [];
}

export function taskTagSuggestions(task: Task): string[] {
  return [
    "ingestion",
    `source:${task.sourceType}`,
    `ingestion_task:${task.id}`,
    ...task.customTags,
    ...sourceGroupTags(task.sourceType, task.sourceConfig),
  ];
}
