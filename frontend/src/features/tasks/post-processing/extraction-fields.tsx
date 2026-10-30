import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { KeyValueEditor } from "@/components/key-value-editor";
import { Segmented } from "@/components/segmented";
import { Input } from "@/components/ui/input";
import type { PipelineStep } from "./model";

const ENDPOINTS: Record<string, string> = {
  unstructured: "https://api.example.com/general/v0/general",
  kreuzberg: "https://api.example.com/extract",
  docling: "https://api.example.com/v1/convert/file",
  mineru: "https://mineru.example.com",
};

export function ExtractionFields({ step, errors, onChange }: {
  step: PipelineStep;
  errors: Record<string, string>;
  onChange: (step: PipelineStep) => void;
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<PipelineStep>) => onChange({ ...step, ...patch });
  const id = (field: string) => `pp-${step.id}-${field}`;
  const cloud = step.type === "mineru" && step.apiMode === "cloud";
  return (
    <div className="space-y-4">
      {step.type === "mineru" && (
        <FieldRow label={t("postProcessing.extraction.apiMode")}>
          <Segmented
            value={step.apiMode ?? "self_hosted"}
            onChange={(apiMode) => set({ apiMode, url: apiMode === "cloud" ? "https://mineru.net" : "", apiKey: "", headers: [] })}
            options={(["self_hosted", "cloud"] as const).map((value) => ({ value, label: t(`postProcessing.extraction.${value}`) }))}
            ariaLabel={t("postProcessing.extraction.apiMode")}
          />
        </FieldRow>
      )}
      <FieldRow label={t("postProcessing.fields.url")} htmlFor={id("url")} required error={errors.url ? t(errors.url) : undefined}
        help={t(step.type === "mineru" ? "postProcessing.extraction.baseURL" : "postProcessing.extraction.endpoint")}>
        <Input id={id("url")} value={step.url ?? ""} onChange={(e) => set({ url: e.target.value })}
          placeholder={cloud ? "https://mineru.net" : ENDPOINTS[step.type]} spellCheck={false} aria-invalid={!!errors.url} />
      </FieldRow>
      <FieldRow label={t("postProcessing.extraction.apiKey")} htmlFor={id("apiKey")} required={cloud} error={errors.apiKey ? t(errors.apiKey) : undefined}>
        <Input id={id("apiKey")} type="password" autoComplete="off" value={step.apiKey ?? ""}
          onChange={(e) => set({ apiKey: e.target.value })} aria-invalid={!!errors.apiKey} />
      </FieldRow>
      <FieldRow label={t("postProcessing.fields.headers")}>
        <KeyValueEditor rows={step.headers ?? []} onChange={(headers) => set({ headers })} monospace addLabel={t("postProcessing.addHeader")} />
      </FieldRow>
      {step.type !== "mineru" && (
        <FieldRow label={t("postProcessing.bodyType.form")} help={t("postProcessing.extraction.formHelp")}>
          <KeyValueEditor rows={step.form ?? []} onChange={(form) => set({ form })} monospace addLabel={t("postProcessing.addField")} />
        </FieldRow>
      )}
    </div>
  );
}
