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
import { useTimezone } from "@/features/settings/api";
import { coerceValue, defaultRuleValue, isMultiOperator, isUnaryOperator } from "./filter-model";
import { isoToLocalInput, localInputToIso } from "@/lib/format";
import { providerCopy } from "@/lib/provider-copy";
import type { FilterFieldSpec, FilterGroup, FilterOperator, FilterRule, FilterValue, SourceSpec } from "@/lib/types";

interface FilterBuilderProps {
  fields: FilterFieldSpec[];
  group: FilterGroup;
  capabilities: SourceSpec["filterCapabilities"];
  onChange: (group: FilterGroup) => void;
  errors?: Record<string, string>;
}

export function FilterBuilder(props: FilterBuilderProps) {
  const { t } = useTranslation();
  if (!props.fields.length) return <p className="text-sm text-muted-foreground">{t("filters.noFields")}</p>;
  return <GroupEditor {...props} path="expression" depth={1} />;
}

function GroupEditor({ fields, group, capabilities, onChange, errors, path, depth, onRemove }: FilterBuilderProps & {
  path: string;
  depth: number;
  onRemove?: () => void;
}) {
  const { t } = useTranslation();
  const groups = group.groups ?? [];
  const combinator = group.combinator ?? "and";
  const empty = !group.rules.length && !groups.length;
  function newRule(): FilterRule {
    const spec = fields[0];
    const operator = spec.operators[0] ?? "equals";
    return { field: spec.key, operator, value: defaultRuleValue(spec, operator) };
  }
  function updateRule(i: number, rule: FilterRule) {
    onChange({ ...group, rules: group.rules.map((r, j) => j === i ? rule : r) });
  }
  const addRule = () => onChange({ ...group, rules: [...group.rules, newRule()] });
  if (empty && depth === 1) {
    return <EmptyState icon={Filter} compact title={t("filters.empty.title")} description={t("filters.empty.description")}
      action={<Button type="button" variant="outline" size="sm" onClick={addRule}><Plus /> {t("filters.add")}</Button>} />;
  }
  return (
    <div className="min-w-0 space-y-3">
      <div className="flex items-center justify-between gap-2">
        <Select value={combinator} onValueChange={(v) => {
          if (v === "and" || v === "or") onChange({ ...group, combinator: v });
        }}>
          <SelectTrigger className="w-auto max-w-full" aria-label={t("filters.match")}><SelectValue /></SelectTrigger>
          <SelectContent>{capabilities.combinators.map((op) => <SelectItem key={op} value={op}>{t(`filters.match${op === "and" ? "All" : "Any"}`)}</SelectItem>)}</SelectContent>
        </Select>
        {onRemove && <Button type="button" variant="ghost" size="icon" onClick={onRemove} aria-label={t("filters.removeGroup")}><Trash2 /></Button>}
      </div>
      {errors?.[path] && <p role="alert" className="text-xs text-destructive">{t(errors[path])}</p>}
      {group.rules.map((rule, i) => <RuleEditor key={i} fields={fields} rule={rule}
        connective={i === 0 ? "where" : combinator} error={errors?.[`${path}.rules.${i}`]}
        onChange={(r) => updateRule(i, r)} onRemove={() => onChange({ ...group, rules: group.rules.filter((_, j) => j !== i) })} />)}
      {groups.map((child, i) => (
        <div key={i} className="space-y-2 border-l-2 pl-2 sm:pl-3">
          {(group.rules.length > 0 || i > 0) && <p className="text-xs font-medium text-muted-foreground">{t(`filters.${combinator}`)}</p>}
          <GroupEditor fields={fields} group={child} capabilities={capabilities} errors={errors} depth={depth + 1} path={`${path}.groups.${i}`}
            onChange={(g) => onChange({ ...group, groups: groups.map((old, j) => j === i ? g : old) })}
            onRemove={() => onChange({ ...group, groups: groups.filter((_, j) => j !== i) })} />
        </div>
      ))}
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" size="sm" onClick={addRule}><Plus /> {t("filters.add")}</Button>
        {depth < capabilities.maxDepth && <Button type="button" variant="ghost" size="sm"
          onClick={() => onChange({ ...group, groups: [...groups, { combinator: "and", rules: [newRule()], groups: [] }] })}><Plus /> {t("filters.addGroup")}</Button>}
      </div>
    </div>
  );
}

