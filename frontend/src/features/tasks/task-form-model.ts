import { z } from "zod";
import { defaultObservationScope, observationScopeSchema } from "@/features/settings/config-model";
import type { KeyValueRow } from "@/components/key-value-editor";
import type { FilePolicy, FilterGroup, SourceSpec, Task, TaskInput } from "@/lib/types";
import { emptyPipeline, firstInvalidStep, normalizePipeline, type Pipeline } from "./post-processing/model";

export const RESERVED_METADATA_PREFIX = "_ingestion_";

const filePolicySchema = z.object({
  plainText: z.boolean(),
  documents: z.boolean(),
  images: z.boolean(),
  audios: z.boolean(),
});

/** Messages are i18n keys; translated at render time. */
export const taskFormSchema = (sources: SourceSpec[]) => z
  .object({
    name: z.string().trim().min(1, "validation.required").max(200, "validation.tooLong"),
    enabled: z.boolean(),
    sourceType: z.string().min(1, "validation.required"),
    sourceCredentialId: z.string(),
    sourceConfig: z.record(z.string(), z.unknown()),
    filterMode: z.enum(["simple", "advanced"]),
    filterGroup: z.custom<FilterGroup>(),
    advancedQuery: z.string(),
    destinationCredentialId: z.string().min(1, "validation.required"),
    destinationBankId: z.string().trim().min(1, "validation.required"),
    retainStrategy: z.string(),
    observationScopeMode: z.enum(["global", "override"]),
    observationScope: observationScopeSchema,
    customTags: z.array(z.string()),
    metadataRows: z.array(z.object({ key: z.string(), value: z.string() })),
    filePolicyMode: z.enum(["global", "override"]),
    inlineMultimodalMode: z.enum(["global", "override"]),
    inlineMultimodalEnabled: z.boolean(),
    filePolicy: filePolicySchema,
    postProcessing: z.custom<Pipeline>(),
    cronExpression: z.string().trim(),
  })
  .superRefine((v, ctx) => {
    if (sources.find((s) => s.type === v.sourceType)?.credentialTypes.length !== 0 && !v.sourceCredentialId) {
      ctx.addIssue({ code: "custom", path: ["sourceCredentialId"], message: "validation.required" });
    }
    metadataRowErrors(v.metadataRows).forEach((msg, i) => {
      if (msg) ctx.addIssue({ code: "custom", path: ["metadataRows", i, "key"], message: msg });
    });
    if (v.filterMode === "advanced" && !v.advancedQuery.trim()) {
      ctx.addIssue({ code: "custom", path: ["advancedQuery"], message: "validation.required" });
    }
    const pp = v.postProcessing;
    if (firstInvalidStep(pp) >= 0 || (pp.enabled && pp.mode === "code" && !pp.code.trim())) {
      ctx.addIssue({ code: "custom", path: ["postProcessing"], message: "postProcessing.errors.invalid" });
    }
  });

export type TaskFormValues = z.infer<ReturnType<typeof taskFormSchema>>;

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
    filterGroup: { combinator: "and", rules: [], groups: [] },
    advancedQuery: "",
    destinationCredentialId: "",
    destinationBankId: "",
    retainStrategy: "",
    observationScopeMode: "global",
    observationScope: defaultObservationScope,
    customTags: [],
    metadataRows: [],
    filePolicyMode: "global",
    inlineMultimodalMode: "global",
    inlineMultimodalEnabled: false,
    filePolicy: { ...DEFAULT_POLICY },
    postProcessing: emptyPipeline(),
    cronExpression: "",
  };
}

export function taskToForm(t: Task): TaskFormValues {
  const filter = t.sourceFilter;
  return {
    name: t.name,
    enabled: t.enabled,
    sourceType: t.sourceType,
    sourceCredentialId: t.sourceCredentialId,
    sourceConfig: { ...(t.sourceConfig ?? {}) },
    filterMode: filter.mode,
    filterGroup: structuredClone(filter.expression),
    advancedQuery: filter.advancedQuery,
    destinationCredentialId: t.destinationCredentialId,
    destinationBankId: t.destinationBankId,
    retainStrategy: t.retainStrategy ?? "",
    observationScopeMode: t.observationScopeMode ?? "global",
    observationScope: t.observationScope ?? defaultObservationScope,
    customTags: [...(t.customTags ?? [])],
    metadataRows: Object.entries(t.customMetadata ?? {}).map(([key, value]) => ({ key, value: String(value) })),
    filePolicyMode: t.filePolicyMode === "override" ? "override" : "global",
    inlineMultimodalMode: t.inlineMultimodalMode === "override" ? "override" : "global",
    inlineMultimodalEnabled: t.inlineMultimodalEnabled ?? false,
    filePolicy: { ...DEFAULT_POLICY, ...(t.filePolicy ?? {}) },
    postProcessing: normalizePipeline(t.postProcessing),
    cronExpression: t.cronExpression,
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
      expression: v.filterGroup,
      advancedQuery: v.filterMode === "advanced" ? v.advancedQuery : "",
    },
    destinationCredentialId: v.destinationCredentialId,
    destinationBankId: v.destinationBankId.trim(),
    retainStrategy: v.retainStrategy,
    observationScopeMode: v.observationScopeMode,
    observationScope: v.observationScope,
    customTags: v.customTags,
    customMetadata,
    filePolicyMode: v.filePolicyMode,
    inlineMultimodalMode: v.inlineMultimodalMode,
    inlineMultimodalEnabled: v.inlineMultimodalEnabled,
    filePolicy: v.filePolicy,
    postProcessing: v.postProcessing,
    cronExpression: v.cronExpression.trim(),
  } satisfies TaskInput;
}

/** Maps a server validation `fields` key to a section id for scrolling. */
export function sectionForField(key: string): string {
  if (key === "name" || key === "enabled") return "general";
  if (key.startsWith("sourceFilter")) return "filters";
  if (key.startsWith("source")) return "source";
  if (key.startsWith("destination")) return "destination";
  if (key.startsWith("observationScope")) return "observations";
  if (key === "retainStrategy") return "retain";
  if (key.startsWith("filePolicy")) return "files";
  if (key.startsWith("inlineMultimodal")) return "inline";
  if (key.startsWith("customTags")) return "tags";
  if (key.startsWith("customMetadata")) return "metadata";
  if (key.startsWith("postProcessing")) return "postprocess";
  if (key.startsWith("cron")) return "schedule";
  return "general";
}
