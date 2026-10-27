import { ChevronDown } from "lucide-react";
import { Tabs } from "radix-ui";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { SchemaField } from "@/components/schema-field";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import type { FieldSpec } from "@/lib/types";
import { isValidNotionDepth, notionSourceFields } from "./notion-source-model";

export function NotionSourceFields({ fields, config, onChange, errors, onBrowse, credentialId, labels }: {
  fields: FieldSpec[];
  config: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
  errors: Record<string, string>;
  onBrowse: (field: FieldSpec) => void;
  credentialId: string;
  labels: Record<string, string>;
}) {
  const { t } = useTranslation();
  const mode = config.mode === "pages" ? "pages" : "database";
  const activeFields = notionSourceFields(fields, config);
  const renderField = (field: FieldSpec) => (
    <SchemaField key={field.key} field={field} value={config[field.key]} error={errors[field.key]}
      onChange={(value) => onChange(field.key, value)}
      onBrowse={field.browse ? () => onBrowse(field) : undefined}
      browseDisabledReason={field.browse && !credentialId ? t("source.pickCredentialFirst") : undefined}
      browseLabel={labels[field.key]} />
  );
  return (
    <div className="grid gap-4">
      <Tabs.Root value={mode} onValueChange={(value) => onChange("mode", value)}>
        <Tabs.List aria-label={t("notion.sourceMode")} className="mb-4 inline-flex gap-1 rounded-lg bg-muted p-1">
          {(["database", "pages"] as const).map((value) => (
            <Tabs.Trigger key={value} value={value} type="button"
              className="rounded-md px-4 py-1.5 text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-background data-[state=active]:shadow-sm">
              {t(`notion.${value}`)}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        {(["database", "pages"] as const).map((value) => (
          <Tabs.Content key={value} value={value}>
            {activeFields.filter((field) => field.key === (value === "pages" ? "pageIds" : "dataSourceId")).map(renderField)}
          </Tabs.Content>
        ))}
      </Tabs.Root>
      {errors.mode && <p role="alert" className="text-xs text-destructive">{errors.mode}</p>}
      <div className="rounded-md border">
        <div className="flex items-start justify-between gap-4 px-3 py-2.5">
          <div className="space-y-0.5">
            <label htmlFor="notion-include-subpages" className="text-sm font-medium">{t("providerCopy.notionIncludeSubpages")}</label>
            <p className="text-xs text-muted-foreground">{t("providerCopy.notionIncludeSubpagesHelp")}</p>
          </div>
          <Switch id="notion-include-subpages" checked={config.includeSubpages === true}
            onCheckedChange={(checked) => {
              if (!checked && !isValidNotionDepth(config.maxDepth)) onChange("maxDepth", 3);
              onChange("includeSubpages", checked);
            }} />
        </div>
        {config.includeSubpages === true && (
          <div className="border-t px-3 py-3">
            <NotionDepthInput value={config.maxDepth === undefined ? 3 : config.maxDepth} error={errors.maxDepth}
              onChange={(value) => onChange("maxDepth", value)} />
          </div>
        )}
      </div>
    </div>
  );
}

function NotionDepthInput({ value, error, onChange }: {
  value: unknown;
  error?: string;
  onChange: (value: unknown) => void;
}) {
  const { t } = useTranslation();
  const unlimited = t("notion.unlimited");
  return (
    <FieldRow label={t("providerCopy.notionMaxDepth")} htmlFor="notion-max-depth" error={error} help={t("notion.depthInputHelp")}>
      <div className="relative">
        <Input id="notion-max-depth" type="text" value={value === null ? unlimited : String(value)} className="pr-10"
          aria-invalid={!!error} autoComplete="off" spellCheck={false} placeholder="3"
          onChange={(event) => {
            const text = event.target.value;
            const normalized = text.trim().toLowerCase();
            onChange(normalized === "unlimited" || normalized === unlimited.toLowerCase() ? null : text);
          }} />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button type="button" variant="ghost" size="icon-sm" className="absolute right-0.5 top-0.5" aria-label={t("notion.chooseDepth")}>
              <ChevronDown className="size-4 text-muted-foreground" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuRadioGroup value={value === null ? "unlimited" : String(value)}
              onValueChange={(depth) => onChange(depth === "unlimited" ? null : Number(depth))}>
              {[0, 1, 2, 3, 5, 10].map((depth) => (
                <DropdownMenuRadioItem key={depth} value={String(depth)}>{depth}</DropdownMenuRadioItem>
              ))}
              <DropdownMenuRadioItem value="unlimited">{unlimited}</DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </FieldRow>
  );
}
