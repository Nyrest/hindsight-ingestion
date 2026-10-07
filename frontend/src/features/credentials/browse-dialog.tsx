import {
  Brain,
  Check,
  ChevronRight,
  Database,
  File,
  FileText,
  Folder,
  Globe,
  HardDrive,
  Loader2,
  NotebookPen,
  RefreshCw,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { EmptyState } from "@/components/empty-state";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { errorMessage } from "@/lib/api";
import type { Breadcrumb, BrowseItem } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useBrowse } from "./api";

const KIND_ICONS: Record<string, LucideIcon> = {
  folder: Folder,
  file: File,
  data_source: Database,
  notebook: NotebookPen,
  bucket: Database,
  bank: Brain,
  drive: HardDrive,
  site: Globe,
};

export interface BrowseSelection {
  id: string;
  name: string;
  path?: string;
  kind: string;
}

export function BrowseDialog({
  open,
  onOpenChange,
  credentialId,
  kind = "",
  config,
  title,
  onSelect,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  credentialId: string;
  kind?: string;
  config?: Record<string, unknown>;
  title?: string;
  onSelect: (item: BrowseSelection) => void;
}) {
  const { t } = useTranslation();
  const [parentId, setParentId] = useState("");
  const [trail, setTrail] = useState<Breadcrumb[]>([]);
  const [selected, setSelected] = useState<BrowseItem | null>(null);

  useEffect(() => {
    if (open) {
      setParentId("");
      setTrail([]);
      setSelected(null);
    }
  }, [open, credentialId]);

  const q = useBrowse(credentialId, parentId, kind, open, config);
  const crumbs: Breadcrumb[] =
    q.data?.breadcrumbs && q.data.breadcrumbs.length > 0
      ? q.data.breadcrumbs
      : [{ id: "", name: t("browse.root") }, ...trail];

  function enter(item: BrowseItem) {
    setTrail((tr) => [...tr, { id: item.id, name: item.name }]);
    setParentId(item.id);
    setSelected(null);
  }

  function goTo(crumb: Breadcrumb, index: number) {
    setParentId(crumb.id);
    // keep local trail in sync when server breadcrumbs are not provided
    setTrail((tr) => (crumb.id === "" ? [] : tr.slice(0, Math.max(0, index))));
    setSelected(null);
  }

  function confirm() {
    if (!selected) return;
    onSelect({ id: selected.id, name: selected.name, path: selected.path, kind: selected.kind });
    onOpenChange(false);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85svh] flex-col gap-3 sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title ?? t("browse.title")}</DialogTitle>
          <DialogDescription>{t("browse.description")}</DialogDescription>
        </DialogHeader>

        <div className="flex items-center gap-2">
          <nav className="flex min-w-0 flex-1 flex-wrap items-center gap-1 text-sm" aria-label={t("browse.breadcrumbs")}>
            {crumbs.map((c, i) => {
              const last = i === crumbs.length - 1;
              return (
                <span key={`${c.id}-${i}`} className="flex items-center gap-1">
                  {i > 0 && <ChevronRight className="size-3.5 text-muted-foreground" />}
                  <button
                    type="button"
                    disabled={last}
                    onClick={() => goTo(c, i)}
                    className={cn(
                      "max-w-[12rem] truncate rounded px-1 py-0.5",
                      last ? "font-medium" : "text-muted-foreground hover:bg-accent hover:text-foreground",
                    )}
                  >
                    {c.name || t("browse.root")}
                  </button>
                </span>
              );
            })}
          </nav>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={() => q.refetch()}
            aria-label={t("common.refresh")}
            disabled={q.isFetching}
          >
            <RefreshCw className={cn(q.isFetching && "animate-spin")} />
          </Button>
        </div>

        <div className="min-h-[16rem] flex-1 overflow-y-auto rounded-md border">
          {q.isLoading ? (
            <div className="space-y-1 p-2">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-9 w-full" />
              ))}
            </div>
          ) : q.isError ? (
            <div className="p-3">
              <Alert variant="destructive">
                <AlertDescription>{errorMessage(q.error)}</AlertDescription>
              </Alert>
            </div>
          ) : !q.data?.items.length ? (
            <EmptyState icon={Folder} title={t("browse.empty")} className="m-3 border-none" compact />
          ) : (
            <ul className="divide-y">
              {q.data.items.map((item) => {
                const Icon = KIND_ICONS[item.kind] ?? FileText;
                const isSel = selected?.id === item.id;
                return (
                  <li key={item.id}>
                    <div
                      className={cn(
                        "flex items-center gap-2 px-2 py-1.5 text-sm",
                        isSel && "bg-primary/8",
                        !item.selectable && !item.hasChildren && "opacity-60",
                      )}
                    >
                      <button
                        type="button"
                        className={cn(
                          "flex min-w-0 flex-1 items-center gap-2.5 rounded px-1.5 py-1 text-left",
                          item.selectable ? "hover:bg-accent" : "cursor-default",
                        )}
                        onClick={() => item.selectable && setSelected(item)}
                        onDoubleClick={() => item.hasChildren && enter(item)}
                        disabled={!item.selectable && !item.hasChildren}
                      >
                        <span
                          className={cn(
                            "flex size-4 shrink-0 items-center justify-center rounded-full border",
                            isSel ? "border-primary bg-primary text-primary-foreground" : "border-input",
                            !item.selectable && "invisible",
                          )}
                        >
                          {isSel && <Check className="size-3" />}
                        </span>
                        <Icon className="size-4 shrink-0 text-muted-foreground" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate">{item.name}</span>
                          {item.path && (
                            <span className="block truncate font-mono text-[11px] text-muted-foreground">{item.path}</span>
                          )}
                        </span>
                        <span className="hidden text-[11px] text-muted-foreground sm:inline">
                          {t(`browse.kinds.${item.kind}`, { defaultValue: item.kind })}
                        </span>
                      </button>
                      {item.hasChildren && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          onClick={() => enter(item)}
                          aria-label={t("browse.open", { name: item.name })}
                        >
                          <ChevronRight />
                        </Button>
                      )}
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </div>

        <DialogFooter className="items-center gap-2 sm:justify-between">
          <p className="min-w-0 truncate text-xs text-muted-foreground">
            {selected ? (
              <>
                {t("browse.selected")}: <span className="font-medium text-foreground">{selected.path || selected.name}</span>
              </>
            ) : (
              t("browse.hint")
            )}
          </p>
          <div className="flex gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="button" onClick={confirm} disabled={!selected}>
              {q.isFetching && !q.isLoading ? <Loader2 className="animate-spin" /> : null}
              {t("browse.select")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
