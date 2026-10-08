import { zodResolver } from "@hookform/resolvers/zod";
import { useQuery } from "@tanstack/react-query";
import {
  ArrowLeft,
  FlaskConical,
  Brain,
  CalendarClock,
  Database,
  FileStack,
  Filter,
  History,
  Loader2,
  Play,
  Save,
  Settings2,
  Square,
  Tags,
  Braces,
  Sparkles,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router";
import { toast } from "sonner";
import { ChipsInput } from "@/components/chips-input";
import { EmptyState } from "@/components/empty-state";
import { FieldRow } from "@/components/field-row";
import { DEFAULT_FILE_POLICY, FilePolicySwitches } from "@/components/file-policy-switches";
import { KeyValueEditor } from "@/components/key-value-editor";
import { mergeConfig, defaultConfig, normalizeConfig, validateConfig } from "@/components/schema-field";
import { Segmented } from "@/components/segmented";
import { RunStatusBadge } from "@/components/status-badge";
import { TypeIcon } from "@/components/type-icon";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { useCredentials } from "@/features/credentials/api";
import { RunsTable } from "@/features/runs/runs-table";
import { api, errorMessage, isApiError } from "@/lib/api";
import { findSource, typeName, useConnectors } from "@/lib/connectors";
import { providerCopy } from "@/lib/provider-copy";
import { formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { qk } from "@/lib/query";
import type { Task } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useCancelTask, useCreateTask, useTaskRuns, useTriggerTask, useUpdateTask } from "./api";
import { CredentialPicker } from "./credential-picker";
import { BankPicker, RetainStrategyField } from "./destination-fields";
import { EditorSection, Hint } from "./editor-section";
import { FilterBuilder, validateRules } from "./filter-builder";
import { ScheduleFields } from "./schedule-fields";
import { SourceConfigFields, SourceTypePicker } from "./source-section";
import {
  emptyTaskForm,
  formToInput,
  metadataRowErrors,
  sectionForField,
  taskFormSchema,
  taskToForm,
  type TaskFormValues,
} from "./task-form-model";

import { DryRunDialog } from "./dry-run-dialog";

const SECTIONS = [
  { id: "general", icon: Settings2 },
  { id: "source", icon: Database },
  { id: "filters", icon: Filter },
  { id: "destination", icon: Brain },
  { id: "retain", icon: Sparkles },
  { id: "files", icon: FileStack },
  { id: "inline", icon: Sparkles },
  { id: "tags", icon: Tags },
  { id: "metadata", icon: Braces },
  { id: "schedule", icon: CalendarClock },
] as const;

type SectionId = (typeof SECTIONS)[number]["id"];

function scrollToSection(id: string) {
  document.getElementById(`section-${id}`)?.scrollIntoView({ behavior: "smooth", block: "start" });
}

function useActiveSection(ids: readonly string[]): string {
  const [active, setActive] = useState(ids[0] ?? "");
  useEffect(() => {
    const els = ids.map((id) => document.getElementById(`section-${id}`)).filter(Boolean) as HTMLElement[];
    if (!els.length) return;
    const visible = new Map<string, number>();
    const obs = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          const id = (e.target as HTMLElement).dataset.section ?? "";
          if (e.isIntersecting) visible.set(id, e.boundingClientRect.top);
          else visible.delete(id);
        }
        const first = ids.find((id) => visible.has(id));
        if (first) setActive(first);
      },
      { rootMargin: "-80px 0px -55% 0px", threshold: 0 },
    );
    els.forEach((el) => obs.observe(el));
    return () => obs.disconnect();
  }, [ids]);
  return active;
}

