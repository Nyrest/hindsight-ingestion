import { FolderSearch } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { BrowseDialog } from "@/features/credentials/browse-dialog";
import { useStrategies } from "@/features/credentials/api";

const BANK_DEFAULT = "__bank_default__";

/** Bank id input with a "Browse…" picker listing banks of a Hindsight credential. */
export function BankPicker({
  id,
  credentialId,
  value,
  onChange,
  invalid,
}: {
  id?: string;
  credentialId: string;
  value: string;
  onChange: (v: string) => void;
  invalid?: boolean;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState<string | null>(null);
  return (
    <div className="space-y-1.5">
      <div className="flex gap-2">
        <Input
          id={id}
          value={value}
          onChange={(e) => {
            onChange(e.target.value);
            setLabel(null);
          }}
          placeholder={t("destination.bankPlaceholder")}
          className="font-mono text-xs sm:text-xs"
          aria-invalid={invalid}
          spellCheck={false}
        />
        <Button
          type="button"
          variant="outline"
          onClick={() => setOpen(true)}
          disabled={!credentialId}
          title={!credentialId ? t("destination.pickCredentialFirst") : undefined}
        >
          <FolderSearch />
          {t("browse.button")}
        </Button>
      </div>
      {label && label !== value && (
        <p className="text-xs text-muted-foreground">
          {t("browse.selected")}: <span className="font-medium text-foreground">{label}</span>
        </p>
      )}
      {credentialId && (
        <BrowseDialog
          open={open}
          onOpenChange={setOpen}
          credentialId={credentialId}
          kind="bank"
          title={t("destination.browseBanks")}
          onSelect={(item) => {
            onChange(item.id);
            setLabel(item.name);
          }}
        />
      )}
    </div>
  );
}

/** Retain strategy select loaded from the destination; falls back to free text if the call fails. */
export function RetainStrategyField({
  id,
  credentialId,
  bankId,
  value,
  onChange,
}: {
  id?: string;
  credentialId: string;
  bankId: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const { t } = useTranslation();
  const q = useStrategies(credentialId, bankId.trim());
  const ready = !!credentialId && !!bankId.trim();

  if (!ready || q.isError) {
    return (
      <div className="space-y-1.5">
        <Input
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={t("retain.freeTextPlaceholder")}
        />
        <p className="text-xs text-muted-foreground">
          {!ready ? t("retain.pickDestinationFirst") : t("retain.loadFailed")}
        </p>
      </div>
    );
  }

  const strategies = q.data?.strategies ?? [];
  const options = value && !strategies.includes(value) ? [value, ...strategies] : strategies;
  const defaultName = q.data?.defaultStrategy;

  return (
    <div className="space-y-1.5">
      <Select
        value={value === "" ? BANK_DEFAULT : value}
        onValueChange={(v) => onChange(v === BANK_DEFAULT ? "" : v)}
        disabled={q.isLoading}
      >
        <SelectTrigger id={id} className="w-full">
          <SelectValue placeholder={q.isLoading ? t("common.loading") : undefined} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={BANK_DEFAULT}>
            {defaultName ? t("retain.bankDefaultNamed", { name: defaultName }) : t("retain.bankDefault")}
          </SelectItem>
          {options.map((s) => (
            <SelectItem key={s} value={s}>
              {s}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-xs text-muted-foreground">{t("retain.help")}</p>
    </div>
  );
}
