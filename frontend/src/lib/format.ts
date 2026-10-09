import cronstrue from "./cron-locales";
import i18n from "./i18n";

function locale(): string {
  return i18n.language || "en";
}

export function parseDate(value: string | null | undefined): Date | null {
  if (!value) return null;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

const UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

export function formatRelative(value: string | Date | null | undefined, now: number = Date.now()): string {
  const d = value instanceof Date ? value : parseDate(value);
  if (!d) return "—";
  const diffSec = Math.round((d.getTime() - now) / 1000);
  const abs = Math.abs(diffSec);
  const rtf = new Intl.RelativeTimeFormat(locale(), { numeric: "auto" });
  if (abs < 10) return rtf.format(0, "second");
  for (const [unit, secs] of UNITS) {
    if (abs >= secs || unit === "second") {
      return rtf.format(Math.round(diffSec / secs), unit);
    }
  }
  return rtf.format(diffSec, "second");
}

export function formatDateTime(value: string | Date | null | undefined, timeZone?: string): string {
  const d = value instanceof Date ? value : parseDate(value);
  if (!d) return "—";
  try {
    return new Intl.DateTimeFormat(locale(), {
      dateStyle: "medium",
      timeStyle: "medium",
      timeZone,
    }).format(d);
  } catch {
    return d.toLocaleString();
  }
}

export function formatTime(value: string | null | undefined, timeZone?: string): string {
  const d = parseDate(value);
  if (!d) return "—";
  return new Intl.DateTimeFormat(locale(), { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false, timeZone }).format(d);
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms) || ms < 0) return "—";
  const totalSec = Math.floor(ms / 1000);
  if (totalSec < 1) return `${ms}ms`;
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  const s = totalSec % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

export function runDuration(startedAt: string | null, finishedAt: string | null, now: number = Date.now()): number | null {
  const s = parseDate(startedAt);
  if (!s) return null;
  const f = parseDate(finishedAt);
  return (f ? f.getTime() : now) - s.getTime();
}

export function formatNumber(n: number | null | undefined, compact = false): string {
  if (n === null || n === undefined) return "—";
  return new Intl.NumberFormat(locale(), compact ? { notation: "compact", maximumFractionDigits: 1 } : {}).format(n);
}

export function describeCron(expr: string): string | null {
  const trimmed = expr.trim();
  if (!trimmed) return null;
  try {
    return cronstrue.toString(trimmed, {
      use24HourTimeFormat: true,
      throwExceptionOnParseError: true,
      verbose: false,
      locale: locale().replaceAll("-", "_"),
    });
  } catch {
    return null;
  }
}

/** Converts an ISO instant to a datetime-local value in the application timezone. */
export function isoToLocalInput(iso: string, timeZone: string): string {
  const d = parseDate(iso);
  if (!d) return "";
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone, year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  }).formatToParts(d);
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((p) => p.type === type)?.value;
  return `${part("year")}-${part("month")}-${part("day")}T${part("hour")}:${part("minute")}`;
}

export function localInputToIso(value: string, timeZone: string): string {
  if (!value) return "";
  const wallTime = Date.parse(`${value}Z`);
  if (Number.isNaN(wallTime)) return "";
  let instant = wallTime;
  // Resolve the zone's offset at the chosen instant, including daylight saving changes.
  for (let i = 0; i < 3; i++) {
    const local = isoToLocalInput(new Date(instant).toISOString(), timeZone);
    const offset = Date.parse(`${local}Z`) - instant;
    instant = wallTime - offset;
  }
  const iso = new Date(instant).toISOString();
  return isoToLocalInput(iso, timeZone) === value ? iso : "";
}

export function getTimeZones(): string[] {
  try {
    const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
    const zones = intl.supportedValuesOf?.("timeZone") ?? [];
    if (zones.length) return zones.includes("UTC") ? zones : ["UTC", ...zones];
  } catch {
    /* not supported */
  }
  return ["UTC"];
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
