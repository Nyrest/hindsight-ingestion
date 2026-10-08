import {
  KeyRound,
  Link2,
  Loader2,
  MoreHorizontal,
  Pencil,
  Plug,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { CredentialStatusBadge } from "@/components/status-badge";
import { TypeIcon } from "@/components/type-icon";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  useCredentials,
  useDeleteCredential,
  useOAuthStart,
  useRefreshCredential,
  useTestCredential,
} from "@/features/credentials/api";
import { CredentialSheet } from "@/features/credentials/credential-sheet";
import { errorMessage, isApiError } from "@/lib/api";
import { findCredentialType, typeName, useConnectors } from "@/lib/connectors";
import { providerCopy } from "@/lib/provider-copy";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import type { Credential } from "@/lib/types";

export default function CredentialsPage() {
  const { t } = useTranslation();
  const now = useNow();
  const connectors = useConnectors();
  const creds = useCredentials();
  const test = useTestCredential();
  const refresh = useRefreshCredential();
  const oauth = useOAuthStart();
  const del = useDeleteCredential();

  const [sheet, setSheet] = useState<{ open: boolean; credential: Credential | null }>({ open: false, credential: null });
  const [toDelete, setToDelete] = useState<Credential | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [params, setParams] = useSearchParams();

  // Handle ?oauth=success|error&id=&message= after provider redirect.
  useEffect(() => {
    const status = params.get("oauth");
    if (!status) return;
    if (status === "success") toast.success(t("credentials.oauth.success"));
    else toast.error(t("credentials.oauth.error"), { description: params.get("message") ?? undefined });
    const next = new URLSearchParams(params);
    ["oauth", "id", "message"].forEach((k) => next.delete(k));
    setParams(next, { replace: true });
  }, [params, setParams, t]);

  async function onTest(c: Credential) {
    setBusy(`test-${c.id}`);
    try {
      const res = await test.mutateAsync(c.id);
      if (res.ok) toast.success(t("credentials.test.ok", { name: c.name }), { description: res.message });
      else toast.error(t("credentials.test.failed", { name: c.name }), { description: res.message });
    } catch (e) {
      toast.error(t("credentials.test.failed", { name: c.name }), { description: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  }

  async function onRefresh(c: Credential) {
    setBusy(`refresh-${c.id}`);
    try {
      await refresh.mutateAsync(c.id);
      toast.success(t("credentials.toast.refreshed"));
    } catch (e) {
      toast.error(t("credentials.toast.refreshFailed"), { description: errorMessage(e) });
    } finally {
      setBusy(null);
    }
  }

  async function onOAuth(c: Credential) {
    setBusy(`oauth-${c.id}`);
    try {
      await oauth.mutateAsync(c.id);
    } catch (e) {
      toast.error(t("credentials.oauth.startFailed"), { description: errorMessage(e) });
      setBusy(null);
    }
  }

  async function onDelete() {
    if (!toDelete) return;
    try {
      await del.mutateAsync(toDelete.id);
      toast.success(t("credentials.toast.deleted"), { description: toDelete.name });
    } catch (e) {
      toast.error(
        isApiError(e) && e.status === 409 ? t("credentials.toast.inUse") : t("credentials.toast.deleteFailed"),
        { description: errorMessage(e) },
      );
      throw e;
    }
  }

  const isOAuth = (c: Credential) => !!findCredentialType(connectors.data, c.type)?.oauth;
  const list = creds.data ?? [];

  function actions(c: Credential) {
    const oauthType = isOAuth(c);
    const anyBusy = busy?.endsWith(c.id);
    return (
      <div className="flex items-center justify-end gap-1">
        {oauthType && (c.status !== "active" || !c.oauthConnected) && (
          <Button size="sm" variant="outline" onClick={() => onOAuth(c)} disabled={!!anyBusy}>
            {busy === `oauth-${c.id}` ? <Loader2 className="animate-spin" /> : <Link2 />}
            {c.oauthConnected ? t("credentials.actions.reconnect") : t("credentials.actions.connect")}
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={() => onTest(c)} disabled={!!anyBusy}>
          {busy === `test-${c.id}` ? <Loader2 className="animate-spin" /> : <Plug />}
          <span className="hidden lg:inline">{t("credentials.actions.test")}</span>
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="icon-sm" variant="ghost" aria-label={t("common.moreActions")}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-48">
            <DropdownMenuItem onSelect={() => setSheet({ open: true, credential: c })}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onTest(c)}>
              <Plug /> {t("credentials.actions.test")}
            </DropdownMenuItem>
            {oauthType && (
              <>
                <DropdownMenuItem onSelect={() => onOAuth(c)}>
                  <Link2 /> {c.oauthConnected ? t("credentials.actions.reconnect") : t("credentials.actions.connect")}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => onRefresh(c)} disabled={!c.oauthConnected}>
                  <RefreshCw /> {t("credentials.actions.refresh")}
                </DropdownMenuItem>
              </>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => setToDelete(c)}>
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    );
  }

  function oauthInfo(c: Credential) {
    if (!isOAuth(c)) return <span className="text-muted-foreground">—</span>;
    if (!c.oauthConnected) return <span className="text-muted-foreground">{t("credentials.oauth.notConnected")}</span>;
    if (!c.oauthExpiresAt) return <span>{t("credentials.oauth.connected")}</span>;
    return (
      <span title={formatDateTime(c.oauthExpiresAt)}>
        {t("credentials.oauth.expires", { when: formatRelative(c.oauthExpiresAt, now) })}
      </span>
    );
  }

  return (
    <>
      <PageHeader
        title={t("credentials.title")}
        description={t("credentials.description")}
        actions={
          <Button onClick={() => setSheet({ open: true, credential: null })}>
            <Plus /> {t("credentials.new")}
          </Button>
        }
      />

      {creds.isLoading ? (
        <Card className="gap-0 p-0">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="flex items-center gap-3 border-b p-4 last:border-0">
              <Skeleton className="size-8 rounded-md" />
              <Skeleton className="h-4 w-40" />
              <Skeleton className="ml-auto h-5 w-20" />
            </div>
          ))}
        </Card>
      ) : creds.isError ? (
        <EmptyState
          icon={KeyRound}
          title={t("common.loadFailed")}
          description={errorMessage(creds.error)}
          action={
            <Button variant="outline" onClick={() => creds.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : list.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title={t("credentials.empty.title")}
          description={t("credentials.empty.description")}
          action={
            <Button onClick={() => setSheet({ open: true, credential: null })}>
              <Plus /> {t("credentials.new")}
            </Button>
          }
        />
      ) : (
        <>
          {/* Desktop table */}
          <Card className="hidden gap-0 overflow-hidden p-0 md:block">
            <Table>
              <TableHeader>
                <TableRow className="bg-muted/40 hover:bg-muted/40">
                  <TableHead className="pl-4">{t("credentials.columns.name")}</TableHead>
                  <TableHead>{t("credentials.columns.type")}</TableHead>
                  <TableHead>{t("credentials.columns.status")}</TableHead>
                  <TableHead>{t("credentials.columns.usedBy")}</TableHead>
                  <TableHead>{t("credentials.columns.oauth")}</TableHead>
                  <TableHead className="pr-4 text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((c) => (
                  <TableRow key={c.id}>
                    <TableCell className="pl-4">
                      <div className="flex items-center gap-3">
                        <TypeIcon type={c.type} boxed />
                        <div className="min-w-0">
                          <button
                            type="button"
                            onClick={() => setSheet({ open: true, credential: c })}
                            className="truncate font-medium hover:underline"
                          >
                            {c.name}
                          </button>
                          {c.statusMessage && (
                            <p className="max-w-xs truncate text-xs text-muted-foreground" title={c.statusMessage}>
                              {c.statusMessage}
                            </p>
                          )}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{providerCopy(typeName(connectors.data, c.type), t)}</TableCell>
                    <TableCell>
                      <CredentialStatusBadge status={c.status} message={c.statusMessage} />
                    </TableCell>
                    <TableCell className="tabular-nums">
                      {t("credentials.usedByCount", { count: c.usedByTasks })}
                    </TableCell>
                    <TableCell className="text-sm">{oauthInfo(c)}</TableCell>
                    <TableCell className="pr-4">{actions(c)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>

          {/* Mobile cards */}
          <div className="grid gap-3 md:hidden">
            {list.map((c) => (
              <Card key={c.id} className="gap-3 p-4">
                <div className="flex items-start gap-3">
                  <TypeIcon type={c.type} boxed />
                  <div className="min-w-0 flex-1">
                    <div className="truncate font-medium">{c.name}</div>
                    <div className="text-xs text-muted-foreground">{providerCopy(typeName(connectors.data, c.type), t)}</div>
                  </div>
                  <CredentialStatusBadge status={c.status} message={c.statusMessage} />
                </div>
                {c.statusMessage && <p className="text-xs text-muted-foreground">{c.statusMessage}</p>}
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                  <span>{t("credentials.usedByCount", { count: c.usedByTasks })}</span>
                  {isOAuth(c) && <span>{oauthInfo(c)}</span>}
                </div>
                {actions(c)}
              </Card>
            ))}
          </div>
        </>
      )}

      <CredentialSheet
        open={sheet.open}
        credential={sheet.credential}
        onOpenChange={(o) => setSheet((s) => ({ ...s, open: o }))}
      />

      <ConfirmDialog
        open={!!toDelete}
        onOpenChange={(o) => !o && setToDelete(null)}
        title={t("credentials.delete.title", { name: toDelete?.name ?? "" })}
        description={
          toDelete && toDelete.usedByTasks > 0
            ? t("credentials.delete.inUse", { count: toDelete.usedByTasks })
            : t("credentials.delete.description")
        }
        confirmLabel={t("common.delete")}
        destructive
        onConfirm={onDelete}
      />
    </>
  );
}
