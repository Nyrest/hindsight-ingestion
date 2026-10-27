import { validateConfig } from "@/components/schema-field";
import type { FieldSpec } from "@/lib/types";

export function isValidNotionDepth(depth: unknown): boolean {
  if (depth === null || depth === undefined || depth === "") return true;
  const numeric = Number(depth);
  return Number.isInteger(numeric) && numeric >= 0 && numeric <= 2147483647;
}

export function notionSourceFields(fields: FieldSpec[], config: Record<string, unknown>): FieldSpec[] {
  const pagesMode = config.mode === "pages";
  return fields.filter((field) => field.key !== (pagesMode ? "dataSourceId" : "pageIds"))
    .map((field) => field.key === (pagesMode ? "pageIds" : "dataSourceId") ? { ...field, required: true } : field);
}

export function validateNotionConfig(
  fields: FieldSpec[],
  config: Record<string, unknown>,
  t: (key: string) => string,
): Record<string, string> {
  const errors = validateConfig(notionSourceFields(fields, config), config, t);
  if (!isValidNotionDepth(config.maxDepth)) {
    errors.maxDepth = t("notion.depthError");
  }
  if (config.mode && config.mode !== "database" && config.mode !== "pages") errors.mode = t("validation.required");
  return errors;
}
