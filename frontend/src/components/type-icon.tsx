import { typeIcon } from "@/lib/connectors";
import { cn } from "@/lib/utils";

export function TypeIcon({ type, className, boxed }: { type: string; className?: string; boxed?: boolean }) {
  const Icon = typeIcon(type);
  if (!boxed) return <Icon className={cn("size-4 shrink-0", className)} />;
  return (
    <span
      className={cn(
        "inline-flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted/60 text-foreground/80",
        className,
      )}
    >
      <Icon className="size-4" />
    </span>
  );
}
