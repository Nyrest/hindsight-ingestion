import GoogleDrive from "@thesvg/react/google-drive";
import MicrosoftOneDrive from "@thesvg/react/microsoft-onedrive";
import Notion from "@thesvg/react/notion";
import Siyuan from "@thesvg/react/siyuan";
import Cloudflare from "@thesvg/react/cloudflare";
import MySQL from "@thesvg/react/mysql";
import PostgreSQL from "@thesvg/react/postgresql";
import SQLite from "@thesvg/react/sqlite";
import Steam from "@thesvg/react/steam";
import { typeIcon } from "@/lib/connectors";
import { cn } from "@/lib/utils";

function brandIcon(type: string) {
  switch (type) {
    case "steam":
      return <Steam aria-hidden="true" className="size-4 shrink-0 text-foreground" variant="mono" />;
    case "cloudflare_crawl":
      return <Cloudflare aria-hidden="true" className="size-4 shrink-0" />;
    case "sql_mysql":
      return <MySQL aria-hidden="true" className="size-4 shrink-0" variant="wordmark" />;
    case "sql_postgres":
      return <PostgreSQL aria-hidden="true" className="size-4 shrink-0" />;
    case "sql_sqlite":
      return <SQLite aria-hidden="true" className="size-4 shrink-0" />;
    case "notion":
      return <Notion aria-hidden="true" className="size-4 shrink-0" />;
    case "siyuan":
      return <Siyuan aria-hidden="true" className="size-4 shrink-0" />;
    case "google_drive":
      return <GoogleDrive aria-hidden="true" className="size-4 shrink-0" />;
    case "onedrive":
      return <MicrosoftOneDrive aria-hidden="true" className="size-4 shrink-0" />;
    default:
      return undefined;
  }
}

export function TypeIcon({ type, className, boxed }: { type: string; className?: string; boxed?: boolean }) {
  const Icon = typeIcon(type);
  const icon = brandIcon(type) ?? <Icon className="size-4 shrink-0" />;

  if (!boxed) return <span className={cn("inline-flex shrink-0", className)}>{icon}</span>;
  return (
    <span
      className={cn(
        "inline-flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted/60 text-foreground/80",
        className,
      )}
    >
      {icon}
    </span>
  );
}
