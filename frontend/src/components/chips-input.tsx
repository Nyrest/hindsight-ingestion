import { X } from "lucide-react";
import { useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

export interface ChipsInputProps {
  value: string[];
  onChange: (value: string[]) => void;
  placeholder?: string;
  /** Return an error message to reject a chip. */
  validate?: (chip: string, existing: string[]) => string | null;
  readOnlyChips?: string[];
  className?: string;
  inputType?: "text" | "number";
  id?: string;
  "aria-invalid"?: boolean;
}

/** Tag-style input: Enter / comma / Tab adds a chip, Backspace on empty removes the last. */
export function ChipsInput({
  value,
  onChange,
  placeholder,
  validate,
  readOnlyChips,
  className,
  inputType = "text",
  id,
  ...rest
}: ChipsInputProps) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);

  function commit(raw: string): boolean {
    const parts = raw
      .split(/[,\n]/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (!parts.length) return false;
    const next = [...value];
    for (const p of parts) {
      if (next.includes(p)) {
        setError(t("chips.duplicate", { value: p }));
        return false;
      }
      const err = validate?.(p, next);
      if (err) {
        setError(err);
        return false;
      }
      next.push(p);
    }
    setError(null);
    onChange(next);
    setDraft("");
    return true;
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      commit(draft);
    } else if (e.key === "Tab" && draft.trim()) {
      if (commit(draft)) e.preventDefault();
    } else if (e.key === "Backspace" && !draft && value.length) {
      onChange(value.slice(0, -1));
    }
  }

  return (
    <div className={cn("space-y-1.5", className)}>
      <div
        className={cn(
          "flex min-h-9 w-full flex-wrap items-center gap-1.5 rounded-md border border-input bg-transparent px-2 py-1.5 shadow-xs transition-[color,box-shadow] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50 dark:bg-input/30",
          (error || rest["aria-invalid"]) && "border-destructive",
        )}
      >
        {readOnlyChips?.map((chip) => (
          <span
            key={`ro-${chip}`}
            title={t("chips.automatic")}
            className="inline-flex items-center rounded-md border border-dashed bg-muted/50 px-1.5 py-0.5 font-mono text-xs text-muted-foreground"
          >
            {chip}
          </span>
        ))}
        {value.map((chip, i) => (
          <span
            key={`${chip}-${i}`}
            className="inline-flex items-center gap-1 rounded-md bg-secondary px-1.5 py-0.5 text-xs font-medium text-secondary-foreground"
          >
            {chip}
            <button
              type="button"
              aria-label={t("chips.remove", { value: chip })}
              className="rounded-sm text-muted-foreground hover:text-foreground"
              onClick={() => onChange(value.filter((_, j) => j !== i))}
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        <input
          id={id}
          type={inputType}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            if (error) setError(null);
          }}
          onKeyDown={onKeyDown}
          onBlur={() => draft.trim() && commit(draft)}
          placeholder={value.length ? undefined : (placeholder ?? t("chips.placeholder"))}
          className="min-w-[8rem] flex-1 bg-transparent px-1 text-sm outline-none placeholder:text-muted-foreground"
        />
      </div>
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  );
}
