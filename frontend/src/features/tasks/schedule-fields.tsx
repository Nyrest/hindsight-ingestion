import { AlertCircle, CalendarClock, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { describeCron, formatDateTime, formatRelative } from "@/lib/format";
import { useDebounced, useNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { useValidateCron } from "./api";
import { TimezoneCombobox } from "./timezone-combobox";

const CRON_PRESETS = [
  { key: "every15m", expr: "*/15 * * * *" },
  { key: "every6h", expr: "0 */6 * * *" },
  { key: "daily3", expr: "0 3 * * *" },
  { key: "weeklyMon8", expr: "0 8 * * 1" },
] as const;

export function ScheduleFields({
  cron,
  timezone,
  onCronChange,
  onTimezoneChange,
  cronError,
  timezoneError,
}: {
  cron: string;
  timezone: string;
  onCronChange: (v: string) => void;
  onTimezoneChange: (v: string) => void;
  cronError?: string;
  timezoneError?: string;
}) {
  const { t } = useTranslation();
  const now = useNow(30_000);
  const debouncedCron = useDebounced(cron.trim(), 450);
  const debouncedTz = useDebounced(timezone, 450);
  const validation = useValidateCron(debouncedCron, debouncedTz || "UTC");
  const description = describeCron(cron);
  const pending = cron.trim() !== debouncedCron || validation.isFetching;
  const serverError = validation.data && !validation.data.valid ? validation.data.error : null;
  const requestError = validation.isError ? t("schedule.previewUnavailable") : null;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-2">
        {CRON_PRESETS.map((p) => (
          <Button
            key={p.key}
            type="button"
            size="sm"
            variant={cron.trim() === p.expr ? "secondary" : "outline"}
            className={cn(cron.trim() === p.expr && "ring-1 ring-primary/40")}
            onClick={() => onCronChange(p.expr)}
          >
            {t(`schedule.presets.${p.key}`)}
          </Button>
        ))}
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <FieldRow
          label={t("schedule.cron")}
          htmlFor="task-cron"
          required
          error={cronError ?? serverError ?? undefined}
          help={description ?? t("schedule.cronHelp")}
        >
          <Input
            id="task-cron"
            value={cron}
            onChange={(e) => onCronChange(e.target.value)}
            placeholder="*/15 * * * *"
            spellCheck={false}
            aria-invalid={!!(cronError || serverError)}
            className="font-mono"
          />
        </FieldRow>
        <FieldRow label={t("schedule.timezone")} htmlFor="task-tz" required error={timezoneError}>
          <TimezoneCombobox id="task-tz" value={timezone} onChange={onTimezoneChange} invalid={!!timezoneError} />
        </FieldRow>
      </div>

      <div className="rounded-md border bg-muted/30 px-3 py-2.5">
        <div className="mb-1.5 flex items-center gap-2 text-xs font-medium text-muted-foreground">
          <CalendarClock className="size-3.5" />
          {t("schedule.nextRuns")}
          {pending && debouncedCron && <Loader2 className="size-3 animate-spin" />}
        </div>
        {!cron.trim() ? (
          <p className="text-sm text-muted-foreground">{t("schedule.enterCron")}</p>
        ) : serverError || requestError ? (
          <p className="flex items-center gap-1.5 text-sm text-muted-foreground">
            <AlertCircle className="size-3.5" />
            {serverError ? t("schedule.invalid") : requestError}
          </p>
        ) : validation.data?.nextRuns?.length ? (
          <ul className="space-y-0.5 text-sm">
            {validation.data.nextRuns.slice(0, 3).map((r) => (
              <li key={r} className="flex flex-wrap items-baseline justify-between gap-x-3">
                <span className="font-mono text-xs">{formatDateTime(r, timezone || undefined)}</span>
                <span className="text-xs text-muted-foreground">{formatRelative(r, now)}</span>
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground">{pending ? t("common.loading") : "—"}</p>
        )}
      </div>
    </div>
  );
}
