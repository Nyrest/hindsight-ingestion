import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2, Save, Settings as SettingsIcon } from "lucide-react";
import { useEffect } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";
import { CopyButton } from "@/components/copy-button";
import { EmptyState } from "@/components/empty-state";
import { FieldRow } from "@/components/field-row";
import { FilePolicySwitches } from "@/components/file-policy-switches";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { useSettings, useUpdateSettings } from "@/features/settings/api";
import { errorMessage, isApiError } from "@/lib/api";

const schema = z.object({
  incrementalSyncEnabled: z.boolean(),
  fullReconcileIntervalHours: z.number({ error: "validation.number" }).int("validation.integer").min(1, "validation.min1"),
  maxFileSizeMB: z.number({ error: "validation.number" }).int("validation.integer").min(1, "validation.min1"),
  filePolicy: z.object({ plainText: z.boolean(), documents: z.boolean(), images: z.boolean(), audios: z.boolean() }),
});
type Values = z.infer<typeof schema>;

export default function SettingsPage() {
  const { t } = useTranslation();
  const settings = useSettings();
  const update = useUpdateSettings();
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      incrementalSyncEnabled: true,
      fullReconcileIntervalHours: 24,
      maxFileSizeMB: 100,
      filePolicy: { plainText: true, documents: true, images: false, audios: false },
    },
  });
  const { errors, isDirty } = form.formState;

  useEffect(() => {
    if (settings.data) {
      const { oauthRedirectUri: _ignored, ...rest } = settings.data;
      void _ignored;
      form.reset(rest);
    }
  }, [settings.data, form]);

  async function onSubmit(v: Values) {
    try {
      const saved = await update.mutateAsync(v);
      const { oauthRedirectUri: _ignored, ...rest } = saved;
      void _ignored;
      form.reset(rest);
      toast.success(t("settings.toast.saved"));
    } catch (e) {
      if (isApiError(e)) {
        for (const [k, msg] of Object.entries(e.fields)) {
          if (k in v) form.setError(k as keyof Values, { message: msg });
        }
      }
      toast.error(t("settings.toast.failed"), { description: errorMessage(e) });
    }
  }

  const errText = (m?: string) => (m ? t(m, { defaultValue: m }) : undefined);

  if (settings.isError) {
    return (
      <>
        <PageHeader title={t("settings.title")} />
        <EmptyState
          icon={SettingsIcon}
          title={t("common.loadFailed")}
          description={errorMessage(settings.error)}
          action={<Button variant="outline" onClick={() => settings.refetch()}>{t("common.retry")}</Button>}
        />
      </>
    );
  }

  return (
    <form onSubmit={form.handleSubmit(onSubmit)} noValidate className="max-w-3xl">
      <PageHeader
        title={t("settings.title")}
        description={t("settings.description")}
        actions={
          <Button type="submit" disabled={!isDirty || update.isPending || settings.isLoading}>
            {update.isPending ? <Loader2 className="animate-spin" /> : <Save />}
            {t("common.saveChanges")}
          </Button>
        }
      />

      {settings.isLoading ? (
        <div className="space-y-6">
          <Skeleton className="h-48" />
          <Skeleton className="h-56" />
          <Skeleton className="h-32" />
        </div>
      ) : (
        <div className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle>{t("settings.sync.title")}</CardTitle>
              <CardDescription>{t("settings.sync.description")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-5">
              <Controller
                control={form.control}
                name="incrementalSyncEnabled"
                render={({ field }) => (
                  <label htmlFor="s-incremental" className="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
                    <span>
                      <span className="block text-sm font-medium">{t("settings.sync.incremental")}</span>
                      <span className="block text-xs text-muted-foreground">{t("settings.sync.incrementalHelp")}</span>
                    </span>
                    <Switch id="s-incremental" checked={field.value} onCheckedChange={field.onChange} />
                  </label>
                )}
              />
              <div className="grid gap-4 sm:grid-cols-2">
                <FieldRow
                  label={t("settings.sync.reconcileInterval")}
                  htmlFor="s-reconcile"
                  help={t("settings.sync.reconcileIntervalHelp")}
                  error={errText(errors.fullReconcileIntervalHours?.message)}
                >
                  <div className="relative">
                    <Input
                      id="s-reconcile"
                      type="number"
                      min={1}
                      className="pr-14"
                      aria-invalid={!!errors.fullReconcileIntervalHours}
                      {...form.register("fullReconcileIntervalHours", { valueAsNumber: true })}
                    />
                    <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">
                      {t("settings.units.hours")}
                    </span>
                  </div>
                </FieldRow>
                <FieldRow
                  label={t("settings.sync.maxFileSize")}
                  htmlFor="s-maxsize"
                  help={t("settings.sync.maxFileSizeHelp")}
                  error={errText(errors.maxFileSizeMB?.message)}
                >
                  <div className="relative">
                    <Input
                      id="s-maxsize"
                      type="number"
                      min={1}
                      className="pr-12"
                      aria-invalid={!!errors.maxFileSizeMB}
                      {...form.register("maxFileSizeMB", { valueAsNumber: true })}
                    />
                    <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">
                      MB
                    </span>
                  </div>
                </FieldRow>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("settings.files.title")}</CardTitle>
              <CardDescription>{t("settings.files.description")}</CardDescription>
            </CardHeader>
            <CardContent>
              <Controller
                control={form.control}
                name="filePolicy"
                render={({ field }) => <FilePolicySwitches value={field.value} onChange={field.onChange} idPrefix="s-fp" />}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("settings.oauth.title")}</CardTitle>
              <CardDescription>{t("settings.oauth.description")}</CardDescription>
            </CardHeader>
            <CardContent>
              <FieldRow label={t("settings.oauth.redirectUri")} htmlFor="s-redirect">
                <div className="flex gap-2">
                  <Input id="s-redirect" readOnly value={settings.data?.oauthRedirectUri ?? ""} className="font-mono text-xs sm:text-xs" onFocus={(e) => e.currentTarget.select()} />
                  <CopyButton value={settings.data?.oauthRedirectUri ?? ""} />
                </div>
              </FieldRow>
            </CardContent>
          </Card>
        </div>
      )}
    </form>
  );
}
