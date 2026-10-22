import { useMutation } from "@tanstack/react-query";
import { Code2, FlaskConical, ListOrdered, Loader2, Play, Plus, WandSparkles } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { useTranslation } from "react-i18next";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { FieldRow } from "@/components/field-row";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Hint } from "../editor-section";
import {
  DEFAULT_CODE,
  STEP_TYPES,
  effectiveCode,
  generateCode,
  newStep,
  stepErrors,
  type Pipeline,
  type PipelineStep,
  type StepType,
} from "./model";
import { STEP_ICONS, StepCard } from "./step-editor";

const CodeEditor = lazy(() => import("./code-editor"));

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

export interface PostProcessingSectionProps {
  value: Pipeline;
  onChange: (value: Pipeline) => void;
  /** Server validation errors keyed relative to postProcessing (e.g. "steps.0.url"). */
  errors: Record<string, string>;
  /** Show client validation errors (after a submit attempt). */
  showErrors: boolean;
  editing: boolean;
  taskId?: string;
  taskName: string;
  sourceType: string;
}

export function PostProcessingSection({ value, onChange, errors, showErrors, editing, taskId, taskName, sourceType }: PostProcessingSectionProps) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [confirmConvert, setConfirmConvert] = useState(false);
  const set = (patch: Partial<Pipeline>) => onChange({ ...value, ...patch });
  const generated = generateCode(value.steps);

  function setSteps(steps: PipelineStep[]) {
    set({ steps });
  }

  function addStep(type: StepType) {
    const step = newStep(type);
    setExpanded((prev) => new Set(prev).add(step.id));
    setSteps([...value.steps, step]);
  }

  function move(index: number, delta: -1 | 1) {
    const steps = [...value.steps];
    const [step] = steps.splice(index, 1);
    steps.splice(index + delta, 0, step);
    setSteps(steps);
  }

  function convertToCode() {
    set({ mode: "code", code: generated });
  }

  const codeDiffers = !!value.code.trim() && value.code !== generated && value.code !== DEFAULT_CODE;

  return (
    <div className="space-y-4">
      <label htmlFor="pp-enabled" className="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
        <span className="space-y-0.5">
          <span className="block text-sm font-medium">{t("postProcessing.enabled")}</span>
          <span className="block text-xs text-muted-foreground">{t(value.enabled ? "postProcessing.enabledHelp" : "postProcessing.disabledHelp")}</span>
        </span>
        <Switch id="pp-enabled" checked={value.enabled} onCheckedChange={(enabled) => set({ enabled })} />
      </label>
      {editing && <Hint>{t("postProcessing.reretainHint")}</Hint>}

      <div className={cn("space-y-4", !value.enabled && "opacity-80")}>
        {value.mode === "ui" ? (
          <>
            {value.code.trim() && codeDiffers && <Hint>{t("postProcessing.codeNotUsed")}</Hint>}
            {value.steps.length === 0 ? (
              <p className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground">{t("postProcessing.empty")}</p>
            ) : (
              <ol className="space-y-2" aria-label={t("postProcessing.stepsLabel")}>
                {value.steps.map((step, i) => {
                  const serverErrs: Record<string, string> = {};
                  for (const [k, msg] of Object.entries(errors)) {
                    const prefix = `steps.${i}.`;
                    if (k.startsWith(prefix)) serverErrs[k.slice(prefix.length)] = msg;
                  }
                  const errs = { ...(showErrors ? stepErrors(step) : {}), ...serverErrs };
                  return (
                    <StepCard
                      key={step.id}
                      step={step}
                      index={i}
                      count={value.steps.length}
                      expanded={expanded.has(step.id) || Object.keys(errs).length > 0}
                      errors={errs}
                      onToggleExpanded={() =>
                        setExpanded((prev) => {
                          const next = new Set(prev);
                          if (!next.delete(step.id)) next.add(step.id);
                          return next;
                        })
                      }
                      onChange={(s) => setSteps(value.steps.map((x, j) => (j === i ? s : x)))}
                      onMove={(delta) => move(i, delta)}
                      onRemove={() => setSteps(value.steps.filter((_, j) => j !== i))}
                    />
                  );
                })}
              </ol>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button type="button" variant="outline" size="sm">
                    <Plus /> {t("postProcessing.addStep")}
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start" className="w-72">
                  {STEP_TYPES.map((type) => {
                    const Icon = STEP_ICONS[type];
                    return (
                      <DropdownMenuItem key={type} onSelect={() => addStep(type)} className="items-start gap-2.5">
                        <Icon className="mt-0.5" />
                        <span>
                          <span className="block text-sm">{t(`postProcessing.steps.${type}.title`)}</span>
                          <span className="block text-xs text-muted-foreground">{t(`postProcessing.steps.${type}.description`)}</span>
                        </span>
                      </DropdownMenuItem>
                    );
                  })}
                </DropdownMenuContent>
              </DropdownMenu>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => (codeDiffers ? setConfirmConvert(true) : convertToCode())}
                disabled={value.steps.length === 0}
              >
                <WandSparkles /> {t("postProcessing.convertToCode")}
              </Button>
            </div>
          </>
        ) : (
          <div className="space-y-2">
            <Hint>{t("postProcessing.codeHelp")}</Hint>
            <Suspense fallback={<Skeleton className="h-[420px] w-full rounded-md" />}>
              <CodeEditor
                path={`post-processing/${taskId ?? "new"}`}
                value={value.code}
                onChange={(code) => set({ code })}
                ariaLabel={t("postProcessing.codeLabel")}
              />
            </Suspense>
            {errors.code && <p className="text-xs font-medium text-destructive">{errors.code}</p>}
            {showErrors && !value.code.trim() && value.enabled && <p className="text-xs font-medium text-destructive">{t("validation.required")}</p>}
            <p className="text-xs text-muted-foreground">
              {t("postProcessing.shortcuts", { format: isMac ? "⇧⌥F" : "Shift+Alt+F", suggest: isMac ? "⌃Space" : "Ctrl+Space" })}
            </p>
          </div>
        )}

        <TestPanel pipeline={value} taskId={taskId} taskName={taskName} sourceType={sourceType} />
      </div>

      <ConfirmDialog
        open={confirmConvert}
        onOpenChange={setConfirmConvert}
        title={t("postProcessing.convertTitle")}
        description={t("postProcessing.convertDescription")}
        confirmLabel={t("postProcessing.convertToCode")}
        onConfirm={convertToCode}
      />
    </div>
  );
}

