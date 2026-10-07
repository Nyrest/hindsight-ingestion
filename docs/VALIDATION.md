# Validation status

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
full inventory scans, and SiYuan/Hindsight deletions rely on periodic reconciliation.
