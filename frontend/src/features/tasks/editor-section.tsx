import { AlertTriangle, Info, Lock, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export function EditorSection({
  id,
  icon: Icon,
  title,
  description,
  actions,
  children,
}: {
  id: string;
  icon: LucideIcon;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section id={`section-${id}`} data-section={id} className="scroll-mt-24 rounded-xl border bg-card shadow-xs">
      <header className="flex flex-col gap-3 border-b px-5 py-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex items-start gap-3">
          <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
            <Icon className="size-4" />
          </span>
          <div className="space-y-0.5">
            <h2 className="text-base font-semibold tracking-tight">{title}</h2>
            {description && <p className="text-sm text-muted-foreground">{description}</p>}
          </div>
        </div>
        {actions && <div className="shrink-0">{actions}</div>}
      </header>
      <div className="space-y-5 px-5 py-5">{children}</div>
    </section>
  );
}

export function Hint({
  tone = "info",
  children,
  className,
}: {
  tone?: "info" | "warning" | "locked";
  children: ReactNode;
  className?: string;
}) {
  const Icon = tone === "warning" ? AlertTriangle : tone === "locked" ? Lock : Info;
  return (
    <div
      className={cn(
        "flex items-start gap-2 rounded-md border px-3 py-2 text-xs",
        tone === "warning"
          ? "border-amber-500/30 bg-amber-500/8 text-amber-800 dark:text-amber-300"
          : tone === "locked"
            ? "bg-muted/50 text-muted-foreground"
            : "border-sky-500/25 bg-sky-500/6 text-sky-800 dark:text-sky-300",
        className,
      )}
    >
      <Icon className="mt-0.5 size-3.5 shrink-0" />
      <div>{children}</div>
    </div>
  );
}
