# Validation status

## 2026-10-12 WebCAL source

- Backend build, vet and the full Go suite passed on Windows/SQLite. OpenAPI and
  frontend types were regenerated and checked; the contract has no new endpoints.
  The frontend production build and WebCAL key parity across all 14 locales passed.
- Real HTTP requests to local test servers cover conditional GET (ETag and
  Last-Modified), unchanged-body shortcuts, forced full scans, gzip size limits,
  redirects with custom-header removal, and sanitized errors. Actual Hindsight
  client requests are decoded to assert UTC timestamps, future schedules, floating
  and all-day dates, DST, to-do completion/due/start priority and explicit `unset`.
- SQLite integration tests cover baseline/update/re-ingest, task-isolated IDs,
  valid empty and mass-disappearance deletions, invalid snapshots, Retain and
  asynchronous-operation failures, durable retry of a failed re-ingest, credential
  URL/header rotation, timezone resets and concurrent configuration fences.
  Dry-run verifies matching decisions, visible timezone warnings and no writes to
  destination, ledger, cursor or run history.
- Parser tests cover recurrence grouping and exceptions, orphan exceptions,
  duplicate/conflicting components, escaped text, deterministic revisions,
  RDATE/EXDATE, COUNT and infinite boundaries, THISANDFUTURE, referenced VTIMEZONE
  definitions, DST day durations, truncated input and component/depth limits.
  Property parameters are additionally capped at 8 KiB before decoding to bound
  the upstream decoder's quadratic string copying.
- Bounded fuzz runs passed with two workers (15 and 30 seconds). The initial
  32-worker run terminated during minimization; its saved input passed direct
  replay, so that first run is not counted as a successful fuzz check.
- The 1,000-component parsing/grouping benchmark measured about 40 ms and 9 MB
  allocated per pass on an AMD Ryzen 9 7940HX. These are synthetic local parsing
  measurements, excluding HTTP, database work and Hindsight processing/cost.
- Browser checks against an isolated local database cover WebCAL credential
  creation, password input and mask-preserving edits; English/Chinese task fields,
  deletion warnings, conservative series filters and source-side absence of Browse.

Real subscription providers and a real Hindsight deployment have not been exercised.
No PostgreSQL/MySQL runtime or Docker image acceptance was performed for WebCAL.
Provider quotas, feed compatibility, end-to-end duration and model fees still need
measurement against the intended deployment. V1 does not resolve unknown custom
VTIMEZONE definitions or expand recurring instances.

## 2026-10-08 identity and source update

- Full backend test suite on Windows with SQLite, including canonical identity
  URL normalization, automatic legacy-ID reconciliation, failed replacement
  retries and cleanup, duplicate deletions, and rejection of Hindsight sources.
- Frontend TypeScript check passed. Connector API tests confirm Hindsight remains
  a destination credential and is absent from the source list.
- SQLite legacy schema upgrade was checked for idempotence, preservation of old
  IDs, and storage of canonical IDs longer than 128 characters.
- PostgreSQL/MySQL schema upgrades and real upstream synchronization were not
  exercised for this update.

## Verified locally

- Go formatting, build, vet, and the full backend test suite on Windows and in the
  Alpine builder (including local filesystem boundary/symlink tests).
- Frontend TypeScript checks and production build, OpenAPI generation, and the
  checked-in contract consistency test. The generated client was exercised against
  the running server for JSON success/errors and plain-text Basic Auth rejection.
- Frozen-lockfile frontend installation, Go module consistency/integrity checks,
  and a source-only backend build with no generated frontend assets.
- Real server smoke checks against fake Notion/Hindsight APIs: baseline and incremental
  synchronization, overlap rejection, persisted cursor after restart, full-reconcile
  deletion, destination lock, credential encryption/masking, and auth enabled/disabled.
- Interactive Edge checks of the File System source form, hourly default/preset
  order, directory picking, creating/saving a disabled task, and task Dry-run from
  the list and editor on desktop and at 390 px width.
- Default SQLite, PostgreSQL, and MySQL Compose configuration validation.
- Isolated Compose deployment with the final Alpine image: SQLite volume
  persistence across restart and Basic Auth enabled/disabled.
- Database portability and the full sync engine suite, including Dry-run tests,
  against fresh PostgreSQL 17 and MySQL 8.4 containers.
- Alpine Docker image build: 29.3 MB versus the existing 104.5 MB Debian image
  (about 72% smaller, uncompressed image sizes).
- Isolated Alpine container smoke checks: Basic Auth, health check, read-only local
  directory browsing, Dry-run with no state or destination writes, streamed file
  updates, complete-inventory deletions, and SQLite ledger persistence on restart.

## Remaining release checks

- Verify connectors and OAuth against real upstream services.
- Exercise remaining credential custom-header UI/JSON switching and real OAuth
  browser redirects.

Deployment supports one application instance. Advanced Google Drive queries require
full inventory scans, and SiYuan deletions rely on periodic reconciliation.
