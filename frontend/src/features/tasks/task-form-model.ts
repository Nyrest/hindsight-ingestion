import { z } from "zod";
import type { KeyValueRow } from "@/components/key-value-editor";
import { browserTimeZone } from "@/lib/format";
import type { FilePolicy, FilterRule, SourceFilter, Task, TaskInput } from "@/lib/types";

export const RESERVED_METADATA_PREFIX = "_ingestion_";

const filePolicySchema = z.object({
  plainText: z.boolean(),
  documents: z.boolean(),
  images: z.boolean(),
  audios: z.boolean(),
});

/** Messages are i18n keys; translated at render time. */
export const taskFormSchema = z
  .object({
    name: z.string().trim().min(1, "validation.required").max(200, "validation.tooLong"),
    enabled: z.boolean(),
    sourceType: z.string().min(1, "validation.required"),
    sourceCredentialId: z.string().min(1, "validation.required"),
    sourceConfig: z.record(z.string(), z.unknown()),
    filterMode: z.enum(["simple", "advanced"]),
    filterRules: z.custom<FilterRule[]>((v) => Array.isArray(v)),
    advancedQuery: z.string(),
    destinationCredentialId: z.string().min(1, "validation.required"),
    destinationBankId: z.string().trim().min(1, "validation.required"),
    retainStrategy: z.string(),
    customTags: z.array(z.string()),
    metadataRows: z.array(z.object({ key: z.string(), value: z.string() })),
    filePolicyMode: z.enum(["global", "override"]),
    inlineMultimodalMode: z.enum(["global", "override"]),
    inlineMultimodalEnabled: z.boolean(),
    filePolicy: filePolicySchema,
    cronExpression: z.string().trim().min(1, "validation.required"),
    cronTimezone: z.string().min(1, "validation.required"),
  })
  .superRefine((v, ctx) => {
    metadataRowErrors(v.metadataRows).forEach((msg, i) => {
      if (msg) ctx.addIssue({ code: "custom", path: ["metadataRows", i, "key"], message: msg });
    });
    if (v.filterMode === "advanced" && !v.advancedQuery.trim()) {
      ctx.addIssue({ code: "custom", path: ["advancedQuery"], message: "validation.required" });
    }
  });

export type TaskFormValues = z.infer<typeof taskFormSchema>;

/** Per-row metadata key error (i18n key) or "" when valid. */
export function metadataRowErrors(rows: KeyValueRow[]): string[] {
  const seen = new Set<string>();
  return rows.map((r) => {
    const k = r.key.trim();
    if (!k) return r.value.trim() ? "metadata.errors.emptyKey" : "";
    if (k.startsWith(RESERVED_METADATA_PREFIX)) return "metadata.errors.reserved";
    if (seen.has(k)) return "metadata.errors.duplicate";
    seen.add(k);
    return "";
  });
}

const DEFAULT_POLICY: FilePolicy = { plainText: true, documents: true, images: false, audios: false };

export function emptyTaskForm(): TaskFormValues {
  return {
    name: "",
    enabled: true,
    sourceType: "",
    sourceCredentialId: "",
    sourceConfig: {},
    filterMode: "simple",
    filterRules: [],
    advancedQuery: "",
    destinationCredentialId: "",
    destinationBankId: "",
    retainStrategy: "",
    customTags: [],
    metadataRows: [],
    filePolicyMode: "global",
    inlineMultimodalMode: "global",
    inlineMultimodalEnabled: false,
    filePolicy: { ...DEFAULT_POLICY },
    cronExpression: "0 * * * *",
    cronTimezone: browserTimeZone(),
  };
}

export function taskToForm(t: Task): TaskFormValues {
  const filter: SourceFilter = t.sourceFilter ?? { mode: "simple", rules: [], advancedQuery: "" };
  return {
    name: t.name,
    enabled: t.enabled,
    sourceType: t.sourceType,
    sourceCredentialId: t.sourceCredentialId,
    sourceConfig: { ...(t.sourceConfig ?? {}) },
    filterMode: filter.mode === "advanced" ? "advanced" : "simple",
    filterRules: [...(filter.rules ?? [])],
    advancedQuery: filter.advancedQuery ?? "",
    destinationCredentialId: t.destinationCredentialId,
    destinationBankId: t.destinationBankId,
    retainStrategy: t.retainStrategy ?? "",
    customTags: [...(t.customTags ?? [])],
    metadataRows: Object.entries(t.customMetadata ?? {}).map(([key, value]) => ({ key, value: String(value) })),
    filePolicyMode: t.filePolicyMode === "override" ? "override" : "global",
    inlineMultimodalMode: t.inlineMultimodalMode === "override" ? "override" : "global",
    inlineMultimodalEnabled: t.inlineMultimodalEnabled ?? false,
    filePolicy: { ...DEFAULT_POLICY, ...(t.filePolicy ?? {}) },
    cronExpression: t.cronExpression,
    cronTimezone: t.cronTimezone || "UTC",
  };
}

export function formToInput(v: TaskFormValues, normalizedConfig: Record<string, unknown>) {
  const customMetadata: Record<string, string> = {};
  for (const r of v.metadataRows) {
    const k = r.key.trim();
    if (k) customMetadata[k] = r.value;
  }
  return {
    name: v.name.trim(),
    enabled: v.enabled,
    sourceType: v.sourceType,
    sourceCredentialId: v.sourceCredentialId,
    sourceConfig: normalizedConfig,
    sourceFilter: {
      mode: v.filterMode,
      rules: v.filterRules,
      advancedQuery: v.filterMode === "advanced" ? v.advancedQuery : "",
    },
    destinationCredentialId: v.destinationCredentialId,
    destinationBankId: v.destinationBankId.trim(),
    retainStrategy: v.retainStrategy,
    customTags: v.customTags,
    customMetadata,
    filePolicyMode: v.filePolicyMode,
    inlineMultimodalMode: v.inlineMultimodalMode,
    inlineMultimodalEnabled: v.inlineMultimodalEnabled,
    filePolicy: v.filePolicy,
    cronExpression: v.cronExpression.trim(),
    cronTimezone: v.cronTimezone,
  } satisfies TaskInput;
}

/** Maps a server validation `fields` key to a section id for scrolling. */
export function sectionForField(key: string): string {
  if (key === "name" || key === "enabled") return "general";
  if (key.startsWith("sourceFilter")) return "filters";
  if (key.startsWith("source")) return "source";
  if (key.startsWith("destination")) return "destination";
  if (key === "retainStrategy") return "retain";
  if (key.startsWith("filePolicy")) return "files";
  if (key.startsWith("inlineMultimodal")) return "inline";
  if (key.startsWith("customTags")) return "tags";
  if (key.startsWith("customMetadata")) return "metadata";
  if (key.startsWith("cron")) return "schedule";
  return "general";
}
