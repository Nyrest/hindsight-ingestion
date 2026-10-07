import { Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export interface KeyValueRow {
  key: string;
  value: string;
}

export interface KeyValueEditorProps {
  rows: KeyValueRow[];
  onChange: (rows: KeyValueRow[]) => void;
  /** Per-row error message (index → message). */
  rowErrors?: Record<number, string>;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  keyLabel?: string;
  valueLabel?: string;
  addLabel?: string;
  emptyText?: string;
  monospace?: boolean;
  isSecretValue?: (row: KeyValueRow) => boolean;
}

/** Generic Key | Value rows editor with "+ Add" and remove buttons. */
export function KeyValueEditor({
  rows,
  onChange,
  rowErrors,
  keyPlaceholder,
  valuePlaceholder,
  keyLabel,
  valueLabel,
  addLabel,
  emptyText,
  monospace,
  isSecretValue,
}: KeyValueEditorProps) {
  const { t } = useTranslation();

  function update(i: number, patch: Partial<KeyValueRow>) {
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  return (
    <div className="space-y-2">
      {rows.length > 0 && (
        <div className="hidden grid-cols-[1fr_1fr_2.25rem] gap-2 px-0.5 text-xs font-medium text-muted-foreground sm:grid">
          <span>{keyLabel ?? t("kv.key")}</span>
          <span>{valueLabel ?? t("kv.value")}</span>
          <span />
        </div>
      )}
      {rows.length === 0 && emptyText && (
        <p className="rounded-md border border-dashed px-3 py-3 text-center text-xs text-muted-foreground">{emptyText}</p>
      )}
      {rows.map((row, i) => {
        const err = rowErrors?.[i];
        return (
          <div key={i} className="space-y-1">
            <div className="grid grid-cols-[1fr_2.25rem] gap-2 sm:grid-cols-[1fr_1fr_2.25rem]">
              <Input
                value={row.key}
                onChange={(e) => update(i, { key: e.target.value })}
                placeholder={keyPlaceholder ?? t("kv.key")}
                aria-label={keyLabel ?? t("kv.key")}
                aria-invalid={!!err}
                className={cn(monospace && "font-mono text-xs sm:text-xs")}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-destructive sm:order-last"
                onClick={() => onChange(rows.filter((_, j) => j !== i))}
                aria-label={t("kv.remove")}
              >
                <Trash2 />
              </Button>
              <Input
                value={row.value}
                type={isSecretValue?.(row) ? "password" : "text"}
                onChange={(e) => update(i, { value: e.target.value })}
                placeholder={valuePlaceholder ?? t("kv.value")}
                aria-label={valueLabel ?? t("kv.value")}
                className={cn("col-span-1 sm:col-span-1", monospace && "font-mono text-xs sm:text-xs")}
              />
            </div>
            {err && <p className="text-xs text-destructive">{err}</p>}
          </div>
        );
      })}
      <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rows, { key: "", value: "" }])}>
        <Plus />
        {addLabel ?? t("kv.add")}
      </Button>
    </div>
  );
}
