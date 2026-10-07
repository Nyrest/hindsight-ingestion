import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Simple labelled form row with optional help + error text (non-RHF usage). */
export function FieldRow({
  label,
  htmlFor,
  required,
  help,
  error,
  children,
  className,
  hint,
}: {
  label?: ReactNode;
  htmlFor?: string;
  required?: boolean;
  help?: ReactNode;
  error?: ReactNode;
  children: ReactNode;
  className?: string;
  hint?: ReactNode;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      {label && (
        <div className="flex items-center justify-between gap-2">
          <label htmlFor={htmlFor} className="text-sm leading-none font-medium">
            {label}
            {required && <span className="ml-0.5 text-destructive">*</span>}
          </label>
          {hint}
        </div>
      )}
      {children}
      {help && !error && <p className="text-xs text-muted-foreground">{help}</p>}
      {error && <p className="text-xs font-medium text-destructive">{error}</p>}
    </div>
  );
}