function RuleEditor({ fields, rule, connective, error, onChange, onRemove }: {
  fields: FilterFieldSpec[];
  rule: FilterRule;
  connective: string;
  error?: string;
  onChange: (rule: FilterRule) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const spec = fields.find((f) => f.key === rule.field);
  function changeField(key: string) {
    if (!key) return;
    const next = fields.find((f) => f.key === key);
    const operator = next?.operators.includes(rule.operator) ? rule.operator : (next?.operators[0] ?? "equals");
    const value = spec?.type === next?.type ? coerceValue(next, operator, rule.value) : defaultRuleValue(next, operator);
    onChange({ field: key, operator, value });
  }
  return (
    <div className="space-y-1">
      <div className="flex flex-col flex-wrap gap-2 rounded-md border bg-muted/20 p-2 sm:flex-row sm:items-start">
        <span className="hidden shrink-0 pt-2 text-xs font-medium text-muted-foreground uppercase sm:block">{t(`filters.${connective}`)}</span>
        <Select value={rule.field} onValueChange={changeField}>
          <SelectTrigger className="w-full bg-background sm:w-40" aria-label={t("filters.field")}><SelectValue placeholder={t("filters.field")} /></SelectTrigger>
          <SelectContent>{fields.map((f) => <SelectItem key={f.key} value={f.key}>{providerCopy(f.label, t)}</SelectItem>)}</SelectContent>
        </Select>
        <Select value={rule.operator} onValueChange={(v) => {
          if (spec?.operators.includes(v as FilterOperator)) onChange({ ...rule, operator: v as FilterOperator, value: coerceValue(spec, v as FilterOperator, rule.value) });
        }}>
          <SelectTrigger className="w-full bg-background sm:w-auto sm:max-w-72" aria-label={t("filters.operator")}><SelectValue /></SelectTrigger>
          <SelectContent>{(spec?.operators ?? []).map((op) => <SelectItem key={op} value={op}>{t(`filters.operators.${op}`)}</SelectItem>)}</SelectContent>
        </Select>
        {!isUnaryOperator(rule.operator) && <div className="min-w-0 flex-1 sm:basis-40"><ValueControl spec={spec} rule={rule} onChange={(value) => onChange({ ...rule, value })} invalid={!!error} /></div>}
        <Button type="button" variant="ghost" size="icon" className="self-end text-muted-foreground hover:text-destructive sm:self-start" onClick={onRemove} aria-label={t("filters.remove")}><Trash2 /></Button>
      </div>
      {rule.operator === "regex" && <p className="text-xs text-muted-foreground">{t("filters.regexHint")}</p>}
      {error && <p role="alert" className="pl-1 text-xs text-destructive">{t(error, { defaultValue: error })}</p>}
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
  const timezone = useTimezone();
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
        validate={(chip) => (type === "number" && !Number.isFinite(Number(chip)) ? t("filters.errors.number") : null)}
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
        title={timezone}
        value={typeof rule.value === "string" ? isoToLocalInput(rule.value, timezone) : ""}
        onChange={(e) => onChange(localInputToIso(e.target.value, timezone))}
      />
    );
  }

  return (
    <Input
      className="bg-background"
      aria-invalid={invalid}
      value={typeof rule.value === "string" ? rule.value : String(rule.value ?? "")}
      onChange={(e) => onChange(e.target.value)}
      placeholder={rule.operator === "regex" ? "(?i)report.*\\.pdf$" : t("filters.valuePlaceholder")}
      title={rule.operator === "regex" ? t("filters.regexHint") : undefined}
    />
  );
}
