# Validation status

## 2026-10-10 GitHub source

- Windows: `go test ./...`, `go vet ./...`, and `go build ./...` passed in
  `backend`. The suite includes the generated OpenAPI contract consistency check.
- Frontend: `bun test` passed (6 tests, 24 assertions), and `bun run build` passed.
  The root TypeScript config now exposes the application alias to Bun's test runner.
- Connector tests exercise anonymous/PAT access, invalid credentials, `/api/v3`
  prefixes, pagination, rate-limit hints/cancellation, external redirects and
  secret headers, pinned blobs, tree truncation, renames/deletions (including a
  deleted selected folder), old-tree fallback, LFS pointers, and size limits.
- Root/nested ignore rules, negation, directory exclusion, escaped characters,
  spaces, and glob patterns were compared with `git check-ignore --no-index`
  on Windows. This test is also part of the backend suite used by Linux CI;
  Linux execution was not performed in this session.
- Discussion fixtures cover PR separation, merged state, ordinary/review/inline
  comments, pagination, comment edits, stable content hashes, overlap windows,
  and full reconciliation after removals. Existing sync-engine tests cover
  destination failures without cursor advancement.
- API integration verifies encrypted/masked PATs, task defaults, a baseline and
  unchanged scan, full reconciliation, failure recovery, ref rebaselining, and
  Dry-run without destination, ledger, cursor, or run-history changes.
- Real read-only GitHub.com validation against `Nyrest/hindsight-ingestion`
  enumerated 209 files and one PR, and read README by its fixed blob SHA.
  To repeat, set `GITHUB_LIVE_TEST_REPOSITORY` and run
  `go test ./internal/connectors/github -run TestPublicRepositoryReadOnly -count=1 -v`.
- Browser checks on an isolated temporary SQLite server exercised anonymous
  credential creation, repository folder browsing, task content switches,
  structured filters, saving/reloading, and a real public-repository Dry-run
  (three files to create, one image skipped by policy; no browser errors).
- Private-repository PAT access and custom Enterprise instances were covered by
  simulated responses only. No real private/Enterprise access, Linux runtime,
  Docker build, or deployment was verified for this change.

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
