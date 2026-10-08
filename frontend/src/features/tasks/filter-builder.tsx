import { Filter, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { ChipsInput } from "@/components/chips-input";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { isoToLocalInput, localInputToIso } from "@/lib/format";
import { providerCopy } from "@/lib/provider-copy";
import type { FilterFieldSpec, FilterOperator, FilterRule, FilterValue } from "@/lib/types";

export const isMultiOperator = (op: FilterOperator) => op === "in" || op === "notIn";

/** Default value for a rule given its field type + operator. */
export function defaultRuleValue(spec: FilterFieldSpec | undefined, op: FilterOperator): FilterValue {
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
function coerceValue(spec: FilterFieldSpec | undefined, op: FilterOperator, prev: FilterValue): FilterValue {
  const multi = isMultiOperator(op);
  if (multi) {
    if (Array.isArray(prev)) return prev;
    if (prev === "" || prev === undefined || prev === null || typeof prev === "boolean") return [];
    return [prev as string | number];
  }
  if (Array.isArray(prev)) return prev.length ? (spec?.type === "number" ? Number(prev[0]) : String(prev[0])) : defaultRuleValue(spec, op);
  if (spec?.type === "boolean") return typeof prev === "boolean" ? prev : true;
  if (spec?.type === "number") return typeof prev === "number" ? prev : Number(prev) || 0;
  if (typeof prev === "boolean" || typeof prev === "number") return defaultRuleValue(spec, op);
  return prev;
}

/** Validate rules → index → message key. */
export function validateRules(rules: FilterRule[], specs: FilterFieldSpec[]): Record<number, string> {
  const errs: Record<number, string> = {};
  rules.forEach((r, i) => {
    const spec = specs.find((s) => s.key === r.field);
    if (!spec) {
      errs[i] = "filters.errors.field";
      return;
    }
    if (!spec.operators.includes(r.operator)) {
      errs[i] = "filters.errors.operator";
      return;
    }
    if (Array.isArray(r.value)) {
      if (!r.value.length) errs[i] = "filters.errors.valueList";
    } else if (spec.type !== "boolean" && (r.value === "" || r.value === null || r.value === undefined)) {
      errs[i] = "filters.errors.value";
    } else if (spec.type === "number" && Number.isNaN(Number(r.value))) {
      errs[i] = "filters.errors.number";
    }
  });
  return errs;
}

export function FilterBuilder({
  fields,
  rules,
  onChange,
  errors,
}: {
  fields: FilterFieldSpec[];
  rules: FilterRule[];
  onChange: (rules: FilterRule[]) => void;
  errors?: Record<number, string>;
}) {
  const { t } = useTranslation();

  function addRule() {
    const spec = fields[0];
    if (!spec) return;
    const op = spec.operators[0] ?? "equals";
    onChange([...rules, { field: spec.key, operator: op, value: defaultRuleValue(spec, op) }]);
  }

  function update(i: number, patch: Partial<FilterRule>) {
    onChange(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  function changeField(i: number, key: string) {
    const spec = fields.find((f) => f.key === key);
    const prev = rules[i];
    const op = spec?.operators.includes(prev.operator) ? prev.operator : (spec?.operators[0] ?? "equals");
    const sameType = fields.find((f) => f.key === prev.field)?.type === spec?.type;
    update(i, { field: key, operator: op, value: sameType ? coerceValue(spec, op, prev.value) : defaultRuleValue(spec, op) });
  }

  function changeOp(i: number, op: FilterOperator) {
    const spec = fields.find((f) => f.key === rules[i].field);
    update(i, { operator: op, value: coerceValue(spec, op, rules[i].value) });
  }

  if (!fields.length) {
    return <p className="text-sm text-muted-foreground">{t("filters.noFields")}</p>;
  }

  return (
    <div className="space-y-3">
      {rules.length === 0 ? (
        <EmptyState
          icon={Filter}
          compact
          title={t("filters.empty.title")}
          description={t("filters.empty.description")}
          action={
            <Button type="button" variant="outline" size="sm" onClick={addRule}>
              <Plus /> {t("filters.add")}
            </Button>
          }
        />
      ) : (
        <>
          <div className="space-y-2">
            {rules.map((rule, i) => {
              const spec = fields.find((f) => f.key === rule.field);
              const err = errors?.[i];
              return (
                <div key={i} className="space-y-1">
                  <div className="flex flex-col gap-2 rounded-md border bg-muted/20 p-2 sm:flex-row sm:items-start">
                    <span className="hidden w-10 shrink-0 pt-2 text-xs font-medium text-muted-foreground uppercase sm:block">
                      {i === 0 ? t("filters.where") : t("filters.and")}
                    </span>
                    <Select value={rule.field} onValueChange={(v) => changeField(i, v)}>
                      <SelectTrigger className="w-full bg-background sm:w-44" aria-label={t("filters.field")}>
                        <SelectValue placeholder={t("filters.field")} />
                      </SelectTrigger>
                      <SelectContent>
                        {fields.map((f) => (
                          <SelectItem key={f.key} value={f.key}>
                            {providerCopy(f.label, t)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Select value={rule.operator} onValueChange={(v) => changeOp(i, v as FilterOperator)}>
                      <SelectTrigger className="w-full bg-background sm:w-40" aria-label={t("filters.operator")}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {(spec?.operators ?? []).map((op) => (
                          <SelectItem key={op} value={op}>
                            {t(`filters.operators.${op}`, { defaultValue: op })}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <div className="min-w-0 flex-1">
                      <ValueControl spec={spec} rule={rule} onChange={(value) => update(i, { value })} invalid={!!err} />
                    </div>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="self-end text-muted-foreground hover:text-destructive sm:self-start"
                      onClick={() => onChange(rules.filter((_, j) => j !== i))}
                      aria-label={t("filters.remove")}
                    >
                      <Trash2 />
                    </Button>
                  </div>
                  {err && <p className="pl-1 text-xs text-destructive">{t(err)}</p>}
                </div>
              );
            })}
          </div>
          <Button type="button" variant="outline" size="sm" onClick={addRule}>
            <Plus /> {t("filters.add")}
          </Button>
        </>
      )}
    </div>
  );
}

function ValueControl({
  spec,
  rule,
  onChange,
  invalid,
}: {
  spec: FilterFieldSpec | undefined;
  rule: FilterRule;
  onChange: (v: FilterValue) => void;
  invalid: boolean;
}) {
  const { t } = useTranslation();
  const multi = isMultiOperator(rule.operator);
  const type = spec?.type ?? "string";

  if (type === "boolean") {
    return (
      <label className="flex h-9 items-center gap-2 text-sm">
        <Switch checked={rule.value === true} onCheckedChange={(c) => onChange(c)} />
        {rule.value === true ? t("common.true") : t("common.false")}
      </label>
    );
  }

  if (type === "enum") {
    const options = spec?.options ?? [];
    if (multi) {
      const selected = Array.isArray(rule.value) ? rule.value.map(String) : [];
      const labels = options.filter((o) => selected.includes(o.value)).map((o) => providerCopy(o.label, t));
      return (
        <Popover>
          <PopoverTrigger asChild>
            <Button
              type="button"
              variant="outline"
              aria-invalid={invalid}
              className="w-full justify-start bg-background font-normal"
            >
              <span className="truncate">{labels.length ? labels.join(", ") : t("filters.selectValues")}</span>
            </Button>
          </PopoverTrigger>
          <PopoverContent align="start" className="w-64 p-1">
            <div className="max-h-64 overflow-y-auto">
              {options.map((o) => {
                const checked = selected.includes(o.value);
                return (
                  <label
                    key={o.value}
                    className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-accent"
                  >
                    <Checkbox
                      checked={checked}
                      onCheckedChange={(c) =>
                        onChange(c ? [...selected, o.value] : selected.filter((v) => v !== o.value))
                      }
                    />
                    {providerCopy(o.label, t)}
                  </label>
                );
              })}
            </div>
          </PopoverContent>
        </Popover>
      );
    }
    return (
      <Select value={String(rule.value ?? "")} onValueChange={(v) => onChange(v)}>
        <SelectTrigger className="w-full bg-background" aria-invalid={invalid}>
          <SelectValue placeholder={t("common.select")} />
        </SelectTrigger>
        <SelectContent>
          {options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {providerCopy(o.label, t)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    );
  }

  if (multi) {
    const values = Array.isArray(rule.value) ? rule.value.map(String) : [];
    return (
      <ChipsInput
        value={values}
        aria-invalid={invalid}
        placeholder={t("filters.valuesPlaceholder")}
        validate={(chip) => (type === "number" && Number.isNaN(Number(chip)) ? t("filters.errors.number") : null)}
        onChange={(v) => onChange(type === "number" ? v.map(Number) : v)}
      />
    );
  }

  if (type === "number") {
    return (
      <Input
        type="number"
        className="bg-background"
        aria-invalid={invalid}
        value={rule.value === "" ? "" : String(rule.value)}
        onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))}
      />
    );
  }

  if (type === "datetime") {
    return (
      <Input
        type="datetime-local"
        className="bg-background"
        aria-invalid={invalid}
        value={typeof rule.value === "string" ? isoToLocalInput(rule.value) : ""}
        onChange={(e) => onChange(localInputToIso(e.target.value))}
      />
    );
  }

  return (
    <Input
      className="bg-background"
      aria-invalid={invalid}
      value={typeof rule.value === "string" ? rule.value : String(rule.value ?? "")}
      onChange={(e) => onChange(e.target.value)}
      placeholder={t("filters.valuePlaceholder")}
    />
  );
}
