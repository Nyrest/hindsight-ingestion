import type { components } from "@/lib/openapi";

export type Pipeline = components["schemas"]["Pipeline"];
export type PipelineStep = components["schemas"]["PipelineStep"];
export type StepType = PipelineStep["type"];
export type PostProcessTestResult = components["schemas"]["postProcessTestDTO"];

/** Step types in menu order. */
export const STEP_TYPES: StepType[] = [
  "replace",
  "trim",
  "collapse_whitespace",
  "strip_html",
  "remove_lines",
  "prepend",
  "append",
  "truncate",
  "case",
  "skip",
  "http",
];

/** Mirrors postprocess.ProcessDocumentation in the backend. */
export const PROCESS_DOCUMENTATION = `/**
 * Transforms one document before it is retained to Hindsight.
 * Return a string or binary data to retain, or null to skip the document.
 *
 * @param {{
 *   task: {
 *     id: string,
 *     name: string,
 *     sourceType: string,
 *     bankId: string
 *   },
 *   run: {
 *     id?: string,
 *     mode?: string,
 *     dryRun: boolean,
 *     full: boolean,
 *     generation: number
 *   },
 *   item: {
 *     id: string,
 *     name: string,
 *     path: string,
 *     kind: string,
 *     revision: string,
 *     modifiedAt: string,
 *     mimeType: string,
 *     size: number,
 *     tags: string[],
 *     metadata: Record<string, string>,
 *     attributes: Record<string, unknown>
 *   },
 *   document: {
 *     contentType: "text" | "binary",
 *     fileName: string,
 *     mimeType: string,
 *     context: string,
 *     size: number
 *   }
 * }} context Task, run, source item and document information.
 * @param {string | ArrayBuffer} input Document text, or binary file data.
 * @returns {Promise<
 *   string | ArrayBuffer | ArrayBufferView | null | undefined | {
 *     content: string | ArrayBuffer | ArrayBufferView | null,
 *     fileName?: string,
 *     mimeType?: string,
 *     context?: string
 *   }
 * >} Content to retain, optional file metadata and context, or null/undefined to skip.
 */
`;

/** Mirrors postprocess.DefaultCode in the backend. */
export const DEFAULT_CODE = PROCESS_DOCUMENTATION + `async function process(context, input) {
  return input;
}
`;

export function emptyPipeline(): Pipeline {
  return { enabled: false, mode: "ui", steps: [], code: "" };
}

export function normalizePipeline(p: Pipeline | undefined): Pipeline {
  if (!p) return emptyPipeline();
  return { enabled: !!p.enabled, mode: p.mode === "code" ? "code" : "ui", steps: (p.steps ?? []).map((s) => ({ ...s })), code: p.code ?? "" };
}

let seq = 0;
function stepId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `step-${Date.now()}-${++seq}`;
}

export function newStep(type: StepType): PipelineStep {
  const base: PipelineStep = { id: stepId(), type, enabled: true };
  switch (type) {
    case "replace":
      return { ...base, pattern: "", replacement: "" };
    case "trim":
      return { ...base, side: "both" };
    case "remove_lines":
    case "skip":
      return { ...base, pattern: "", regex: true };
    case "prepend":
    case "append":
      return { ...base, text: "", separator: "\n\n" };
    case "truncate":
      return { ...base, maxLength: 10000, suffix: "…" };
    case "case":
      return { ...base, case: "lower" };
    case "http":
      return { ...base, method: "POST", url: "", bodyType: "json", body: '{\n  "text": "{{input}}"\n}', responseMode: "json", responsePath: "" };
    default:
      return base;
  }
}

const IDENTITY_KEYS = new Set(["id", "type", "enabled"]);

/** Keeps only the fields meaningful for the step type (omitting empty values). */
export function stepOptions(step: PipelineStep): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(step).sort()) {
    if (IDENTITY_KEYS.has(key)) continue;
    const value = (step as Record<string, unknown>)[key];
    if (value === undefined || value === "" || value === false || value === 0) continue;
    if (Array.isArray(value) && value.length === 0) continue;
    out[key] = value;
  }
  return out;
}

function functionName(type: StepType): string {
  return type.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase());
}

/** Compiles UI steps to code mode JavaScript, like postprocess.Generate. */
export function generateCode(steps: PipelineStep[]): string {
  const lines = ["// Generated from post-processing steps.", PROCESS_DOCUMENTATION + "async function process(context, input) {"];
  for (const step of steps) {
    const options = JSON.stringify(stepOptions(step)).replaceAll("\u2028", "\\u2028").replaceAll("\u2029", "\\u2029");
    lines.push(`  ${step.enabled ? "" : "// "}input = await steps.${functionName(step.type)}(context, input, ${options});`);
  }
  lines.push("  return input;", "}", "");
  return lines.join("\n");
}

/** Per-step client validation; returns field → i18n key. */
export function stepErrors(step: PipelineStep): Record<string, string> {
  const errs: Record<string, string> = {};
  if ((step.type === "replace" || step.type === "remove_lines" || step.type === "skip") && !step.pattern) errs.pattern = "validation.required";
  if (step.type === "truncate" && !(Number(step.maxLength) > 0)) errs.maxLength = "postProcessing.errors.positive";
  if (step.type === "http" && !step.url?.trim()) errs.url = "validation.required";
  if (step.regex && step.pattern && (step.type === "replace" || step.type === "remove_lines" || step.type === "skip")) {
    try {
      new RegExp(step.pattern);
    } catch {
      errs.pattern = "postProcessing.errors.regex";
    }
  }
  return errs;
}

/** Index of the first invalid step, or -1. Steps are validated in every mode, like on the server. */
export function firstInvalidStep(p: Pipeline): number {
  return p.steps.findIndex((s) => Object.keys(stepErrors(s)).length > 0);
}

/** The code that will run, or "" when disabled. */
export function effectiveCode(p: Pipeline): string {
  if (!p.enabled) return "";
  return p.mode === "code" ? p.code : generateCode(p.steps);
}
