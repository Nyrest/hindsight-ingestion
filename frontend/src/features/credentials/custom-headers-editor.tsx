import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyValueEditor, type KeyValueRow } from "@/components/key-value-editor";
import { Segmented } from "@/components/segmented";
import { Textarea } from "@/components/ui/textarea";
import { MASK } from "@/lib/types";
import {
  headersFromRows,
  parseHeadersJson,
  rowsFromHeaders,
  rowsToJson,
  type HeadersParseResult,
} from "./custom-headers";

type Mode = "ui" | "json";

export interface CustomHeadersState {
  headers: Record<string, string>;
  error: string | null;
}

/**
 * Custom headers editor with a lossless UI | JSON toggle.
 * Uncontrolled internally; reports the parsed headers and a validation error upward.
 * Reset by changing the `key` prop.
 */
export function CustomHeadersEditor({
  initial,
  onChange,
}: {
  initial: Record<string, string>;
  onChange: (state: CustomHeadersState) => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>("ui");
  const [rows, setRows] = useState<KeyValueRow[]>(() => rowsFromHeaders(initial));
  const [jsonText, setJsonText] = useState<string>(() => rowsToJson(rowsFromHeaders(initial)));
  const [switchError, setSwitchError] = useState<string | null>(null);

  const result: HeadersParseResult = useMemo(
    () => (mode === "ui" ? headersFromRows(rows) : parseHeadersJson(jsonText)),
    [mode, rows, jsonText],
  );

  const errorText = result.ok ? null : describe(result);

  function describe(r: Extract<HeadersParseResult, { ok: false }>): string {
    switch (r.errorKey) {
      case "duplicate":
        return t("headers.errors.duplicate", { name: r.detail });
      case "invalidJson":
        return t("headers.errors.invalidJson", { detail: r.detail ?? "" });
      case "notObject":
        return t("headers.errors.notObject");
      case "nonString":
        return t("headers.errors.nonString", { name: r.detail });
      case "emptyName":
        return t("headers.errors.emptyName");
    }
  }

  useEffect(() => {
    onChange({ headers: result.ok ? result.headers : {}, error: errorText });
  }, [result, errorText]);

  function switchMode(next: Mode) {
    setSwitchError(null);
    if (next === "json") {
      // UI → JSON: serialize rows. Blocked if rows have duplicates (JSON can't represent them losslessly).
      const r = headersFromRows(rows);
      if (!r.ok) {
        setSwitchError(describe(r));
        return;
      }
      setJsonText(rowsToJson(rows));
      setMode("json");
    } else {
      const r = parseHeadersJson(jsonText);
      if (!r.ok) {
        setSwitchError(describe(r));
        return;
      }
      setRows(rowsFromHeaders(r.headers));
      setMode("ui");
    }
  }

  const dupIndex = useMemo(() => {
    if (mode !== "ui" || result.ok || result.errorKey !== "duplicate") return {};
    const seen = new Set<string>();
    const errs: Record<number, string> = {};
    rows.forEach((r, i) => {
      const k = r.key.trim().toLowerCase();
      if (!k) return;
      if (seen.has(k)) errs[i] = t("headers.errors.duplicate", { name: r.key.trim() });
      seen.add(k);
    });
    return errs;
  }, [mode, result, rows, t]);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div>
          <p className="text-sm font-medium">{t("headers.title")}</p>
          <p className="text-xs text-muted-foreground">{t("headers.help")}</p>
        </div>
        <Segmented<Mode>
          value={mode}
          onChange={switchMode}
          ariaLabel={t("headers.mode")}
          options={[
            { value: "ui", label: t("headers.ui") },
            { value: "json", label: t("headers.json") },
          ]}
        />
      </div>

      {switchError && <p className="text-xs font-medium text-destructive">{switchError}</p>}

      {mode === "ui" ? (
        <>
          <KeyValueEditor
            rows={rows}
            onChange={(r) => {
              setRows(r);
              setSwitchError(null);
            }}
            rowErrors={dupIndex}
            keyLabel={t("headers.name")}
            valueLabel={t("headers.value")}
            keyPlaceholder="X-Custom-Header"
            valuePlaceholder={t("headers.valuePlaceholder")}
            addLabel={t("headers.add")}
            emptyText={t("headers.empty")}
            monospace
            isSecretValue={(row) => row.value === MASK}
          />
          {errorText && Object.keys(dupIndex).length === 0 && (
            <p className="text-xs text-destructive">{errorText}</p>
          )}
        </>
      ) : (
        <div className="space-y-1.5">
          <Textarea
            value={jsonText}
            onChange={(e) => {
              setJsonText(e.target.value);
              setSwitchError(null);
            }}
            rows={6}
            spellCheck={false}
            aria-invalid={!!errorText}
            className="font-mono text-xs"
            placeholder={'{\n  "X-Custom-Header": "value"\n}'}
          />
          {errorText && <p className="text-xs text-destructive">{errorText}</p>}
        </div>
      )}
      <p className="text-xs text-muted-foreground">{t("headers.maskHint", { mask: MASK })}</p>
    </div>
  );
}
