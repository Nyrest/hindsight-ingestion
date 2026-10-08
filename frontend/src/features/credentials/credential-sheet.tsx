import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";
import { FieldRow } from "@/components/field-row";
import {
  defaultConfig,
  mergeConfig,
  normalizeConfig,
  SchemaField,
  validateConfig,
} from "@/components/schema-field";
import { TypeIcon } from "@/components/type-icon";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { errorMessage, isApiError } from "@/lib/api";
import { findCredentialType, useConnectors } from "@/lib/connectors";
import { providerCopy } from "@/lib/provider-copy";
import type { Credential } from "@/lib/types";
import { cn } from "@/lib/utils";
import { useCreateCredential, useUpdateCredential } from "./api";
import { CustomHeadersEditor, type CustomHeadersState } from "./custom-headers-editor";

const schema = z.object({
  name: z.string().trim().min(1, "validation.required").max(200, "validation.tooLong"),
  type: z.string().min(1, "validation.required"),
});
type FormValues = z.infer<typeof schema>;

function omitKey<T>(obj: Record<string, T>, key: string): Record<string, T> {
  const next = { ...obj };
  delete next[key];
  return next;
}

export function CredentialSheet({
  open,
  onOpenChange,
  credential,
  defaultType,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** When set, the sheet edits this credential. */
  credential?: Credential | null;
  defaultType?: string;
  onSaved?: (c: Credential) => void;
}) {
  const { t } = useTranslation();
  const connectors = useConnectors();
  const editing = !!credential;
  const create = useCreateCredential();
  const update = useUpdateCredential();

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", type: "" },
  });
  const type = form.watch("type");
  const typeSpec = findCredentialType(connectors.data, type);

  const [config, setConfig] = useState<Record<string, unknown>>({});
  const [configErrors, setConfigErrors] = useState<Record<string, string>>({});
  const [headers, setHeaders] = useState<CustomHeadersState>({ headers: {}, error: null });
  const [headersKey, setHeadersKey] = useState(0);
  const [formError, setFormError] = useState<string | null>(null);

  // Reset whenever opened.
  useEffect(() => {
    if (!open) return;
    const initialType = credential?.type ?? defaultType ?? "";
    form.reset({ name: credential?.name ?? "", type: initialType });
    setConfigErrors({});
    setFormError(null);
    setHeaders({ headers: credential?.customHeaders ?? {}, error: null });
    setHeadersKey((k) => k + 1);
  }, [open, credential, defaultType]);

  // Initialize config once type spec is known.
  useEffect(() => {
    if (!open || !typeSpec) return;
    if (credential && credential.type === typeSpec.type) setConfig(mergeConfig(typeSpec.fields, credential.config));
    else setConfig(defaultConfig(typeSpec.fields));
    setConfigErrors({});
  }, [open, typeSpec, credential]);

  const tr = (key: string, opts?: Record<string, unknown>) => t(key, opts ?? {}) as string;
  const pending = create.isPending || update.isPending;

  async function onSubmit(values: FormValues) {
    setFormError(null);
    if (!typeSpec) return;
    const errs = validateConfig(typeSpec.fields, config, tr);
    setConfigErrors(errs);
    if (Object.keys(errs).length || headers.error) return;
    const body = {
      name: values.name.trim(),
      config: normalizeConfig(typeSpec.fields, config),
      customHeaders: headers.headers,
    };
    try {
      const saved = editing
        ? await update.mutateAsync({ id: credential!.id, body })
        : await create.mutateAsync({ ...body, type: values.type });
      toast.success(editing ? t("credentials.toast.updated") : t("credentials.toast.created"), {
        description: saved.name,
      });
      onSaved?.(saved);
      onOpenChange(false);
    } catch (e) {
      if (isApiError(e) && Object.keys(e.fields).length) {
        const cfgErrs: Record<string, string> = {};
        for (const [k, msg] of Object.entries(e.fields)) {
          if (k === "name") form.setError("name", { message: msg });
          else if (k === "customHeaders" || k.startsWith("customHeaders.")) setFormError(msg);
          else cfgErrs[k.replace(/^config\./, "")] = msg;
        }
        setConfigErrors(cfgErrs);
      }
      setFormError((prev) => prev ?? errorMessage(e));
    }
  }

  const types = connectors.data?.credentialTypes ?? [];
  const sortedTypes = useMemo(() => [...types].sort((a, b) => a.name.localeCompare(b.name)), [types]);

  return (
    <Sheet open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <SheetContent className="flex w-full flex-col gap-0 p-0 sm:max-w-xl">
        <SheetHeader className="border-b px-6 py-4">
          <SheetTitle>{editing ? t("credentials.editTitle") : t("credentials.createTitle")}</SheetTitle>
          <SheetDescription>
            {editing ? t("credentials.editDescription") : t("credentials.createDescription")}
          </SheetDescription>
        </SheetHeader>

        <form
          id="credential-form"
          onSubmit={(e) => {
            // The sheet may be portaled inside another <form> (task editor); React bubbles through portals.
            e.stopPropagation();
            void form.handleSubmit(onSubmit)(e);
          }}
          className="flex-1 space-y-6 overflow-y-auto px-6 py-5"
          noValidate
        >
          {/* Type picker */}
          <FieldRow label={t("credentials.fields.type")} required error={form.formState.errors.type && t("validation.required")}>
            {connectors.isLoading ? (
              <div className="grid grid-cols-2 gap-2">
                {Array.from({ length: 6 }).map((_, i) => (
                  <Skeleton key={i} className="h-14" />
                ))}
              </div>
            ) : editing ? (
              <div className="flex items-center gap-3 rounded-md border px-3 py-2.5">
                <TypeIcon type={type} boxed />
                <div>
                  <div className="text-sm font-medium">{typeSpec?.name ?? type}</div>
                  <div className="text-xs text-muted-foreground">{typeSpec?.description}</div>
                </div>
              </div>
            ) : (
              <Controller
                control={form.control}
                name="type"
                render={({ field }) => (
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                    {sortedTypes.map((ct) => {
                      const active = field.value === ct.type;
                      return (
                        <button
                          key={ct.type}
                          type="button"
                          onClick={() => field.onChange(ct.type)}
                          className={cn(
                            "flex items-start gap-3 rounded-md border px-3 py-2.5 text-left transition-colors",
                            active ? "border-primary bg-primary/5 ring-1 ring-primary" : "hover:bg-accent/60",
                          )}
                        >
                          <TypeIcon type={ct.type} boxed />
                          <span className="min-w-0">
                            <span className="flex items-center gap-1.5 text-sm font-medium">
                              {providerCopy(ct.name, t)}
                              {ct.oauth && (
                                <Badge variant="outline" className="h-4 px-1 text-[10px]">
                                  OAuth
                                </Badge>
                              )}
                            </span>
                            <span className="line-clamp-2 text-xs text-muted-foreground">{providerCopy(ct.description, t)}</span>
                          </span>
                        </button>
                      );
                    })}
                  </div>
                )}
              />
            )}
          </FieldRow>

          {typeSpec && (
            <>
              <FieldRow
                label={t("credentials.fields.name")}
                htmlFor="cred-name"
                required
                error={form.formState.errors.name?.message && t(form.formState.errors.name.message)}
              >
                <Input
                  id="cred-name"
                  placeholder={t("credentials.namePlaceholder", { type: providerCopy(typeSpec.name, t) })}
                  aria-invalid={!!form.formState.errors.name}
                  {...form.register("name")}
                />
              </FieldRow>

              {typeSpec.fields.length > 0 && (
                <div className="space-y-4">
                  {typeSpec.fields.map((f) => (
                    <SchemaField
                      key={f.key}
                      field={f}
                      idPrefix="cred"
                      value={config[f.key]}
                      editing={editing}
                      error={configErrors[f.key]}
                      onChange={(v) => {
                        setConfig((c) => ({ ...c, [f.key]: v }));
                        if (configErrors[f.key]) setConfigErrors((prev) => omitKey(prev, f.key));
                      }}
                    />
                  ))}
                </div>
              )}

              {typeSpec.oauth && (
                <Alert>
                  <AlertDescription>{t("credentials.oauthHint")}</AlertDescription>
                </Alert>
              )}

              <Separator />

              <CustomHeadersEditor
                key={headersKey}
                initial={credential?.customHeaders ?? {}}
                onChange={setHeaders}
              />
            </>
          )}

          {formError && (
            <Alert variant="destructive">
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
        </form>

        <SheetFooter className="flex-row justify-end gap-2 border-t px-6 py-4">
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" form="credential-form" disabled={pending || !typeSpec || !!headers.error}>
            {pending && <Loader2 className="animate-spin" />}
            {editing ? t("common.save") : t("common.create")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
