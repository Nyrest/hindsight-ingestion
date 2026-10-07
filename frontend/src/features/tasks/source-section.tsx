import { useState } from "react";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { SchemaField } from "@/components/schema-field";
import { TypeIcon } from "@/components/type-icon";
import { Skeleton } from "@/components/ui/skeleton";
import { BrowseDialog } from "@/features/credentials/browse-dialog";
import type { Credential, FieldSpec, SourceSpec } from "@/lib/types";
import { cn } from "@/lib/utils";
import { CredentialPicker } from "./credential-picker";

export function CapabilityBadges({ source }: { source: SourceSpec }) {
  const { t } = useTranslation();
  const c = source.capabilities;
  const badges: string[] = [
    t(`capabilities.incremental.${c.incrementalMode}`, { defaultValue: c.incrementalMode }),
    t(`capabilities.deletion.${c.deletionMode}`, { defaultValue: c.deletionMode }),
  ];
  if (c.supportsFiles) badges.push(t("capabilities.files"));
  if (c.supportsOAuth) badges.push(t("capabilities.oauth"));
  if (c.supportsAdvancedFilter) badges.push(t("capabilities.advancedFilter"));
  return (
    <div className="flex flex-wrap gap-1">
      {badges.map((b) => (
        <span key={b} className="rounded border bg-background px-1.5 py-px text-[10px] font-medium text-muted-foreground">
          {b}
        </span>
      ))}
    </div>
  );
}

export function SourceTypePicker({
  sources,
  value,
  onChange,
  loading,
}: {
  sources: SourceSpec[];
  value: string;
  onChange: (type: string) => void;
  loading?: boolean;
}) {
  if (loading) {
    return (
      <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-20" />
        ))}
      </div>
    );
  }
  return (
    <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3" role="radiogroup">
      {sources.map((s) => {
        const active = s.type === value;
        return (
          <button
            key={s.type}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => !active && onChange(s.type)}
            className={cn(
              "flex flex-col gap-2 rounded-lg border p-3 text-left transition-colors",
              active ? "border-primary bg-primary/5 ring-1 ring-primary" : "hover:bg-accent/50",
            )}
          >
            <span className="flex items-center gap-2.5">
              <TypeIcon type={s.type} boxed />
              <span className="text-sm font-medium">{s.name}</span>
            </span>
            <CapabilityBadges source={s} />
          </button>
        );
      })}
    </div>
  );
}

/** Source credential + schema-driven sourceConfig fields with Browse… support. */
export function SourceConfigFields({
  source,
  credentials,
  credentialId,
  onCredentialChange,
  credentialError,
  config,
  onConfigChange,
  configErrors,
  editing,
}: {
  source: SourceSpec;
  credentials: Credential[];
  credentialId: string;
  onCredentialChange: (id: string) => void;
  credentialError?: string;
  config: Record<string, unknown>;
  onConfigChange: (key: string, value: unknown) => void;
  configErrors: Record<string, string>;
  editing: boolean;
}) {
  const { t } = useTranslation();
  const [browseField, setBrowseField] = useState<FieldSpec | null>(null);
  const [labels, setLabels] = useState<Record<string, string>>({});
  const credTypeName = source.name;

  return (
    <>
      <FieldRow label={t("source.credential")} htmlFor="task-source-cred" required error={credentialError}>
        <CredentialPicker
          id="task-source-cred"
          credentials={credentials}
          type={source.credentialType}
          typeLabel={credTypeName}
          value={credentialId}
          onChange={onCredentialChange}
          invalid={!!credentialError}
        />
      </FieldRow>

      {source.fields.length > 0 && (
        <div className="grid gap-4">
          {source.fields.map((f) => (
            <SchemaField
              key={f.key}
              field={f}
              idPrefix="src"
              value={config[f.key]}
              editing={editing}
              error={configErrors[f.key]}
              onChange={(v) => {
                onConfigChange(f.key, v);
                setLabels((l) => {
                  if (!(f.key in l)) return l;
                  const next = { ...l };
                  delete next[f.key];
                  return next;
                });
              }}
              onBrowse={f.browse ? () => setBrowseField(f) : undefined}
              browseDisabledReason={f.browse && !credentialId ? t("source.pickCredentialFirst") : undefined}
              browseLabel={labels[f.key]}
            />
          ))}
        </div>
      )}

      {browseField && credentialId && (
        <BrowseDialog
          open={!!browseField}
          onOpenChange={(o) => !o && setBrowseField(null)}
          credentialId={credentialId}
          kind={browseField.browseKind ?? source.browseKinds?.[0] ?? ""}
          config={config}
          title={t("browse.titleFor", { label: browseField.label })}
          onSelect={(item) => {
            onConfigChange(browseField.key, item.id);
            setLabels((l) => ({ ...l, [browseField.key]: item.path || item.name }));
          }}
        />
      )}
    </>
  );
}
