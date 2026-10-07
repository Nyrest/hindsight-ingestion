import { Plus } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { CredentialStatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { CredentialSheet } from "@/features/credentials/credential-sheet";
import type { Credential } from "@/lib/types";

/** Credential select filtered by type, with inline "create credential". */
export function CredentialPicker({
  id,
  credentials,
  type,
  typeLabel,
  value,
  onChange,
  invalid,
  disabled,
}: {
  id?: string;
  credentials: Credential[];
  type: string;
  typeLabel: string;
  value: string;
  onChange: (id: string) => void;
  invalid?: boolean;
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const [sheetOpen, setSheetOpen] = useState(false);
  const options = credentials.filter((c) => c.type === type);
  const current = credentials.find((c) => c.id === value);

  return (
    <div className="space-y-1.5">
      <div className="flex gap-2">
        <Select value={value || undefined} onValueChange={onChange} disabled={disabled}>
          <SelectTrigger id={id} className="w-full" aria-invalid={invalid}>
            <SelectValue placeholder={options.length ? t("credentials.pick") : t("credentials.noneOfType", { type: typeLabel })} />
          </SelectTrigger>
          <SelectContent>
            {options.map((c) => (
              <SelectItem key={c.id} value={c.id}>
                <span className="flex items-center gap-2">
                  {c.name}
                  {c.status !== "active" && <CredentialStatusBadge status={c.status} />}
                </span>
              </SelectItem>
            ))}
            {current && !options.includes(current) && (
              <SelectItem value={current.id}>{current.name}</SelectItem>
            )}
          </SelectContent>
        </Select>
        {!disabled && (
          <Button type="button" variant="outline" onClick={() => setSheetOpen(true)} title={t("credentials.createInline")}>
            <Plus />
            <span className="hidden sm:inline">{t("common.new")}</span>
          </Button>
        )}
      </div>
      {current && current.status !== "active" && (
        <p className="text-xs text-amber-700 dark:text-amber-400">
          {t("credentials.notActiveWarning", { status: t(`credentialStatus.${current.status}`) })}
        </p>
      )}
      <CredentialSheet
        open={sheetOpen}
        onOpenChange={setSheetOpen}
        defaultType={type}
        onSaved={(c) => onChange(c.id)}
      />
    </div>
  );
}
