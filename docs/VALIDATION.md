# Validation status

## Verified locally

- Go formatting, build, vet, and the full backend test suite.
- Frontend TypeScript checks and production build.
- Frozen-lockfile frontend installation, Go module consistency/integrity checks,
  and a source-only backend build with no generated frontend assets.
- Real server smoke checks against fake Notion/Hindsight APIs: baseline and incremental
  synchronization, overlap rejection, persisted cursor after restart, full-reconcile
  deletion, destination lock, credential encryption/masking, and auth enabled/disabled.
- Headless Edge screenshots of the tasks and credentials pages.
- Default SQLite, PostgreSQL, and MySQL Compose configuration validation.

## Remaining release checks

- Rebuild the Docker image and run Compose checks for SQLite volume persistence and
  authentication enabled/disabled.
- Re-run database and sync tests against PostgreSQL and MySQL.
- Verify connectors and OAuth against real upstream services.
- Exercise interactive browser flows, including saving forms, browsing, custom-header
  UI/JSON switching, and OAuth redirects.

The previous implementation pass tested PostgreSQL/MySQL and Docker successfully;
those checks have not been repeated after the final fixes. Docker Desktop startup
failed during the latest verification. Configuration validation does not establish
container runtime behavior.

Deployment supports one application instance. Advanced Google Drive queries require
full inventory scans, and SiYuan/Hindsight deletions rely on periodic reconciliation.
