import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import type { CredentialStatus, RunStatus } from "@/lib/types";

type Tone = "green" | "blue" | "red" | "amber" | "gray";

const TONE_CLASSES: Record<Tone, string> = {
  green: "bg-emerald-500/10 text-emerald-700 ring-emerald-600/20 dark:text-emerald-400 dark:ring-emerald-400/25",
  blue: "bg-sky-500/10 text-sky-700 ring-sky-600/20 dark:text-sky-400 dark:ring-sky-400/25",
  red: "bg-red-500/10 text-red-700 ring-red-600/20 dark:text-red-400 dark:ring-red-400/25",
  amber: "bg-amber-500/10 text-amber-700 ring-amber-600/25 dark:text-amber-400 dark:ring-amber-400/25",
  gray: "bg-muted text-muted-foreground ring-border",
};

const DOT_CLASSES: Record<Tone, string> = {
  green: "bg-emerald-500",
  blue: "bg-sky-500",
  red: "bg-red-500",
  amber: "bg-amber-500",
  gray: "bg-muted-foreground/60",
};

export function ToneBadge({
  tone,
  pulse,
  children,
  className,
  title,
}: {
  tone: Tone;
  pulse?: boolean;
  children: React.ReactNode;
  className?: string;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ring-1 ring-inset",
        TONE_CLASSES[tone],
        className,
      )}
    >
      <span className="relative flex size-1.5">
        {pulse && (
          <span className={cn("absolute inline-flex h-full w-full animate-ping rounded-full opacity-75", DOT_CLASSES[tone])} />
        )}
        <span className={cn("relative inline-flex size-1.5 rounded-full", DOT_CLASSES[tone])} />
      </span>
      {children}
    </span>
  );
}

const RUN_TONES: Record<RunStatus, Tone> = {
  succeeded: "green",
  running: "blue",
  waiting_operations: "blue",
  failed: "red",
  interrupted: "amber",
  cancelled: "gray",
  pending: "gray",
};

export function RunStatusBadge({ status, className }: { status: RunStatus; className?: string }) {
  const { t } = useTranslation();
  const tone = RUN_TONES[status] ?? "gray";
  return (
    <ToneBadge tone={tone} pulse={status === "running" || status === "waiting_operations"} className={className}>
      {t(`runStatus.${status}`, { defaultValue: status })}
    </ToneBadge>
  );
}

const CRED_TONES: Record<CredentialStatus, Tone> = {
  active: "green",
  reauth_required: "amber",
  pending_oauth: "blue",
  error: "red",
};

export function CredentialStatusBadge({
  status,
  message,
  className,
}: {
  status: CredentialStatus;
  message?: string;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <ToneBadge tone={CRED_TONES[status] ?? "gray"} className={className} title={message || undefined}>
      {t(`credentialStatus.${status}`, { defaultValue: status })}
    </ToneBadge>
  );
}
