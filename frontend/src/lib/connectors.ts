import { useQuery } from "@tanstack/react-query";
import {
  Brain,
  Cloud,
  Database,
  FileText,
  HardDrive,
  NotebookPen,
  Plug,
  Server,
  type LucideIcon,
} from "lucide-react";
import { api } from "./api";
import { qk } from "./query";
import type { Connectors, CredentialTypeSpec, SourceSpec } from "./types";

const TYPE_ICONS: Record<string, LucideIcon> = {
  notion: FileText,
  siyuan: NotebookPen,
  s3: Database,
  webdav: Server,
  google_drive: HardDrive,
  onedrive: Cloud,
  hindsight: Brain,
  filesystem: HardDrive,
};

export function typeIcon(type: string | undefined | null): LucideIcon {
  return (type && TYPE_ICONS[type]) || Plug;
}

const FALLBACK_NAMES: Record<string, string> = {
  notion: "Notion",
  siyuan: "SiYuan",
  s3: "S3",
  webdav: "WebDAV",
  google_drive: "Google Drive",
  onedrive: "OneDrive",
  hindsight: "Hindsight",
  filesystem: "File System",
};

export function useConnectors() {
  return useQuery({
    queryKey: qk.connectors,
    queryFn: api.getConnectors,
    staleTime: 10 * 60_000,
  });
}

export function findCredentialType(c: Connectors | undefined, type: string): CredentialTypeSpec | undefined {
  return c?.credentialTypes.find((x) => x.type === type);
}

export function findSource(c: Connectors | undefined, type: string): SourceSpec | undefined {
  return c?.sources.find((x) => x.type === type);
}

export function typeName(c: Connectors | undefined, type: string): string {
  return (
    findCredentialType(c, type)?.name ?? findSource(c, type)?.name ?? FALLBACK_NAMES[type] ?? type
  );
}
