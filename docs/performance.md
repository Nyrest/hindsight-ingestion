# Memory and throughput

File uploads stream from the source to Hindsight. Empty UI post-processing pipelines and UI pipelines containing only text transforms also preserve streaming for binary files, because those transforms leave binary input unchanged. Code pipelines, HTTP steps, and skip steps still execute as configured.

## Working storage

Each run or preview uses a private temporary SQLite working file for ledger snapshots, deduplication, deletion candidates, and preview results. Ledger reads from the application database remain batched; individual source items do not introduce individual remote database lookups. The working file's SQLite page cache is limited to 2 MiB, and row iteration uses bounded pages. Preview counters remain exact, with the first 100 examples ordered by source ID.

Working files contain metadata, not downloaded file bodies. They are removed when execution returns, including errors, cancellation, and panic unwinding. The temporary directory must be writable and have room for the task's metadata. Set `TMPDIR` to disk storage for large inventories; a memory-backed temporary filesystem uses RAM. After an abrupt process termination, abandoned `hindsight-scan-*` directories can be removed while the service is stopped.

This exchanges memory for disk I/O. On the review machine, loading a representative 100,000-row ledger retained about 72 MiB of Go heap before the change. The disk-backed version retained about 16 KiB of additional Go heap after GC, plus native SQLite memory and its bounded page cache. Snapshot loading took about 1.90 seconds versus 0.56 seconds for the former in-memory map. These are isolated local SQLite measurements, not production peak-RSS guarantees.

## Pending writes

Ordinary scans apply destination backpressure in windows of approximately 500 items, allowing a final text batch to complete. Operation status requests use at most four concurrent calls, with batched status updates and an index on run/remote-operation ID. Each window queries only its own operations, so a failure from an earlier window cannot be mistaken for a later success.

IMAP scans spool pending metadata to disk and settle it in bounded windows after FETCH finishes. Destination processing delays therefore do not hold an active IMAP FETCH open. The source cursor advances only after all writes settle. Incomplete inventories and failed writes continue to prevent unsafe disappearance deletion and cursor advancement.

Email staging publication uses keyset pagination over the full composite key, avoiding increasingly expensive OFFSET scans inside its commit transaction.

## Post-processing budgets

Scripts accept at most 16 MiB of text or binary input and return at most 16 MiB of text or binary output. Inline-media input is checked against the same budget before flattening; its base64 bytes count toward that input budget. These budgets are separate from the streaming file-size setting, which can be much larger. Oversized transformations fail the item with a size-limit error rather than silently truncating its content. Binary attachment encoding and batch JSON serialization avoid extra full-size intermediate copies.

Runtimes rotate after 128 documents, 32 MiB of cumulative input/output/fetched response bytes, or ten minutes, whichever comes first. Idle pooling remains bounded and expires after two minutes. Treat scripts as stateless document transforms: global variables can be reused temporarily but are reset when a runtime rotates. These limits bound reuse and accepted payloads; they do not impose a hard heap quota on arbitrary JavaScript allocations within one execution.

Text truncation walks only the requested prefix and preserves Unicode code points. The review's 1 MiB-to-80-character benchmark went from roughly 233 MiB of allocation to 46 KiB. The empty UI pipeline's 4 MiB binary pass-through benchmark allocates 48 bytes in post-processing plus draining the stream; it does not serialize an attachment. These allocation figures describe those specific paths, not an entire run.

## Upstream clients

S3 uses the iterator listing API so early termination cannot leave a channel producer blocked. Steam and Notion share pacing reservations by account/integration, with automatic retirement of unused keys. Cancelled queues do not retain obsolete reservations indefinitely. Task summaries project cursor presence in SQL rather than loading full cursor JSON.

Metadata JSON responses are limited to 64 MiB of decoded bytes. WebDAV parses one XML response at a time, with a 64 MiB response limit and 100,000-entry limit. Exceeding either bound fails the inventory; the scan cannot authorize disappearance deletion from a truncated response. Streaming file bodies retain their independent configured size limit.

## Regression checks and benchmarks

From `backend/`:

```bash
go test ./...
go test -race ./...
go vet ./...
go test ./internal/postprocess ./internal/sync -run '^$' \
  -bench 'Benchmark(TruncateLargeInput|LedgerWorkingSet|UIBinaryPassThrough)$' \
  -benchtime=1x
```

The regression tests cover early S3 exits, Unicode truncation, transformation budgets, runtime rotation and pointer cleanup, idle pacing retirement, cancellation, multi-page email commits, large previews, pending-window failures, and deletion after a write window has already settled. External database and service integration tests use the repository's existing environment-variable opt-ins.
