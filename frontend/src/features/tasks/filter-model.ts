import type { FilterFieldSpec, FilterGroup, FilterOperator, FilterRule, FilterValue } from "@/lib/types";

export const isMultiOperator = (op: FilterOperator) => op === "in" || op === "notIn" || op === "containsAny" || op === "containsAll";

export const isUnaryOperator = (op: FilterOperator) => op === "exists" || op === "notExists";

/** Default value for a rule given its field type + operator. */
export function defaultRuleValue(spec: FilterFieldSpec | undefined, op: FilterOperator): FilterValue {
  if (isUnaryOperator(op)) return null;
  if (isMultiOperator(op)) return [];
  switch (spec?.type) {
    case "boolean":
      return true;
    case "number":
      return 0;
    case "enum":
      return spec.options?.[0]?.value ?? "";
    default:
      return "";
  }
}

/** Coerce an existing value when operator/field changes, keeping what we can. */
export function coerceValue(spec: FilterFieldSpec | undefined, op: FilterOperator, prev: FilterValue): FilterValue {
  if (isUnaryOperator(op)) return null;
  if (prev === null || prev === undefined) return defaultRuleValue(spec, op);
  const multi = isMultiOperator(op);
  if (multi) {
    if (Array.isArray(prev)) return prev;
    if (prev === "" || typeof prev === "boolean") return [];
    return [prev as string | number];
  }
  if (Array.isArray(prev)) return prev.length ? (spec?.type === "number" ? Number(prev[0]) : String(prev[0])) : defaultRuleValue(spec, op);
  if (spec?.type === "boolean") return typeof prev === "boolean" ? prev : true;
  if (spec?.type === "number") return typeof prev === "number" ? prev : Number(prev) || 0;
  if (typeof prev === "boolean" || typeof prev === "number") return defaultRuleValue(spec, op);
  return prev;
}

function valueError(spec: FilterFieldSpec, value: FilterValue): string | undefined {
  if (spec.type === "boolean") return typeof value === "boolean" ? undefined : "filters.errors.value";
  if (value === null || value === undefined || (typeof value === "string" && !value.trim())) return "filters.errors.value";
  switch (spec.type) {
    case "number": return (typeof value === "number" || typeof value === "string") && Number.isFinite(Number(value)) ? undefined : "filters.errors.number";
    case "datetime": return typeof value === "string" && Number.isFinite(Date.parse(value)) ? undefined : "filters.errors.date";
    case "enum": return spec.options?.some((o) => o.value === value) ? undefined : "filters.errors.value";
    default: return typeof value === "string" ? undefined : "filters.errors.value";
  }
}

function ruleError(rule: FilterRule, specs: FilterFieldSpec[]): string | undefined {
  const spec = specs.find((s) => s.key === rule.field);
  if (!spec) return "filters.errors.field";
  if (!spec.operators.includes(rule.operator)) return "filters.errors.operator";
  if (isUnaryOperator(rule.operator)) return;
  const values = isMultiOperator(rule.operator) ? (Array.isArray(rule.value) ? rule.value : []) : [rule.value];
  if (!values.length) return "filters.errors.valueList";
  for (const value of values) {
    const error = valueError(spec, value);
    if (error) return error;
  }
}

export function validateFilterGroup(group: FilterGroup, specs: FilterFieldSpec[], maxDepth: number): Record<string, string> {
  const errors: Record<string, string> = {};
  function visit(g: FilterGroup, path: string, depth: number) {
    if (depth > maxDepth) {
      errors[path] = "filters.errors.depth";
      return;
    }
    if (depth > 1 && !g.rules.length && !g.groups?.length) errors[path] = "filters.errors.emptyGroup";
    g.rules.forEach((rule, i) => {
      const error = ruleError(rule, specs);
      if (error) errors[`${path}.rules.${i}`] = error;
    });
    g.groups?.forEach((child, i) => visit(child, `${path}.groups.${i}`, depth + 1));
  }
  visit(group, "expression", 1);
  return errors;
}
