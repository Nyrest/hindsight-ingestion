import { useMutation } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, errorMessage } from "@/lib/api";
import type { Task } from "@/lib/types";

export function DryRunDialog({ task, onClose }: { task: Task; onClose: () => void }) {
  const { t } = useTranslation();
  const preview = useMutation({ mutationFn: () => api.dryRunTask(task.id) });
  const result = preview.data;
  const counts = result ? [
    ["create", result.createdCount], ["update", result.updatedCount], ["delete", result.deletedCount],
    ["unchanged", result.unchangedCount], ["skip", result.skippedCount], ["fail", result.failedCount],
  ] as const : [];

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t("dryRun.title", { name: task.name })}</DialogTitle>
          <DialogDescription>{t("dryRun.description")}</DialogDescription>
        </DialogHeader>
        <Button onClick={() => preview.mutate()} disabled={preview.isPending}>
          {preview.isPending && <Loader2 className="animate-spin" />}
          {t(preview.isPending ? "dryRun.running" : "dryRun.start")}
        </Button>
        {preview.isError && <p role="alert" className="text-sm text-destructive">{errorMessage(preview.error)}</p>}
        {result && <>
          <p className="text-sm">{t(result.complete ? "dryRun.complete" : "dryRun.incomplete", { count: result.discoveredCount })}</p>
          {result.error && <p role="alert" className="text-sm text-destructive">{result.error}</p>}
          <div className="grid grid-cols-3 gap-2 sm:grid-cols-6">
            {counts.map(([action, count]) => <div key={action} className="rounded-md border p-2 text-center">
              <div className="text-lg font-semibold">{count}</div>
              <div className="text-xs text-muted-foreground">{t(`dryRun.actions.${action}`)}</div>
            </div>)}
          </div>
          <Table>
            <TableHeader><TableRow><TableHead>{t("dryRun.file")}</TableHead><TableHead>{t("dryRun.action")}</TableHead><TableHead>{t("dryRun.reason")}</TableHead></TableRow></TableHeader>
            <TableBody>{result.items.map((item) => <TableRow key={item.sourceItemId}>
              <TableCell className="max-w-64 break-all whitespace-normal">{item.path || item.name || item.sourceItemId}</TableCell>
              <TableCell>{t(`dryRun.actions.${item.action}`)}</TableCell>
              <TableCell className="max-w-64 break-words whitespace-normal text-muted-foreground">{item.reason}</TableCell>
            </TableRow>)}</TableBody>
          </Table>
          {result.truncated && <p className="text-xs text-muted-foreground">{t("dryRun.truncated")}</p>}
        </>}
      </DialogContent>
    </Dialog>
  );
}
