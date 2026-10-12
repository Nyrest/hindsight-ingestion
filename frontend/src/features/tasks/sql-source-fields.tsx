import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { Tabs } from "radix-ui";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage } from "@/lib/api";
import type { SQLPreviewRequest } from "@/lib/types";

export function SQLSourceFields({ credentialId, config, onChange, errors }: {
  credentialId: string;
  config: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
  errors: Record<string, string>;
}) {
  const { t } = useTranslation();
  const query = typeof config.query === "string" ? config.query : "";
  const documentMode: SQLPreviewRequest["documentMode"] = config.documentMode === "query" ? "query" : "rows";
  const signature = JSON.stringify([credentialId, query, documentMode]);
  const [requested, setRequested] = useState<string | null>(null);
  const active = requested === signature;
  const preview = useQuery({
    queryKey: ["sql-preview", credentialId, query, documentMode],
    queryFn: ({ signal }) => api.previewSQL(credentialId, { query, documentMode }, signal),
    enabled: active && !!credentialId && !!query.trim(),
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });

  function change(key: string, value: string) {
    setRequested(null);
    onChange(key, value);
  }

  return (
    <div className="min-w-0 space-y-4">
      <Tabs.Root value={documentMode} onValueChange={(value) => change("documentMode", value)}>
        <Tabs.List aria-label={t("sql.mode")} className="grid grid-cols-2 gap-1 rounded-lg bg-muted p-1">
          {(["rows", "query"] as const).map((mode) => (
            <Tabs.Trigger key={mode} value={mode} className="rounded-md px-2 py-2 text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-background data-[state=active]:shadow-sm">
              {t(`sql.${mode}`)}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="rows" className="space-y-2 pt-3 text-xs text-muted-foreground">
          <p>{t("sql.rowsHint")}</p>
          <pre className="overflow-x-auto rounded-md border bg-muted/30 p-3 font-mono text-xs">{"SELECT id AS __id, body AS __content,\n       context AS __context FROM articles"}</pre>
        </Tabs.Content>
        <Tabs.Content value="query" className="pt-3 text-xs text-muted-foreground">
          <p>{t("sql.queryHint")}</p>
        </Tabs.Content>
      </Tabs.Root>
      {errors.documentMode && <p className="text-sm text-destructive">{errors.documentMode}</p>}
      <FieldRow label={t("sql.queryLabel")} htmlFor="sql-query" required error={errors.query}>
        <Textarea id="sql-query" rows={8} className="font-mono text-xs" spellCheck={false} value={query} onChange={(event) => change("query", event.target.value)} aria-invalid={!!errors.query} placeholder="SELECT …" />
      </FieldRow>
      <p className="text-xs text-muted-foreground">{t("sql.limits")}</p>
      <div className="space-y-2">
        <Button type="button" variant="outline" disabled={!credentialId || !query.trim() || (active && preview.isFetching)} onClick={() => {
          if (active) void preview.refetch();
          else setRequested(signature);
        }}>
          {active && preview.isFetching && <Loader2 className="animate-spin" />}
          {t("sql.preview")}
        </Button>
        <p className="text-xs text-muted-foreground">{t("sql.previewHint")}</p>
      </div>
      {active && preview.error && <Alert variant="destructive"><AlertDescription>{errorMessage(preview.error)}</AlertDescription></Alert>}
      {active && preview.data && (
        <div className="space-y-3 rounded-lg border p-3 text-xs">
          <p className="break-all"><span className="font-medium">{t("sql.columns")}: </span>{preview.data.columns.join(", ")}</p>
          {preview.data.truncated && <p className="text-amber-700 dark:text-amber-400">{t("sql.truncated")}</p>}
          {!preview.data.documents.length && <p className="text-muted-foreground">{t("sql.empty")}</p>}
          <div className="max-h-80 space-y-3 overflow-auto">
            {preview.data.documents.map((doc) => (
              <div key={doc.id} className="space-y-1 rounded-md bg-muted/40 p-3">
                {doc.context && <p className="whitespace-pre-wrap break-words text-muted-foreground">{t("sql.context")}: {doc.context}</p>}
                <pre className="whitespace-pre-wrap break-words font-mono">{doc.content}</pre>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