/** Mode tabs rendered in the section header. */
export function PostProcessingModeTabs({ value, onChange }: { value: Pipeline; onChange: (value: Pipeline) => void }) {
  const { t } = useTranslation();
  const generated = generateCode(value.steps);
  const tabs = [
    { mode: "ui" as const, icon: ListOrdered, label: t("postProcessing.mode.ui") },
    { mode: "code" as const, icon: Code2, label: t("postProcessing.mode.code") },
  ];
  return (
    <div role="tablist" aria-label={t("postProcessing.mode.label")} className="inline-flex items-center rounded-md border bg-muted/50 p-0.5">
      {tabs.map(({ mode, icon: Icon, label }) => {
        const active = value.mode === mode;
        return (
          <button
            key={mode}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => {
              if (active) return;
              if (mode === "code" && !value.code.trim()) onChange({ ...value, mode, code: value.steps.length ? generated : DEFAULT_CODE });
              else onChange({ ...value, mode });
            }}
            className={cn(
              "inline-flex h-7 items-center gap-1.5 rounded-[5px] px-2.5 text-xs font-medium transition-colors",
              active ? "bg-background text-foreground shadow-xs ring-1 ring-border" : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="size-3.5" />
            {label}
          </button>
        );
      })}
    </div>
  );
}

function TestPanel({ pipeline, taskId, taskName, sourceType }: { pipeline: Pipeline; taskId?: string; taskName: string; sourceType: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [input, setInput] = useState("");
  const [itemName, setItemName] = useState("example.md");
  const test = useMutation({
    mutationFn: () =>
      api.testPostProcessing({
        postProcessing: { ...pipeline, enabled: true },
        input,
        itemName,
        taskId,
        taskName,
        sourceType,
      }),
  });
  const result = test.data;
  const running = !!effectiveCode({ ...pipeline, enabled: true }).trim();

  if (!open) {
    return (
      <Button type="button" variant="outline" size="sm" onClick={() => setOpen(true)}>
        <FlaskConical /> {t("postProcessing.test.open")}
      </Button>
    );
  }
  return (
    <div className="space-y-3 rounded-lg border bg-muted/20 p-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="flex items-center gap-2 text-sm font-medium">
          <FlaskConical className="size-4 text-muted-foreground" /> {t("postProcessing.test.title")}
        </h3>
        <Button type="button" variant="ghost" size="xs" onClick={() => setOpen(false)}>
          {t("common.close")}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{t("postProcessing.test.description")}</p>
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_14rem]">
        <FieldRow label={t("postProcessing.test.input")} htmlFor="pp-test-input">
          <Textarea id="pp-test-input" rows={6} value={input} onChange={(e) => setInput(e.target.value)} className="font-mono text-xs" spellCheck={false} />
        </FieldRow>
        <FieldRow label={t("postProcessing.test.itemName")} htmlFor="pp-test-name">
          <Input id="pp-test-name" value={itemName} onChange={(e) => setItemName(e.target.value)} />
        </FieldRow>
      </div>
      <Button type="button" size="sm" onClick={() => test.mutate()} disabled={test.isPending || !running}>
        {test.isPending ? <Loader2 className="animate-spin" /> : <Play />}
        {t("postProcessing.test.run")}
      </Button>
      {test.isError && <p role="alert" className="text-sm text-destructive">{errorMessage(test.error)}</p>}
      {result && (
        <div className="space-y-2" aria-live="polite">
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <Badge variant={result.ok ? "secondary" : "destructive"}>{t(`postProcessing.test.outputType.${result.outputType}`)}</Badge>
            <span className="tabular-nums">{result.durationMs} ms</span>
            {result.fileName && <span className="font-mono">{result.fileName}</span>}
            {result.mimeType && <span className="font-mono">{result.mimeType}</span>}
          </div>
          {result.error && <pre className="overflow-x-auto rounded-md border border-destructive/40 bg-destructive/5 p-2 font-mono text-xs whitespace-pre-wrap text-destructive">{result.error}</pre>}
          {result.outputType === "text" && (
            <pre className="max-h-72 overflow-auto rounded-md border bg-background p-2 font-mono text-xs whitespace-pre-wrap">{result.output || " "}</pre>
          )}
          {result.outputType === "binary" && (
            <p className="text-xs text-muted-foreground">{t("postProcessing.test.binaryOutput", { bytes: Math.floor((result.output.length * 3) / 4) })}</p>
          )}
          {result.outputType === "skip" && <p className="text-xs text-muted-foreground">{t("postProcessing.test.skipped")}</p>}
          {result.context && (
            <p className="text-xs text-muted-foreground">
              {t("postProcessing.test.context")}: <span className="font-mono">{result.context}</span>
            </p>
          )}
          {result.logs.length > 0 && (
            <div className="space-y-1">
              <p className="text-xs font-medium">{t("postProcessing.test.logs")}</p>
              <pre className="max-h-48 overflow-auto rounded-md border bg-background p-2 font-mono text-xs whitespace-pre-wrap">
                {result.logs.map((l) => `[${l.level}] ${l.message}`).join("\n")}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
