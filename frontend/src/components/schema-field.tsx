import { FolderSearch } from "lucide-react";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { MASK, type FieldSpec } from "@/lib/types";

export function isSecretField(f: FieldSpec): boolean {
  return f.type === "password" || !!f.secret;
}

/** Initial value for a field from its spec default. */
export function defaultFieldValue(f: FieldSpec): unknown {
  if (f.default !== undefined && f.default !== null) return f.default;
  switch (f.type) {
    case "boolean":
      return false;
    case "number":
      return "";
    default:
      return "";
  }
}

export function defaultConfig(fields: FieldSpec[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of fields) out[f.key] = defaultFieldValue(f);
  return out;
}

/** Merge stored config with defaults for any missing field keys. */
export function mergeConfig(fields: FieldSpec[], stored: Record<string, unknown> | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = { ...(stored ?? {}) };
  for (const f of fields) if (!(f.key in out)) out[f.key] = defaultFieldValue(f);
  return out;
}

export function isEmptyValue(v: unknown): boolean {
  return v === undefined || v === null || (typeof v === "string" && v.trim() === "");
}

/** Validate required / number / url constraints. Returns key → i18n message. */
export function validateConfig(
  fields: FieldSpec[],
  config: Record<string, unknown>,
  t: (key: string, opts?: Record<string, unknown>) => string,
): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const f of fields) {
    const v = config[f.key];
    if (f.required && f.type !== "boolean" && isEmptyValue(v)) {
      errors[f.key] = t("validation.required");
      continue;
    }
    if (isEmptyValue(v)) continue;
    if (f.type === "number" && typeof v !== "number" && Number.isNaN(Number(v))) {
      errors[f.key] = t("validation.number");
    }
    if (f.type === "url" && typeof v === "string" && v !== MASK) {
      try {
        new URL(v);
      } catch {
        errors[f.key] = t("validation.url");
      }
    }
  }
  return errors;
}

/** Normalize values for submission (numbers parsed, empty numbers dropped). */
export function normalizeConfig(fields: FieldSpec[], config: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = { ...config };
  for (const f of fields) {
    const v = out[f.key];
    if (f.type === "number") {
      if (isEmptyValue(v)) delete out[f.key];
      else out[f.key] = typeof v === "number" ? v : Number(v);
    }
    if (f.type === "boolean") out[f.key] = !!v;
  }
  return out;
}

const NONE = "__none__";

export interface SchemaFieldProps {
  field: FieldSpec;
  value: unknown;
  onChange: (value: unknown) => void;
  error?: string;
  /** Edit mode: secrets may hold the server mask. */
  editing?: boolean;
  onBrowse?: () => void;
  browseDisabledReason?: string;
  browseLabel?: string;
  idPrefix?: string;
  extraHelp?: React.ReactNode;
}

/** Renders a single FieldSpec as the appropriate control. */
export function SchemaField({
  field: f,
  value,
  onChange,
  error,
  editing,
  onBrowse,
  browseDisabledReason,
  browseLabel,
  idPrefix = "f",
  extraHelp,
}: SchemaFieldProps) {
  const { t } = useTranslation();
  const id = `${idPrefix}-${f.key}`;
  const secret = isSecretField(f);
  const str = value === undefined || value === null ? "" : String(value);
  const masked = secret && str === MASK;

  let control: React.ReactNode;
  switch (f.type) {
    case "boolean":
      return (
        <div className="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
          <div className="space-y-0.5">
            <label htmlFor={id} className="text-sm font-medium">
              {f.label}
            </label>
            {f.help && <p className="text-xs text-muted-foreground">{f.help}</p>}
            {error && <p className="text-xs font-medium text-destructive">{error}</p>}
          </div>
          <Switch id={id} checked={!!value} onCheckedChange={(c) => onChange(c)} />
        </div>
      );
    case "select": {
      const options = f.options ?? [];
      control = (
        <Select
          value={str === "" ? (f.required ? undefined : NONE) : str}
          onValueChange={(v) => onChange(v === NONE ? "" : v)}
        >
          <SelectTrigger id={id} className="w-full" aria-invalid={!!error}>
            <SelectValue placeholder={f.placeholder ?? t("common.select")} />
          </SelectTrigger>
          <SelectContent>
            {!f.required && <SelectItem value={NONE}>{t("common.none")}</SelectItem>}
            {options.map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      );
      break;
    }
    case "textarea":
      control = (
        <Textarea
          id={id}
          value={str}
          onChange={(e) => onChange(e.target.value)}
          placeholder={f.placeholder}
          aria-invalid={!!error}
          rows={4}
          className={secret ? "font-mono text-xs" : undefined}
        />
      );
      break;
    case "number":
      control = (
        <Input
          id={id}
          type="number"
          inputMode="decimal"
          value={str}
          onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))}
          placeholder={f.placeholder}
          aria-invalid={!!error}
        />
      );
      break;
    default: {
      const input = (
        <Input
          id={id}
          type={secret ? "password" : f.type === "url" ? "url" : "text"}
          autoComplete={secret ? "new-password" : "off"}
          value={str}
          onChange={(e) => onChange(e.target.value)}
          onFocus={(e) => masked && e.currentTarget.select()}
          placeholder={f.placeholder}
          aria-invalid={!!error}
          className={f.browse ? "font-mono text-xs sm:text-xs" : undefined}
        />
      );
      control = onBrowse ? (
        <div className="flex gap-2">
          {input}
          <Button
            type="button"
            variant="outline"
            onClick={onBrowse}
            disabled={!!browseDisabledReason}
            title={browseDisabledReason}
          >
            <FolderSearch />
            {t("browse.button")}
          </Button>
        </div>
      ) : (
        input
      );
    }
  }

  const helpParts: React.ReactNode[] = [];
  if (f.help) helpParts.push(f.help);
  if (secret && editing && masked) helpParts.push(t("secret.keepHint"));
  return (
    <FieldRow
      label={f.label}
      htmlFor={id}
      required={f.required}
      error={error}
      help={
        helpParts.length || browseLabel || extraHelp ? (
          <span className="flex flex-col gap-0.5">
            {browseLabel && (
              <span>
                {t("browse.selected")}: <span className="font-medium text-foreground">{browseLabel}</span>
              </span>
            )}
            {helpParts.map((h, i) => (
              <span key={i}>{h}</span>
            ))}
            {extraHelp}
          </span>
        ) : undefined
      }
      hint={
        secret && editing && masked ? (
          <button
            type="button"
            className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            onClick={() => onChange("")}
          >
            {t("secret.clear")}
          </button>
        ) : undefined
      }
    >
      {control}
    </FieldRow>
  );
}
