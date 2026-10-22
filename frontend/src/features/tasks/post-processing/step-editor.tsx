import {
  ArrowDownToLine,
  ArrowUpToLine,
  Ban,
  CaseSensitive,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  CodeXml,
  Globe,
  ListX,
  Replace,
  Ruler,
  Scissors,
  Trash2,
  WrapText,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { KeyValueEditor } from "@/components/key-value-editor";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import type { PipelineStep, StepType } from "./model";

export const STEP_ICONS: Record<StepType, LucideIcon> = {
  replace: Replace,
  trim: Scissors,
  collapse_whitespace: WrapText,
  strip_html: CodeXml,
  remove_lines: ListX,
  prepend: ArrowUpToLine,
  append: ArrowDownToLine,
  truncate: Ruler,
  case: CaseSensitive,
  skip: Ban,
  http: Globe,
};

const SEPARATORS = [
  { value: "none", sep: "" },
  { value: "space", sep: " " },
  { value: "newline", sep: "\n" },
  { value: "blank", sep: "\n\n" },
] as const;

// Passed as values: i18next would otherwise interpolate the literal placeholders.
const TEMPLATE_LIST = "{{input}}, {{item.name}}, {{item.path}}, {{task.name}}, {{document.fileName}}";
const JSON_EXAMPLE = '"text": "{{input}}"';

function separatorKey(sep: string | undefined) {
  return SEPARATORS.find((s) => s.sep === (sep ?? ""))?.value ?? "none";
}

/** One-line description of a step for its collapsed header. */
export function stepSummary(step: PipelineStep, t: (key: string, opts?: Record<string, unknown>) => string): string {
  switch (step.type) {
    case "replace":
      return step.pattern ? `${step.regex ? `/${step.pattern}/` : `"${step.pattern}"`} → "${step.replacement ?? ""}"` : "";
    case "remove_lines":
    case "skip":
      return step.pattern ? `${step.invert ? "!" : ""}${step.regex ? `/${step.pattern}/` : `"${step.pattern}"`}` : "";
    case "trim":
      return t(`postProcessing.side.${step.side ?? "both"}`);
    case "prepend":
    case "append":
      return step.text ?? "";
    case "truncate":
      return `≤ ${step.maxLength ?? 0}`;
    case "case":
      return t(`postProcessing.case.${step.case ?? "lower"}`);
    case "http":
      return step.url ? `${step.method ?? "POST"} ${step.url}` : "";
    default:
      return "";
  }
}

interface StepCardProps {
  step: PipelineStep;
  index: number;
  count: number;
  expanded: boolean;
  errors: Record<string, string>;
  onToggleExpanded: () => void;
  onChange: (step: PipelineStep) => void;
  onMove: (delta: -1 | 1) => void;
  onRemove: () => void;
}

export function StepCard({ step, index, count, expanded, errors, onToggleExpanded, onChange, onMove, onRemove }: StepCardProps) {
  const { t } = useTranslation();
  const tr = (key: string, opts?: Record<string, unknown>) => t(key, opts ?? {}) as string;
  const Icon = STEP_ICONS[step.type];
  const summary = stepSummary(step, tr);
  const invalid = Object.keys(errors).length > 0;
  const bodyId = `pp-step-${step.id}`;
  return (
    <li className={cn("rounded-lg border bg-background", !step.enabled && "opacity-70", invalid && "border-destructive/60")}>
      <div className="flex items-center gap-2 px-2 py-1.5 sm:px-3">
        <button
          type="button"
          onClick={onToggleExpanded}
          aria-expanded={expanded}
          aria-controls={bodyId}
          className="flex min-w-0 flex-1 items-center gap-2 rounded-md py-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          {expanded ? <ChevronDown className="size-4 shrink-0 text-muted-foreground" /> : <ChevronRight className="size-4 shrink-0 text-muted-foreground" />}
          <span className="w-5 shrink-0 text-right font-mono text-xs text-muted-foreground tabular-nums">{index + 1}</span>
          <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
            <Icon className="size-3.5" />
          </span>
          <span className="shrink-0 text-sm font-medium">{t(`postProcessing.steps.${step.type}.title`)}</span>
          {summary && <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">{summary}</span>}
        </button>
        <Switch
          size="sm"
          checked={step.enabled}
          onCheckedChange={(enabled) => onChange({ ...step, enabled })}
          aria-label={t("postProcessing.stepEnabled")}
        />
        <div className="flex shrink-0 items-center">
          <Button type="button" variant="ghost" size="icon-xs" disabled={index === 0} onClick={() => onMove(-1)} aria-label={t("postProcessing.moveUp")}>
            <ChevronUp />
          </Button>
          <Button type="button" variant="ghost" size="icon-xs" disabled={index === count - 1} onClick={() => onMove(1)} aria-label={t("postProcessing.moveDown")}>
            <ChevronDown />
          </Button>
          <Button type="button" variant="ghost" size="icon-xs" className="text-muted-foreground hover:text-destructive" onClick={onRemove} aria-label={t("postProcessing.removeStep")}>
            <Trash2 />
          </Button>
        </div>
      </div>
      {expanded && (
        <div id={bodyId} className="space-y-4 border-t px-3 py-3 sm:px-4">
          <p className="text-xs text-muted-foreground">{t(`postProcessing.steps.${step.type}.description`)}</p>
          <StepFields step={step} errors={errors} onChange={onChange} />
        </div>
      )}
    </li>
  );
}

function Toggle({ id, label, checked, onChange }: { id: string; label: ReactNode; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label htmlFor={id} className="flex items-center gap-2 text-sm">
      <Switch id={id} size="sm" checked={checked} onCheckedChange={onChange} />
      {label}
    </label>
  );
}

function StepFields({ step, errors, onChange }: { step: PipelineStep; errors: Record<string, string>; onChange: (step: PipelineStep) => void }) {
  const { t } = useTranslation();
  const set = (patch: Partial<PipelineStep>) => onChange({ ...step, ...patch });
  const err = (field: string) => (errors[field] ? t(errors[field]) : undefined);
  const id = (field: string) => `pp-${step.id}-${field}`;

  const patternFields = (
    <>
      <FieldRow label={t("postProcessing.fields.pattern")} htmlFor={id("pattern")} required error={err("pattern")}>
        <Input
          id={id("pattern")}
          value={step.pattern ?? ""}
          onChange={(e) => set({ pattern: e.target.value })}
          placeholder={step.regex ? "^\\s*#.*$" : t("postProcessing.placeholders.pattern")}
          className="font-mono text-xs sm:text-xs"
          aria-invalid={!!errors.pattern}
          spellCheck={false}
        />
      </FieldRow>
      <div className="flex flex-wrap gap-x-6 gap-y-2">
        <Toggle id={id("regex")} label={t("postProcessing.fields.regex")} checked={!!step.regex} onChange={(regex) => set({ regex })} />
        <Toggle id={id("ignoreCase")} label={t("postProcessing.fields.ignoreCase")} checked={!!step.ignoreCase} onChange={(ignoreCase) => set({ ignoreCase })} />
        {(step.type === "remove_lines" || step.type === "skip") && (
          <Toggle id={id("invert")} label={t(`postProcessing.fields.invert_${step.type}`)} checked={!!step.invert} onChange={(invert) => set({ invert })} />
        )}
      </div>
    </>
  );

  switch (step.type) {
    case "replace":
      return (
        <div className="space-y-3">
          {patternFields}
          <FieldRow label={t("postProcessing.fields.replacement")} htmlFor={id("replacement")} help={step.regex ? t("postProcessing.help.replacementRegex") : undefined}>
            <Input
              id={id("replacement")}
              value={step.replacement ?? ""}
              onChange={(e) => set({ replacement: e.target.value })}
              className="font-mono text-xs sm:text-xs"
              spellCheck={false}
            />
          </FieldRow>
        </div>
      );
    case "remove_lines":
    case "skip":
      return <div className="space-y-3">{patternFields}</div>;
    case "trim":
      return (
        <div className="grid gap-3 sm:grid-cols-2">
          <FieldRow label={t("postProcessing.fields.side")}>
            <Segmented
              value={step.side ?? "both"}
              onChange={(side) => set({ side })}
              options={(["both", "start", "end"] as const).map((v) => ({ value: v, label: t(`postProcessing.side.${v}`) }))}
              size="md"
              ariaLabel={t("postProcessing.fields.side")}
            />
          </FieldRow>
          <FieldRow label={t("postProcessing.fields.characters")} htmlFor={id("characters")} help={t("postProcessing.help.characters")}>
            <Input id={id("characters")} value={step.characters ?? ""} onChange={(e) => set({ characters: e.target.value })} className="font-mono text-xs sm:text-xs" />
          </FieldRow>
        </div>
      );
    case "prepend":
    case "append":
      return (
        <div className="space-y-3">
          <FieldRow label={t("postProcessing.fields.text")} htmlFor={id("text")} help={t("postProcessing.help.template", { list: TEMPLATE_LIST })}>
            <Textarea id={id("text")} rows={3} value={step.text ?? ""} onChange={(e) => set({ text: e.target.value })} className="font-mono text-xs" spellCheck={false} />
          </FieldRow>
          <FieldRow label={t("postProcessing.fields.separator")}>
            <Select value={separatorKey(step.separator)} onValueChange={(v) => set({ separator: SEPARATORS.find((s) => s.value === v)?.sep ?? "" })}>
              <SelectTrigger className="w-full sm:w-56">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SEPARATORS.map((s) => (
                  <SelectItem key={s.value} value={s.value}>
                    {t(`postProcessing.separator.${s.value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FieldRow>
        </div>
      );
    case "truncate":
      return (
        <div className="grid gap-3 sm:grid-cols-2">
          <FieldRow label={t("postProcessing.fields.maxLength")} htmlFor={id("maxLength")} required error={err("maxLength")}>
            <Input
              id={id("maxLength")}
              type="number"
              min={1}
              value={step.maxLength ?? ""}
              onChange={(e) => set({ maxLength: e.target.value === "" ? undefined : Math.trunc(Number(e.target.value)) })}
              aria-invalid={!!errors.maxLength}
            />
          </FieldRow>
          <FieldRow label={t("postProcessing.fields.suffix")} htmlFor={id("suffix")}>
            <Input id={id("suffix")} value={step.suffix ?? ""} onChange={(e) => set({ suffix: e.target.value })} />
          </FieldRow>
        </div>
      );
    case "case":
      return (
        <Segmented
          value={step.case ?? "lower"}
          onChange={(c) => set({ case: c })}
          options={(["lower", "upper"] as const).map((v) => ({ value: v, label: t(`postProcessing.case.${v}`) }))}
          size="md"
          ariaLabel={t("postProcessing.steps.case.title")}
        />
      );
    case "http":
      return <HttpFields step={step} errors={errors} set={set} id={id} />;
    default:
      return null;
  }
}

function HttpFields({
  step,
  errors,
  set,
  id,
}: {
  step: PipelineStep;
  errors: Record<string, string>;
  set: (patch: Partial<PipelineStep>) => void;
  id: (field: string) => string;
}) {
  const { t } = useTranslation();
  const bodyType = step.bodyType ?? "none";
  const responseMode = step.responseMode ?? "text";
  return (
    <div className="space-y-4">
      <FieldRow label={t("postProcessing.fields.url")} htmlFor={id("url")} required error={errors.url ? t(errors.url) : undefined} help={t("postProcessing.help.template", { list: TEMPLATE_LIST })}>
        <div className="flex gap-2">
          <Select value={step.method ?? "POST"} onValueChange={(method) => set({ method: method as PipelineStep["method"] })}>
            <SelectTrigger className="w-28 shrink-0 font-mono text-xs" aria-label={t("postProcessing.fields.method")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["GET", "POST", "PUT", "PATCH", "DELETE"] as const).map((m) => (
                <SelectItem key={m} value={m} className="font-mono text-xs">
                  {m}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            id={id("url")}
            value={step.url ?? ""}
            onChange={(e) => set({ url: e.target.value })}
            placeholder="https://api.example.com/convert"
            className="font-mono text-xs sm:text-xs"
            aria-invalid={!!errors.url}
            spellCheck={false}
          />
        </div>
      </FieldRow>

      <FieldRow label={t("postProcessing.fields.query")}>
        <KeyValueEditor rows={step.query ?? []} onChange={(query) => set({ query })} monospace addLabel={t("postProcessing.addParam")} />
      </FieldRow>
      <FieldRow label={t("postProcessing.fields.headers")}>
        <KeyValueEditor rows={step.headers ?? []} onChange={(headers) => set({ headers })} monospace keyPlaceholder="Authorization" valuePlaceholder="Bearer …" addLabel={t("postProcessing.addHeader")} />
      </FieldRow>

      {step.method !== "GET" && (
        <div className="space-y-2">
          <FieldRow label={t("postProcessing.fields.body")}>
            <Segmented
              value={bodyType}
              onChange={(v) => set({ bodyType: v })}
              options={(["none", "input", "json", "form", "text"] as const).map((v) => ({ value: v, label: t(`postProcessing.bodyType.${v}`) }))}
              ariaLabel={t("postProcessing.fields.body")}
              className="flex-wrap"
            />
          </FieldRow>
          {bodyType === "input" && <p className="text-xs text-muted-foreground">{t("postProcessing.help.bodyInput")}</p>}
          {(bodyType === "json" || bodyType === "text") && (
            <>
              <Textarea
                aria-label={t("postProcessing.fields.body")}
                rows={5}
                value={step.body ?? ""}
                onChange={(e) => set({ body: e.target.value })}
                className="font-mono text-xs"
                spellCheck={false}
              />
              <p className="text-xs text-muted-foreground">{bodyType === "json" ? t("postProcessing.help.bodyJson", { example: JSON_EXAMPLE }) : t("postProcessing.help.template", { list: TEMPLATE_LIST })}</p>
            </>
          )}
          {bodyType === "form" && <KeyValueEditor rows={step.form ?? []} onChange={(form) => set({ form })} monospace addLabel={t("postProcessing.addField")} />}
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <FieldRow label={t("postProcessing.fields.response")}>
          <Select value={responseMode} onValueChange={(v) => set({ responseMode: v as PipelineStep["responseMode"] })}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["text", "json", "binary", "ignore"] as const).map((v) => (
                <SelectItem key={v} value={v}>
                  {t(`postProcessing.responseMode.${v}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FieldRow>
        {responseMode === "json" && (
          <FieldRow label={t("postProcessing.fields.responsePath")} htmlFor={id("responsePath")} help={t("postProcessing.help.responsePath")}>
            <Input
              id={id("responsePath")}
              value={step.responsePath ?? ""}
              onChange={(e) => set({ responsePath: e.target.value })}
              placeholder="choices.0.message.content"
              className="font-mono text-xs sm:text-xs"
              spellCheck={false}
            />
          </FieldRow>
        )}
      </div>
    </div>
  );
}
