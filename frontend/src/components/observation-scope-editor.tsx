import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { FieldRow } from "@/components/field-row";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import type { ObservationScope, ScopeTag } from "@/lib/types";

const sameTag = (a: ScopeTag, b: ScopeTag) =>
  a.kind === b.kind && a.value === b.value;

function ScopeTagPicker({
  tags,
  onChange,
  suggestions,
  credentialId,
  bankId,
}: {
  tags: ScopeTag[];
  onChange: (tags: ScopeTag[]) => void;
  suggestions: string[];
  credentialId?: string;
  bankId?: string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 200);
    return () => clearTimeout(timer);
  }, [search]);
  const remote = useQuery({
    queryKey: ["scope-tags", credentialId, bankId, query],
    queryFn: () => api.tags(credentialId!, bankId!, query ? `*${query}*` : ""),
    enabled: open && !!credentialId && !!bankId,
    staleTime: 60_000,
    retry: false,
  });
  const label = (tag: ScopeTag) =>
    tag.kind === "dynamic"
      ? t(`observationScope.dynamic.${tag.value}`)
      : tag.value;
  const options: ScopeTag[] = [
    ...["task", "source", "source_group"].map((value) => ({
      kind: "dynamic" as const,
      value,
    })),
    ...Array.from(
      new Set([
        "ingestion",
        ...suggestions,
        ...(remote.data?.items.map((tag) => tag.tag) ?? []),
      ]),
    ).map((value) => ({ kind: "literal" as const, value })),
  ];
  const visible = options.filter(
    (option) =>
      !tags.some((tag) => sameTag(tag, option)) &&
      label(option).toLowerCase().includes(search.trim().toLowerCase()),
  );
  const literal: ScopeTag = { kind: "literal", value: search.trim() };
  const canCreate =
    !!literal.value &&
    !options.some((tag) => sameTag(tag, literal)) &&
    !tags.some((tag) => sameTag(tag, literal));
  function add(tag: ScopeTag) {
    onChange([...tags, tag]);
    setSearch("");
    setOpen(false);
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      {tags.map((tag) => (
        <span
          key={`${tag.kind}:${tag.value}`}
          className="inline-flex max-w-full items-center gap-1 rounded-md bg-secondary px-2 py-1 text-sm"
        >
          {tag.kind === "dynamic" && (
            <RefreshCw className="size-3.5 shrink-0" aria-hidden="true" />
          )}
          <span className="break-all">{label(tag)}</span>
          <button
            type="button"
            aria-label={t("chips.remove", { value: label(tag) })}
            onClick={() =>
              onChange(tags.filter((existing) => !sameTag(existing, tag)))
            }
          >
            <X className="size-3.5" />
          </button>
        </span>
      ))}
      <Popover
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (next) setSearch("");
        }}
      >
        <PopoverTrigger asChild>
          <Button type="button" variant="outline" size="sm">
            <Plus />
            {t("observationScope.addTag")}
          </Button>
        </PopoverTrigger>
        <PopoverContent
          className="w-[min(22rem,calc(100vw-2rem))] p-0"
          align="start"
        >
          <Command shouldFilter={false}>
            <CommandInput
              value={search}
              onValueChange={setSearch}
              placeholder={t("observationScope.searchTags")}
            />
            <CommandList>
              {visible.map((tag) => (
                <CommandItem
                  key={`${tag.kind}:${tag.value}`}
                  value={`${tag.kind}:${tag.value}`}
                  onSelect={() => add(tag)}
                >
                  {label(tag)}
                </CommandItem>
              ))}
              {canCreate && (
                <CommandItem
                  value={`create:${literal.value}`}
                  onSelect={() => add(literal)}
                >
                  {t("observationScope.useTag", { tag: literal.value })}
                </CommandItem>
              )}
            </CommandList>
            {remote.isError && (
              <p className="px-3 py-2 text-sm text-muted-foreground">
                {t("observationScope.tagsUnavailable")}
              </p>
            )}
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  );
}

export function ObservationScopeEditor({
  value,
  onChange,
  mode,
  onModeChange,
  globalValue,
  suggestions = [],
  credentialId,
  bankId,
  showFileLimitation,
  error,
}: {
  value: ObservationScope;
  onChange: (scope: ObservationScope) => void;
  mode?: "global" | "override";
  onModeChange?: (mode: "global" | "override") => void;
  globalValue?: ObservationScope;
  suggestions?: string[];
  credentialId?: string;
  bankId?: string;
  showFileLimitation?: boolean;
  error?: string;
}) {
  const { t } = useTranslation();
  const id = useId();
  const inherited = mode === "global";
  const effective = inherited ? globalValue : value;
  return (
    <div className="space-y-4">
      <FieldRow label={t("observationScope.rule")} htmlFor={id} error={error}>
        <Select
          value={inherited ? "global" : value.rule}
          onValueChange={(rule) => {
            // The native select can emit an empty value during form reset.
            if (!rule) return;
            if (rule === "global") {
              onModeChange?.("global");
              return;
            }
            onModeChange?.("override");
            onChange({
              rule: rule as ObservationScope["rule"],
              scopes:
                rule === "custom"
                  ? value.scopes.length
                    ? value.scopes
                    : [[]]
                  : [],
            });
          }}
        >
          <SelectTrigger id={id} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {onModeChange && (
              <SelectItem value="global">
                {t("proxy.global")}
                {globalValue
                  ? ` (${t(`observationScope.rules.${globalValue.rule}`)})`
                  : ""}
              </SelectItem>
            )}
            {(
              [
                "combined",
                "shared",
                "per_tag",
                "all_combinations",
                "custom",
              ] as const
            ).map((rule) => (
              <SelectItem key={rule} value={rule}>
                {t(`observationScope.rules.${rule}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </FieldRow>
      {effective?.rule === "all_combinations" && (
        <Alert className="border-amber-500/30 bg-amber-500/8 text-amber-800 dark:text-amber-300">
          <AlertTriangle />
          <AlertDescription>
            {t("observationScope.exponentialWarning")}
          </AlertDescription>
        </Alert>
      )}
      {showFileLimitation && (
        <p className="text-sm text-muted-foreground">
          {t("observationScope.fileLimitation")}
        </p>
      )}
      {!inherited && value.rule === "custom" && (
        <div className="space-y-3">
          {value.scopes.map((tags, index) => (
            <div key={index} className="space-y-2 rounded-md border p-3">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium">
                  {t("observationScope.group", { number: index + 1 })}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t("observationScope.removeGroup", {
                    number: index + 1,
                  })}
                  onClick={() =>
                    onChange({
                      ...value,
                      scopes: value.scopes.filter((_, i) => i !== index),
                    })
                  }
                >
                  <Trash2 />
                </Button>
              </div>
              <ScopeTagPicker
                tags={tags}
                suggestions={suggestions}
                credentialId={credentialId}
                bankId={bankId}
                onChange={(next) =>
                  onChange({
                    ...value,
                    scopes: value.scopes.map((group, i) =>
                      i === index ? next : group,
                    ),
                  })
                }
              />
            </div>
          ))}
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() =>
              onChange({ ...value, scopes: [...value.scopes, []] })
            }
          >
            <Plus />
            {t("observationScope.addGroup")}
          </Button>
        </div>
      )}
    </div>
  );
}