export function TaskEditor({ task }: { task?: Task }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const now = useNow();
  const editing = !!task;
  const [dryRunOpen, setDryRunOpen] = useState(false);
  const connectors = useConnectors();
  const credentials = useCredentials();
  const settings = useQuery({ queryKey: qk.settings, queryFn: api.getSettings, staleTime: 60_000 });
  const create = useCreateTask();
  const update = useUpdateTask();
  const trigger = useTriggerTask();
  const cancel = useCancelTask();
  const runs = useTaskRuns(task?.id, 5, 0, !!task?.running);

  const form = useForm<TaskFormValues>({
    resolver: zodResolver(taskFormSchema),
    defaultValues: task ? taskToForm(task) : emptyTaskForm(),
    mode: "onSubmit",
  });
  const { control, register, watch, setValue, formState } = form;
  const errors = formState.errors;

  const [configErrors, setConfigErrors] = useState<Record<string, string>>({});
  const [ruleErrors, setRuleErrors] = useState<Record<number, string>>({});
  const [serverError, setServerError] = useState<string | null>(null);

  const sourceType = watch("sourceType");
  const sourceConfig = watch("sourceConfig");
  const filterMode = watch("filterMode");
  const filterRules = watch("filterRules");
  const destinationCredentialId = watch("destinationCredentialId");
  const destinationBankId = watch("destinationBankId");
  const filePolicyMode = watch("filePolicyMode");
  const inlineMode = watch("inlineMultimodalMode");
  const inlineOverride = watch("inlineMultimodalEnabled");
  const filePolicy = watch("filePolicy");
  const metadataRows = watch("metadataRows");

  const source = findSource(connectors.data, sourceType);
  const supportsFiles = !!source?.capabilities.supportsFiles;
  const supportsInline = !!source?.capabilities.supportsInlineMultimodal;
  const effectiveInline = inlineMode === "override" ? inlineOverride : !!settings.data?.inlineMultimodalEnabled;
  const effectiveImages = filePolicyMode === "override" ? filePolicy.images : !!settings.data?.filePolicy.images;
  const locked = !!task?.destinationLocked;
  const credList = credentials.data ?? [];

  // Fill defaults for fields of the selected source once the schema is available.
  useEffect(() => {
    if (!source) return;
    const merged = mergeConfig(source.fields, form.getValues("sourceConfig"));
    // When editing a pristine form, make the merged defaults part of the baseline so it isn't marked dirty.
    if (editing && !form.formState.isDirty) form.reset({ ...form.getValues(), sourceConfig: merged });
    else setValue("sourceConfig", merged);
  }, [source, setValue, form, editing]);

  const sectionIds = useMemo(
    () => SECTIONS.filter((s) => (s.id !== "files" || supportsFiles || supportsInline) && (s.id !== "inline" || supportsInline)).map((s) => s.id),
    [supportsFiles, supportsInline],
  );
  const activeSection = useActiveSection(sectionIds);

  const tr = (key: string, opts?: Record<string, unknown>) => t(key, opts ?? {}) as string;
  const errText = (msg?: string) => (msg ? t(msg, { defaultValue: msg }) : undefined);

  function changeSourceType(type: string) {
    const spec = findSource(connectors.data, type);
    setValue("sourceType", type, { shouldDirty: true, shouldValidate: formState.isSubmitted });
    setValue("sourceCredentialId", "", { shouldDirty: true });
    setValue("sourceConfig", spec ? defaultConfig(spec.fields) : {}, { shouldDirty: true });
    setValue("filterRules", [], { shouldDirty: true });
    setValue("filterMode", "simple", { shouldDirty: true });
    setValue("advancedQuery", "", { shouldDirty: true });
    setConfigErrors({});
    setRuleErrors({});
  }

  function focusFirstError(keys: string[]) {
    const order = sectionIds as readonly string[];
    const sections = keys.map(sectionForField).sort((a, b) => order.indexOf(a) - order.indexOf(b));
    if (sections[0]) scrollToSection(sections[0]);
  }

  async function onValid(values: TaskFormValues) {
    setServerError(null);
    const spec = findSource(connectors.data, values.sourceType);
    const cfgErrs = spec ? validateConfig(spec.fields, values.sourceConfig, tr) : {};
    const rErrs = values.filterMode === "simple" && spec ? validateRules(values.filterRules, spec.filterFields) : {};
    setConfigErrors(cfgErrs);
    setRuleErrors(rErrs);
    if (Object.keys(cfgErrs).length || Object.keys(rErrs).length) {
      focusFirstError([...(Object.keys(cfgErrs).length ? ["sourceConfig"] : []), ...(Object.keys(rErrs).length ? ["sourceFilter"] : [])]);
      return;
    }
    const input = formToInput(values, spec ? normalizeConfig(spec.fields, values.sourceConfig) : values.sourceConfig);
    if (!spec?.capabilities.supportsAdvancedFilter) input.sourceFilter.mode = "simple";

    try {
      if (editing) {
        const body = { ...input } as Partial<typeof input>;
        if (locked) {
          delete body.destinationCredentialId;
          delete body.destinationBankId;
        }
        const saved = await update.mutateAsync({ id: task!.id, body });
        form.reset(taskToForm(saved));
        toast.success(t("taskEditor.toast.updated"), { description: saved.name });
      } else {
        const saved = await create.mutateAsync(input);
        toast.success(t("taskEditor.toast.created"), { description: saved.name });
        navigate("/tasks");
      }
    } catch (e) {
      if (isApiError(e) && Object.keys(e.fields).length) {
        const cfg: Record<string, string> = {};
        const unknown: string[] = [];
        for (const [k, msg] of Object.entries(e.fields)) {
          if (k.startsWith("sourceConfig.")) cfg[k.slice("sourceConfig.".length)] = msg;
          else if (k.startsWith("sourceFilter")) setRuleErrorsFromServer(k, msg);
          else if (k.startsWith("customMetadata")) form.setError("metadataRows", { message: msg });
          else if (k === "sourceFilter.advancedQuery") form.setError("advancedQuery", { message: msg });
          else if (k in values) form.setError(k as keyof TaskFormValues, { message: msg });
          else unknown.push(`${k}: ${msg}`);
        }
        setConfigErrors(cfg);
        setServerError(unknown.length ? `${e.message} — ${unknown.join("; ")}` : e.message);
        focusFirstError(Object.keys(e.fields));
      } else if (isApiError(e) && e.status === 409) {
        setServerError(e.message);
        toast.error(t("taskEditor.toast.conflict"), { description: e.message });
      } else {
        setServerError(errorMessage(e));
        toast.error(t("taskEditor.toast.saveFailed"), { description: errorMessage(e) });
      }
    }
  }

  function setRuleErrorsFromServer(key: string, msg: string) {
    const m = /rules\.(\d+)/.exec(key) ?? /rules\[(\d+)\]/.exec(key);
    if (m) setRuleErrors((r) => ({ ...r, [Number(m[1])]: msg }));
    else if (key.includes("advancedQuery")) form.setError("advancedQuery", { message: msg });
    else setServerError(msg);
  }

  function onInvalid(errs: Record<string, unknown>) {
    focusFirstError(
      Object.keys(errs).map((k) =>
        k === "metadataRows"
          ? "customMetadata"
          : k === "filterRules" || k === "advancedQuery"
            ? "sourceFilter"
            : k,
      ),
    );
  }

  const saving = create.isPending || update.isPending;
  const metaErrs = metadataRowErrors(metadataRows);
  const metaRowErrors: Record<number, string> = {};
  metaErrs.forEach((m, i) => {
    if (m) metaRowErrors[i] = t(m);
  });

  const autoTags = ["ingestion", `source:${sourceType || "<type>"}`, `ingestion_task:${task?.id ?? t("tags.newTaskId")}`];
  const lastRun = runs.data?.items[0] ?? task?.lastRun ?? null;

  return (
    <form onSubmit={form.handleSubmit(onValid, onInvalid)} noValidate>
      {/* Header */}
      <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 space-y-1">
          <Link to="/tasks" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
            <ArrowLeft className="size-3.5" /> {t("nav.tasks")}
          </Link>
          <h1 className="flex items-center gap-2.5 truncate text-xl font-semibold tracking-tight sm:text-2xl">
            {sourceType && <TypeIcon type={sourceType} boxed />}
            <span className="truncate">{editing ? task!.name : t("taskEditor.newTitle")}</span>
          </h1>
          {editing && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
              {task!.running ? (
                <RunStatusBadge status="running" />
              ) : lastRun ? (
                <span className="flex items-center gap-1.5">
                  <RunStatusBadge status={lastRun.status} />
                  {lastRun.startedAt && formatRelative(lastRun.startedAt, now)}
                </span>
              ) : null}
              {task!.nextRunAt && task!.enabled && (
                <span>{t("taskEditor.nextRun", { when: formatRelative(task!.nextRunAt, now) })}</span>
              )}
              <span>{t("taskEditor.itemCount", { count: task!.itemCount })}</span>
              {task!.reconcileRequired && <span className="text-amber-600 dark:text-amber-400">{t("tasks.reconcileRequired")}</span>}
            </div>
          )}
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          {editing &&
            (task!.running ? (
              <Button type="button" variant="outline" onClick={() => cancel.mutate(task!.id)} disabled={cancel.isPending}>
                {cancel.isPending ? <Loader2 className="animate-spin" /> : <Square />}
                {t("tasks.actions.cancel")}
              </Button>
            ) : (
              <Button
                type="button"
                variant="outline"
                onClick={() => trigger.mutate({ id: task!.id, kind: "run", name: task!.name })}
                disabled={trigger.isPending || formState.isDirty}
                title={formState.isDirty ? t("taskEditor.saveBeforeRun") : undefined}
              >
                {trigger.isPending ? <Loader2 className="animate-spin" /> : <Play />}
                {t("tasks.actions.runNow")}
              </Button>
            ))}
          {task && <Button type="button" variant="outline" onClick={() => setDryRunOpen(true)} disabled={task.running || formState.isDirty} title={formState.isDirty ? t("taskEditor.saveBeforeRun") : undefined}>
            <FlaskConical /> {t("tasks.actions.dryRun")}
          </Button>}
          <Button type="submit" disabled={saving || (editing && !formState.isDirty)}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />}
            {editing ? t("common.saveChanges") : t("taskEditor.create")}
          </Button>
        </div>
      </div>

      {/* Mobile section tabs */}
      <div className="sticky top-14 z-20 -mx-4 mb-4 overflow-x-auto border-b bg-background/90 px-4 py-2 backdrop-blur lg:hidden">
        <div className="flex gap-1">
          {SECTIONS.filter((s) => sectionIds.includes(s.id)).map((s) => (
            <button
              key={s.id}
              type="button"
              onClick={() => scrollToSection(s.id)}
              className={cn(
                "shrink-0 rounded-md px-2.5 py-1 text-xs font-medium whitespace-nowrap",
                activeSection === s.id ? "bg-secondary text-foreground" : "text-muted-foreground",
              )}
            >
              {t(`taskEditor.sections.${s.id}.title`)}
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-6 lg:grid-cols-[190px_minmax(0,1fr)]">
        {/* Desktop sticky nav */}
        <nav className="hidden lg:block">
          <div className="sticky top-8 space-y-0.5">
            {SECTIONS.filter((s) => sectionIds.includes(s.id)).map(({ id, icon: Icon }) => (
              <button
                key={id}
                type="button"
                onClick={() => scrollToSection(id)}
                className={cn(
                  "flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm transition-colors",
                  activeSection === id
                    ? "bg-secondary font-medium text-foreground"
                    : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
                )}
              >
                <Icon className="size-4" />
                {t(`taskEditor.sections.${id}.title`)}
              </button>
            ))}
            {editing && (
              <button
                type="button"
                onClick={() => scrollToSection("runs")}
                className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-muted-foreground hover:bg-accent/60 hover:text-foreground"
              >
                <History className="size-4" />
                {t("taskEditor.recentRuns")}
              </button>
            )}
          </div>
        </nav>

        <div className="min-w-0 space-y-6">
          {serverError && (
            <Alert variant="destructive">
              <AlertTitle>{t("taskEditor.saveError")}</AlertTitle>
              <AlertDescription>{serverError}</AlertDescription>
            </Alert>
          )}

          {/* General */}
          <EditorSection id="general" icon={Settings2} title={t("taskEditor.sections.general.title")} description={t("taskEditor.sections.general.description")}>
            <FieldRow label={t("taskEditor.fields.name")} htmlFor="task-name" required error={errText(errors.name?.message)}>
              <Input id="task-name" placeholder={t("taskEditor.namePlaceholder")} aria-invalid={!!errors.name} {...register("name")} />
            </FieldRow>
            <Controller
              control={control}
              name="enabled"
              render={({ field }) => (
                <label htmlFor="task-enabled" className="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
                  <span>
                    <span className="block text-sm font-medium">{t("taskEditor.fields.enabled")}</span>
                    <span className="block text-xs text-muted-foreground">{t("taskEditor.enabledHelp")}</span>
                  </span>
                  <Switch id="task-enabled" checked={field.value} onCheckedChange={field.onChange} />
                </label>
              )}
            />
          </EditorSection>

          {/* Source */}
          <EditorSection id="source" icon={Database} title={t("taskEditor.sections.source.title")} description={t("taskEditor.sections.source.description")}>
            {editing && <Hint tone="warning">{t("taskEditor.hints.sourceReset")}</Hint>}
            <FieldRow label={t("source.type")} required error={errText(errors.sourceType?.message)}>
              {editing ? (
                <div className="flex items-center gap-3 rounded-md border px-3 py-2.5">
                  <TypeIcon type={sourceType} boxed />
                  <div className="min-w-0 flex-1 space-y-1">
                    <div className="text-sm font-medium">{providerCopy(typeName(connectors.data, sourceType), t)}</div>
                    {source && <SourceCapabilityLine sourceType={sourceType} />}
                  </div>
                </div>
              ) : (
                <SourceTypePicker
                  sources={connectors.data?.sources ?? []}
                  value={sourceType}
                  onChange={changeSourceType}
                  loading={connectors.isLoading}
                />
              )}
            </FieldRow>
            {editing && <p className="-mt-3 text-xs text-muted-foreground">{t("taskEditor.hints.sourceTypeLocked")}</p>}
            {source ? (
              <Controller
                control={control}
                name="sourceCredentialId"
                render={({ field }) => (
                  <SourceConfigFields
                    source={source}
                    credentials={credList}
                    credentialId={field.value}
                    onCredentialChange={(id) => field.onChange(id)}
                    credentialError={errText(errors.sourceCredentialId?.message)}
                    config={sourceConfig}
                    onConfigChange={(key, value) => {
                      setValue("sourceConfig", { ...form.getValues("sourceConfig"), [key]: value }, { shouldDirty: true });
                      if (configErrors[key]) {
                        setConfigErrors((prev) => {
                          const next = { ...prev };
                          delete next[key];
                          return next;
                        });
                      }
                    }}
                    configErrors={configErrors}
                    editing={editing}
                  />
                )}
              />
            ) : (
              !connectors.isLoading && <p className="text-sm text-muted-foreground">{t("source.pickTypeFirst")}</p>
            )}
          </EditorSection>

          {/* Filters */}
          <EditorSection
            id="filters"
            icon={Filter}
            title={t("taskEditor.sections.filters.title")}
            description={t("taskEditor.sections.filters.description")}
            actions={
              source?.capabilities.supportsAdvancedFilter ? (
                <Segmented
                  value={filterMode}
                  onChange={(v) => setValue("filterMode", v, { shouldDirty: true })}
                  options={[
                    { value: "simple", label: t("filters.simple") },
                    { value: "advanced", label: t("filters.advanced") },
                  ]}
                  ariaLabel={t("filters.mode")}
                />
              ) : undefined
            }
          >
            {editing && <Hint tone="warning">{t("taskEditor.hints.filtersReconcile")}</Hint>}
            {!source ? (
              <p className="text-sm text-muted-foreground">{t("source.pickTypeFirst")}</p>
            ) : filterMode === "advanced" && source.capabilities.supportsAdvancedFilter ? (
              <FieldRow
                label={t("filters.advancedQuery")}
                htmlFor="task-adv-query"
                required
                error={errText(errors.advancedQuery?.message)}
                help={sourceType === "google_drive" ? t("filters.driveHint") : t("filters.advancedHint")}
              >
                <Textarea
                  id="task-adv-query"
                  rows={4}
                  spellCheck={false}
                  className="font-mono text-xs"
                  placeholder={sourceType === "google_drive" ? "mimeType = 'application/pdf' and name contains 'report'" : ""}
                  aria-invalid={!!errors.advancedQuery}
                  {...register("advancedQuery")}
                />
              </FieldRow>
            ) : (
              <FilterBuilder
                fields={source.filterFields ?? []}
                rules={filterRules}
                errors={ruleErrors}
                onChange={(rules) => {
                  setValue("filterRules", rules, { shouldDirty: true });
                  if (Object.keys(ruleErrors).length) setRuleErrors({});
                }}
              />
            )}
          </EditorSection>

          {/* Destination */}
          <EditorSection id="destination" icon={Brain} title={t("taskEditor.sections.destination.title")} description={t("taskEditor.sections.destination.description")}>
            {locked ? (
              <>
                <Hint tone="locked">{t("destination.locked")}</Hint>
                <div className="grid gap-4 sm:grid-cols-2">
                  <FieldRow label={t("destination.credential")}>
                    <div className="flex h-9 items-center gap-2 rounded-md border bg-muted/40 px-3 text-sm">
                      <TypeIcon type="hindsight" />
                      {credList.find((c) => c.id === destinationCredentialId)?.name ?? destinationCredentialId}
                    </div>
                  </FieldRow>
                  <FieldRow label={t("destination.bank")}>
                    <div className="flex h-9 items-center rounded-md border bg-muted/40 px-3 font-mono text-xs">{destinationBankId}</div>
                  </FieldRow>
                </div>
              </>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2">
                <FieldRow label={t("destination.credential")} htmlFor="task-dest-cred" required error={errText(errors.destinationCredentialId?.message)}>
                  <Controller
                    control={control}
                    name="destinationCredentialId"
                    render={({ field }) => (
                      <CredentialPicker
                        id="task-dest-cred"
                        credentials={credList}
                        type="hindsight"
                        typeLabel={providerCopy(typeName(connectors.data, "hindsight"), t)}
                        value={field.value}
                        onChange={field.onChange}
                        invalid={!!errors.destinationCredentialId}
                      />
                    )}
                  />
                </FieldRow>
                <FieldRow label={t("destination.bank")} htmlFor="task-dest-bank" required error={errText(errors.destinationBankId?.message)}>
                  <Controller
                    control={control}
                    name="destinationBankId"
                    render={({ field }) => (
                      <BankPicker
                        id="task-dest-bank"
                        credentialId={destinationCredentialId}
                        value={field.value}
                        onChange={field.onChange}
                        invalid={!!errors.destinationBankId}
                      />
                    )}
                  />
                </FieldRow>
                {!editing && <Hint className="sm:col-span-2">{t("destination.lockNotice")}</Hint>}
              </div>
            )}
          </EditorSection>

          {/* Retain strategy */}
          <EditorSection id="retain" icon={Sparkles} title={t("taskEditor.sections.retain.title")} description={t("taskEditor.sections.retain.description")}>
            {editing && <Hint>{t("taskEditor.hints.policyRevision")}</Hint>}
            <FieldRow label={t("retain.label")} htmlFor="task-retain" error={errText(errors.retainStrategy?.message)}>
              <Controller
                control={control}
                name="retainStrategy"
                render={({ field }) => (
                  <RetainStrategyField
                    id="task-retain"
                    credentialId={destinationCredentialId}
                    bankId={destinationBankId}
                    value={field.value}
                    onChange={field.onChange}
                  />
                )}
              />
            </FieldRow>
          </EditorSection>

          {/* File types */}
          {(supportsFiles || supportsInline) && (
            <EditorSection id="files" icon={FileStack} title={t("taskEditor.sections.files.title")} description={t("taskEditor.sections.files.description")}>
              {editing && <Hint tone="warning">{t("taskEditor.hints.filesReconcile")}</Hint>}
              <Controller
                control={control}
                name="filePolicyMode"
                render={({ field }) => (
                  <RadioGroup value={field.value} onValueChange={field.onChange} className="grid gap-2 sm:grid-cols-2">
                    {(["global", "override"] as const).map((m) => (
                      <label
                        key={m}
                        htmlFor={`fpm-${m}`}
                        className={cn(
                          "flex cursor-pointer items-start gap-3 rounded-md border px-3 py-2.5",
                          field.value === m && "border-primary bg-primary/5",
                        )}
                      >
                        <RadioGroupItem id={`fpm-${m}`} value={m} className="mt-0.5" />
                        <span>
                          <span className="block text-sm font-medium">{t(`filePolicy.mode.${m}`)}</span>
                          <span className="block text-xs text-muted-foreground">{t(`filePolicy.mode.${m}Help`)}</span>
                        </span>
                      </label>
                    ))}
                  </RadioGroup>
                )}
              />
              <Controller
                control={control}
                name="filePolicy"
                render={({ field }) =>
                  filePolicyMode === "override" ? (
                    <FilePolicySwitches value={field.value} onChange={field.onChange} idPrefix="task-fp" groups={supportsFiles ? undefined : ["images"]} />
                  ) : (
                    <div className="space-y-2">
                      <p className="text-xs text-muted-foreground">{t("filePolicy.globalPreview")}</p>
                      <FilePolicySwitches
                        groups={supportsFiles ? undefined : ["images"]}
                        value={settings.data?.filePolicy ?? DEFAULT_FILE_POLICY}
                        onChange={() => undefined}
                        disabled
                        idPrefix="task-fp-global"
                      />
                    </div>
                  )
                }
              />
            </EditorSection>
          )}

          {/* Inline media */}
          {supportsInline && (
            <EditorSection id="inline" icon={Sparkles} title={t("inlineMultimodal.title")} description={t("inlineMultimodal.description")}>
              <Controller control={control} name="inlineMultimodalMode" render={({ field }) => (
                <RadioGroup value={field.value} onValueChange={field.onChange} className="flex flex-wrap gap-6">
                  {(["global", "override"] as const).map((mode) => (
                    <label key={mode} htmlFor={`inline-${mode}`} className="flex items-center gap-2">
                      <RadioGroupItem id={`inline-${mode}`} value={mode} />{t(`filePolicy.mode.${mode}`)}
                    </label>
                  ))}
                </RadioGroup>
              )} />
              <FieldRow label={t("inlineMultimodal.enabled")} htmlFor="task-inline" help={t("inlineMultimodal.policyHelp")}>
                <Controller control={control} name="inlineMultimodalEnabled" render={({ field }) => (
                  <Switch id="task-inline" checked={effectiveInline} disabled={inlineMode === "global"} onCheckedChange={field.onChange} />
                )} />
              </FieldRow>
              <p className="text-sm text-muted-foreground">{t(effectiveInline && effectiveImages ? "inlineMultimodal.active" : "inlineMultimodal.inactive")}</p>
            </EditorSection>
          )}

          {/* Tags */}
          <EditorSection id="tags" icon={Tags} title={t("taskEditor.sections.tags.title")} description={t("taskEditor.sections.tags.description")}>
            <FieldRow label={t("tags.custom")} help={t("tags.help")} error={errText(errors.customTags?.message)}>
              <Controller
                control={control}
                name="customTags"
                render={({ field }) => (
                  <ChipsInput value={field.value} onChange={field.onChange} readOnlyChips={autoTags} placeholder={t("tags.placeholder")} />
                )}
              />
            </FieldRow>
          </EditorSection>

          {/* Metadata */}
          <EditorSection id="metadata" icon={Braces} title={t("taskEditor.sections.metadata.title")} description={t("taskEditor.sections.metadata.description")}>
            <Controller
              control={control}
              name="metadataRows"
              render={({ field }) => (
                <KeyValueEditor
                  rows={field.value}
                  onChange={field.onChange}
                  rowErrors={metaRowErrors}
                  keyPlaceholder="project"
                  valuePlaceholder="alpha"
                  addLabel={t("metadata.add")}
                  emptyText={t("metadata.empty")}
                />
              )}
            />
            {errors.metadataRows?.message && (
              <p className="text-xs text-destructive">{errText(errors.metadataRows.message)}</p>
            )}
            <p className="text-xs text-muted-foreground">{t("metadata.reservedHint")}</p>
          </EditorSection>

          {/* Schedule */}
          <EditorSection id="schedule" icon={CalendarClock} title={t("taskEditor.sections.schedule.title")} description={t("taskEditor.sections.schedule.description")}>
            <ScheduleFields
              cron={watch("cronExpression")}
              timezone={watch("cronTimezone")}
              onCronChange={(v) => setValue("cronExpression", v, { shouldDirty: true, shouldValidate: formState.isSubmitted })}
              onTimezoneChange={(v) => setValue("cronTimezone", v, { shouldDirty: true, shouldValidate: formState.isSubmitted })}
              cronError={errText(errors.cronExpression?.message)}
              timezoneError={errText(errors.cronTimezone?.message)}
            />
          </EditorSection>

          {/* Recent runs */}
          {editing && (
            <section id="section-runs" data-section="runs" className="scroll-mt-24 space-y-3">
              <div className="flex items-center justify-between">
                <h2 className="flex items-center gap-2 text-base font-semibold tracking-tight">
                  <History className="size-4 text-muted-foreground" /> {t("taskEditor.recentRuns")}
                </h2>
                <Button asChild variant="link" size="sm">
                  <Link to={`/runs?taskId=${encodeURIComponent(task!.id)}`}>{t("common.viewAll")}</Link>
                </Button>
              </div>
              {runs.data?.items.length ? (
                <RunsTable runs={runs.data.items} showTask={false} />
              ) : (
                <EmptyState
                  compact
                  icon={History}
                  title={runs.isLoading ? t("common.loading") : t("runs.empty.taskTitle")}
                  description={runs.isLoading ? undefined : t("runs.empty.taskDescription")}
                />
              )}
            </section>
          )}

          {/* Bottom save bar */}
          <div className="sticky bottom-0 z-10 -mx-1 flex items-center justify-end gap-2 rounded-lg border bg-background/90 px-3 py-2.5 shadow-sm backdrop-blur">
            <span className="mr-auto text-xs text-muted-foreground">
              {formState.isDirty ? t("taskEditor.unsaved") : editing ? t("taskEditor.saved") : ""}
            </span>
            <Button type="button" variant="ghost" onClick={() => navigate("/tasks")}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={saving || (editing && !formState.isDirty)}>
              {saving ? <Loader2 className="animate-spin" /> : <Save />}
              {editing ? t("common.saveChanges") : t("taskEditor.create")}
            </Button>
          </div>
        </div>
      </div>
      {task && dryRunOpen && <DryRunDialog task={task} onClose={() => setDryRunOpen(false)} />}
    </form>
  );
}

function SourceCapabilityLine({ sourceType }: { sourceType: string }) {
  const connectors = useConnectors();
  const source = findSource(connectors.data, sourceType);
  const { t } = useTranslation();
  if (!source) return null;
  const c = source.capabilities;
  return (
    <div className="text-xs text-muted-foreground">
      {t(`capabilities.incremental.${c.incrementalMode}`, { defaultValue: c.incrementalMode })} ·{" "}
      {t(`capabilities.deletion.${c.deletionMode}`, { defaultValue: c.deletionMode })}
    </div>
  );
}

export type { SectionId };
