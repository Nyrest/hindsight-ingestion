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
  Search,
  X,
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
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { errorMessage } from "@/lib/api";
import { useDebounced } from "@/lib/hooks";
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

const ROOT_BREADCRUMB_KEYS: Record<string, string> = {
  Root: "browse.root",
  Banks: "browse.memoryBanks",
  Drives: "browse.drives",
  Buckets: "browse.buckets",
  "Data sources": "browse.dataSources",
  Notebooks: "browse.notebooks",
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
  multiple = false,
  selectedIds,
  onSelectMany,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  credentialId: string;
  kind?: string;
  config?: Record<string, unknown>;
  title?: string;
  onSelect: (item: BrowseSelection) => void;
  multiple?: boolean;
  selectedIds?: string[];
  onSelectMany?: (items: BrowseSelection[]) => void;
}) {
  const { t } = useTranslation();
  const [parentId, setParentId] = useState("");
  const [trail, setTrail] = useState<Breadcrumb[]>([]);
  const [selected, setSelected] = useState<BrowseItem | null>(null);
  const [selections, setSelections] = useState<BrowseSelection[]>([]);
  const [search, setSearch] = useState("");
  const query = search.trim();
  const debouncedQuery = useDebounced(query);

  useEffect(() => {
    if (open) {
      setParentId("");
      setTrail([]);
      setSelected(null);
      setSelections((selectedIds ?? []).map((id) => ({ id, name: id, kind: kind || "folder" })));
      setSearch("");
    }
  }, [open, credentialId]);

  const listing = useBrowse(credentialId, { parentId, kind, config }, open);
  const supportsSearch = listing.data?.supportsSearch ?? false;
  const searchResults = useBrowse(
    credentialId,
    { parentId, kind, config, query: debouncedQuery },
    open && supportsSearch && !!query && !!debouncedQuery,
  );
  const q = supportsSearch && query ? searchResults : listing;
  const searchPending = supportsSearch && query !== debouncedQuery;
  const loading = q.isLoading || searchPending;
  const serverCrumbs = listing.data?.breadcrumbs;
  const rootCrumb = serverCrumbs?.[0] ?? { id: "", name: t("browse.root") };
  const crumbs: Breadcrumb[] = trail.length ? [rootCrumb, ...trail] : serverCrumbs?.length ? serverCrumbs : [rootCrumb];
  const searchTerm = query.toLowerCase();
  const matchingItems = q.data?.items.filter((entry) => supportsSearch || !searchTerm ||
    [entry.name, entry.path, entry.id].some((label) => label.toLowerCase().includes(searchTerm))) ?? [];

  function changeSearch(text: string) {
    setSearch(text);
    setSelected(null);
  }

  function enter(item: BrowseItem) {
    setTrail([...crumbs.slice(1), { id: item.id, name: item.name }]);
    setParentId(item.id);
    setSelected(null);
    setSearch("");
  }

  function goTo(crumb: Breadcrumb, index: number) {
    setParentId(crumb.id);
    setTrail(crumbs.slice(1, index + 1));
    setSelected(null);
    setSearch("");
  }

  function confirm() {
    if (multiple) {
      onSelectMany?.(selections);
      onOpenChange(false);
      return;
    }
    if (!selected) return;
    onSelect({ id: selected.id, name: selected.name, path: selected.path, kind: selected.kind });
    onOpenChange(false);
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85svh] flex-col gap-3 overflow-hidden sm:max-w-xl">
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
                    {i === 0 ? t(ROOT_BREADCRUMB_KEYS[c.name] ?? "browse.root") : c.name || t("browse.root")}
                  </button>
                </span>
              );
            })}
          </nav>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={() => {
              setSelected(null);
              void q.refetch();
            }}
            aria-label={t("common.refresh")}
            disabled={q.isFetching || searchPending}
          >
            <RefreshCw className={cn(q.isFetching && "animate-spin")} />
          </Button>
        </div>

        <div className="space-y-1.5">
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="text"
              value={search}
              onChange={(event) => changeSearch(event.target.value)}
              placeholder={t(supportsSearch ? "browse.searchSources" : "browse.searchList")}
              aria-label={t(supportsSearch ? "browse.searchSources" : "browse.searchList")}
              spellCheck={false}
              className="px-9"
            />
            {search && (
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="absolute right-1 top-1/2 -translate-y-1/2"
                onClick={() => changeSearch("")}
                aria-label={t("browse.clearSearch")}
              >
                <X />
              </Button>
            )}
          </div>
          <p className="text-xs text-muted-foreground">{t(supportsSearch ? "browse.searchSourcesHelp" : "browse.searchListHelp")}</p>
        </div>

        <div className="h-80 min-h-0 overflow-y-auto rounded-md border" aria-busy={loading || q.isFetching}>
          {loading ? (
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
          ) : !matchingItems.length ? (
            <EmptyState icon={query ? Search : Folder} title={t(query ? "browse.noResults" : "browse.empty")} className="m-3 border-none" compact />
          ) : (
            <ul className="divide-y">
              {matchingItems.map((item) => {
                const Icon = KIND_ICONS[item.kind] ?? FileText;
                const isSel = multiple ? selections.some((entry) => entry.id === item.id) : selected?.id === item.id;
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
                        onClick={() => {
                          if (!item.selectable) return;
                          if (multiple) {
                            setSelections((current) => isSel ? current.filter((entry) => entry.id !== item.id) : [...current, item]);
                          } else {
                            setSelected(item);
                          }
                        }}
                        onDoubleClick={() => item.hasChildren && enter(item)}
                        aria-pressed={item.selectable ? isSel : undefined}
                        disabled={!item.selectable && !item.hasChildren}
                      >
                        <span
                          className={cn(
                            "flex size-4 shrink-0 items-center justify-center border",
                            multiple ? "rounded" : "rounded-full",
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

        {multiple && (
          <div className="flex max-h-24 flex-wrap gap-1 overflow-y-auto" aria-label={t("browse.selected")}>
            {selections.map((item) => (
              <Button key={item.id} type="button" variant="outline" size="sm" onClick={() => setSelections((current) => current.filter((entry) => entry.id !== item.id))}>
                <span className="max-w-60 truncate">{item.path || item.name}</span>
                <X className="size-3" />
              </Button>
            ))}
          </div>
        )}
        <DialogFooter className="items-center gap-2 sm:justify-between">
          <p className="min-w-0 truncate text-xs text-muted-foreground">
            {multiple ? `${t("browse.selected")}: ${selections.length}` : selected ? (
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
            <Button type="button" onClick={confirm} disabled={(!multiple && !selected) || loading || q.isError}>
              {q.isFetching && !q.isLoading ? <Loader2 className="animate-spin" /> : null}
              {t("browse.select")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
