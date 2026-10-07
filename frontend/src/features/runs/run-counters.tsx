import { useTranslation } from "react-i18next";
import { formatNumber } from "@/lib/format";
import type { Run } from "@/lib/types";
import { cn } from "@/lib/utils";

export const COUNTERS = [
  { key: "discoveredCount", short: "D", tone: "text-foreground" },
  { key: "createdCount", short: "+", tone: "text-emerald-600 dark:text-emerald-400" },
  { key: "updatedCount", short: "~", tone: "text-sky-600 dark:text-sky-400" },
  { key: "deletedCount", short: "−", tone: "text-orange-600 dark:text-orange-400" },
  { key: "unchangedCount", short: "=", tone: "text-muted-foreground" },
  { key: "skippedCount", short: "↷", tone: "text-muted-foreground" },
  { key: "failedCount", short: "!", tone: "text-red-600 dark:text-red-400" },
] as const satisfies ReadonlyArray<{ key: keyof Run; short: string; tone: string }>;

/** Compact one-line counters for tables. Zero values are dimmed. */
export function RunCountersCompact({ run, className }: { run: Run; className?: string }) {
  const { t } = useTranslation();
  return (
    <div className={cn("flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-xs tabular-nums", className)}>
      {COUNTERS.map((c) => {
        const v = run[c.key];
        return (
          <span
            key={c.key}
            title={`${t(`runs.counters.${c.key}`)}: ${v}`}
            aria-label={`${t(`runs.counters.${c.key}`)}: ${v}`}
            className={cn(v ? c.tone : "text-muted-foreground/45")}
          >
            <span className="opacity-70">{c.short}</span>
            {formatNumber(v, true)}
          </span>
        );
      })}
    </div>
  );
}

/** Grid of counter tiles for run detail. */
export function RunCountersGrid({ run }: { run: Run }) {
  const { t } = useTranslation();
  return (
    <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-7">
      {COUNTERS.map((c) => (
        <div key={c.key} className="rounded-lg border bg-card px-3 py-2.5">
          <div className="text-xs text-muted-foreground">{t(`runs.counters.${c.key}`)}</div>
          <div className={cn("mt-0.5 text-xl font-semibold tabular-nums", run[c.key] ? c.tone : "text-muted-foreground")}>
            {formatNumber(run[c.key])}
          </div>
        </div>
      ))}
    </div>
  );
}
