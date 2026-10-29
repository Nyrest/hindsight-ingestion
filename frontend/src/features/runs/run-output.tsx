import { useVirtualizer } from "@tanstack/react-virtual";
import { memo, useCallback, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";
import { CopyButton } from "@/components/copy-button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatDateTime, formatTime } from "@/lib/format";
import type { LogLine, Operation } from "@/lib/types";
import { cn } from "@/lib/utils";

const estimateOperationSize = () => 41;
const estimateLogSize = () => 32;
const HEADER_HEIGHT = 40;

export const RunOperationsTable = memo(function RunOperationsTable({ operations, timezone }: {
  operations: Operation[];
  timezone: string;
}) {
  const { t } = useTranslation();
  const scrollRef = useRef<HTMLDivElement>(null);
  const getItemKey = useCallback((index: number) => operations[index].id, [operations]);
  const virtualizer = useVirtualizer({
    count: operations.length,
    getScrollElement: () => scrollRef.current,
    getItemKey,
    estimateSize: estimateOperationSize,
    overscan: 5,
    scrollMargin: HEADER_HEIGHT,
  });
  const rows = virtualizer.getVirtualItems();
  const paddingTop = rows.length ? rows[0].start - HEADER_HEIGHT : 0;
  const paddingBottom = rows.length ? virtualizer.getTotalSize() - (rows[rows.length - 1].end - HEADER_HEIGHT) : 0;

  return (
    <Table
      aria-label={t("runDetail.operations")}
      aria-rowcount={operations.length + 1}
      className="table-fixed min-w-[56rem]"
      containerProps={{ ref: scrollRef, className: "max-h-[32rem] overflow-auto", tabIndex: 0 }}
    >
      <TableHeader className="sticky top-0 z-10 bg-card">
        <TableRow aria-rowindex={1}>
          <TableHead className="w-[28%] pl-5">{t("runDetail.opColumns.remoteId")}</TableHead>
          <TableHead className="w-[12%]">{t("runDetail.opColumns.type")}</TableHead>
          <TableHead className="w-[16%]">{t("runDetail.opColumns.status")}</TableHead>
          <TableHead className="w-[8%]">{t("runDetail.opColumns.retries")}</TableHead>
          <TableHead className="w-[18%]">{t("runDetail.opColumns.created")}</TableHead>
          <TableHead className="w-[18%] pr-5">{t("runDetail.opColumns.updated")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {paddingTop > 0 && <tr aria-hidden="true"><td colSpan={6} style={{ height: paddingTop }} /></tr>}
        {rows.map((row) => {
          const op = operations[row.index];
          return (
            <TableRow key={row.key} data-index={row.index} ref={virtualizer.measureElement} aria-rowindex={row.index + 2}>
              <TableCell className="truncate pl-5 font-mono text-xs" title={op.remoteOperationId}>{op.remoteOperationId || "—"}</TableCell>
              <TableCell className="truncate" title={op.type}>{op.type}</TableCell>
              <TableCell className="truncate" title={op.status}>
                <span className={cn("text-xs font-medium", /fail|error/i.test(op.status)
                  ? "text-red-600 dark:text-red-400"
                  : /complete|success|succeeded|done/i.test(op.status)
                    ? "text-emerald-600 dark:text-emerald-400" : "text-sky-600 dark:text-sky-400")}>{op.status}</span>
              </TableCell>
              <TableCell className="tabular-nums">{op.retryCount}</TableCell>
              <TableCell className="text-xs whitespace-nowrap">{formatDateTime(op.createdAt, timezone)}</TableCell>
              <TableCell className="pr-5 text-xs whitespace-nowrap">{formatDateTime(op.updatedAt, timezone)}</TableCell>
            </TableRow>
          );
        })}
        {paddingBottom > 0 && <tr aria-hidden="true"><td colSpan={6} style={{ height: paddingBottom }} /></tr>}
      </TableBody>
    </Table>
  );
});

const LEVEL_CLASSES: Record<string, string> = {
  debug: "text-muted-foreground",
  info: "text-sky-600 dark:text-sky-400",
  warn: "text-amber-600 dark:text-amber-400",
  warning: "text-amber-600 dark:text-amber-400",
  error: "text-red-600 dark:text-red-400",
};

export const RunLogCopyButton = memo(function RunLogCopyButton({ log }: { log: LogLine[] }) {
  const value = useMemo(() => log.map((l) => `${l.time} ${l.level.toUpperCase()} ${l.message}`).join("\n"), [log]);
  return <CopyButton value={value} className="size-8" />;
});

export const RunLog = memo(function RunLog({ log, timezone }: { log: LogLine[]; timezone: string }) {
  const { t } = useTranslation();
  const scrollRef = useRef<HTMLDivElement>(null);
  const virtualizer = useVirtualizer({
    count: log.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: estimateLogSize,
    // Wrapped lines resize together; defer DOM updates outside the observer callback.
    useAnimationFrameWithResizeObserver: true,
    overscan: 5,
    paddingStart: 8,
    paddingEnd: 8,
  });
  return (
    <div ref={scrollRef} tabIndex={0} role="region" aria-label={t("runDetail.log")} className="max-h-[32rem] overflow-auto bg-muted/30 font-mono text-xs leading-relaxed">
      <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
        {virtualizer.getVirtualItems().map((row) => {
          const line = log[row.index];
          return (
            <div key={row.key} data-index={row.index} ref={virtualizer.measureElement}
              className="absolute top-0 left-0 flex w-full gap-3 px-5 py-px hover:bg-accent/50"
              style={{ transform: `translateY(${row.start}px)` }}>
              <span className="shrink-0 text-muted-foreground tabular-nums" title={formatDateTime(line.time, timezone)}>{formatTime(line.time, timezone)}</span>
              <span className={cn("w-12 shrink-0 font-semibold uppercase", LEVEL_CLASSES[line.level.toLowerCase()] ?? "text-muted-foreground")}>{line.level}</span>
              <span className="min-w-0 break-all whitespace-pre-wrap">{line.message}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
});
