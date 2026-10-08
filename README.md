# Hindsight Ingestion

Self-hosted service that incrementally synchronizes content from **Notion, SiYuan, S3 / S3-compatible storage, WebDAV, Google Drive, OneDrive and local files** into [Hindsight](https://github.com/vectorize-io/hindsight) memory banks.

It is a long-running Go service with an embedded React admin UI, an in-process [gocron](https://github.com/go-co-op/gocron) scheduler and a relational database (SQLite, PostgreSQL or MySQL) as the synchronization ledger. It replaces the Cloudflare-based [notion-to-hindsight-sync](https://github.com/Nyrest/notion-to-hindsight-sync).

## Quick start (Docker Compose, SQLite)

```bash
cp .env.example .env
# edit .env: set BASIC_AUTH_PASSWORD and CREDENTIAL_ENCRYPTION_KEY (openssl rand -hex 32)
docker compose up -d
```

Open <http://localhost:8080> and sign in with the Basic Auth credentials. Everything else is configured in the WebUI:

1. **Credentials** — add a Hindsight credential (destination) and a credential for each source. Use **Test** to verify them. For Google Drive / OneDrive, save the credential and click **Connect**.
2. **Tasks** — create a task: pick a source, its root (bucket, folder, data source, notebook, …), optional filters, the destination bank, retain strategy, file types, tags, metadata and a cron schedule.
3. **Runs** — inspect run history, counters, Hindsight operations and logs.

Data lives in the `hindsight-ingestion-data` named volume (`/data/hindsight-ingestion.db`).

### PostgreSQL / MySQL

PostgreSQL and MySQL are used as external databases via environment variables. Example overrides that also start a database container (for convenience/testing):

```bash
docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d
docker compose -f docker-compose.yml -f docker-compose.mysql.yml up -d
```

In production set `DB_TYPE` / `DB_DSN` to your own server:

```
DB_TYPE=postgres
DB_DSN=host=db user=... password=... dbname=... port=5432 sslmode=disable

DB_TYPE=mysql
DB_DSN=user:pass@tcp(db:3306)/hindsight?charset=utf8mb4&parseTime=True&loc=UTC
```

Migrations run automatically on startup.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `BASIC_AUTH_USERNAME` | — | **Required** unless `DISABLE_AUTH=true`. |
| `BASIC_AUTH_PASSWORD` | — | **Required** unless `DISABLE_AUTH=true`. Never logged. |
| `CREDENTIAL_ENCRYPTION_KEY` | — | **Required.** 32-byte secret (64 hex chars, base64, or any passphrase ≥16 chars, which is stretched with SHA-256). Encrypts stored credentials with AES-256-GCM. Changing it makes stored secrets unreadable. |
| `DISABLE_AUTH` | `false` | `true` disables authentication for the whole UI and API. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `PUBLIC_URL` | derived | External base URL; used to build the OAuth redirect URI `${PUBLIC_URL}/api/oauth/callback`. |
| `DB_TYPE` | `sqlite` | `sqlite`, `postgres` or `mysql`. |
| `DB_DSN` | `/data/hindsight-ingestion.db` | Database DSN / SQLite path. |
| `MAX_CONCURRENT_TASKS` | `4` | Global limit of simultaneously executing task runs. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `NOTION_API_BASE` | `https://api.notion.com/v1` | Override the Notion API base URL (proxies/testing). |

Startup fails when authentication is enabled and username/password are missing, or when the encryption key is missing.

`GET /api/health` is always unauthenticated (for health checks) and returns no sensitive data. The OAuth callback is also reachable without Basic Auth; it is protected by a random single-use `state` value.

### OAuth apps

Register the redirect URI shown on the **Settings** page (`${PUBLIC_URL}/api/oauth/callback`):

- **Google Drive** — Google Cloud console → OAuth client (Web application), scope `drive.readonly`.
- **OneDrive** — Microsoft Entra app registration (Web platform), delegated permissions `Files.Read.All`, `Sites.Read.All`, `offline_access`, `User.Read`.

Access tokens are refreshed by one-time gocron jobs scheduled at `expiry − 15 min`; rotated refresh tokens are persisted immediately. When a provider returns `invalid_grant`, the credential is marked `reauth_required` and shown on the dashboard.

## Sources

| Source | Item identity | Incremental | Deletions | Content |
| --- | --- | --- | --- | --- |
| Notion | `notion_page:<page-id>` | `last_edited_time` high-water mark | periodic full reconciliation; trashed pages | Markdown + page properties |
| SiYuan | `siyuan:<instance-id>:<document-id>` | SQL `updated` high-water mark | periodic full reconciliation | Markdown export |
| S3 | `s3:<endpoint>:<bucket>:<key>` | full metadata LIST, download only changed (VersionId, or ETag + LastModified + Size) | scan generations | streamed file |
| WebDAV | `webdav:<server>:<path>` | RFC 6578 `sync-collection` when available, else PROPFIND + ETag inventory | sync-token removals / scan generations | streamed file |
| Google Drive | `google_drive:<drive-id>:<file-id>` | Changes API page token (baseline inventory, then changes since the start token) | `removed` / trashed / moved out of scope | streamed file; Docs → Markdown, Sheets → CSV, Slides → PDF |
| OneDrive | `onedrive:<drive-id>:<item-id>` | Graph `deltaLink` | `deleted` facet / moved out of scope | streamed file |
| File System | `filesystem:<full-path>` | full inventory; modification time + size revision | scan generations | streamed file |

SiYuan's instance ID can be configured on the credential and defaults to its base
URL. URL components omit the leading `http://` or `https://` and trailing `/`.
S3 without a custom endpoint uses `s3.amazonaws.com`; Google Drive's My Drive
uses its stable root folder ID as the drive ID.

Files are classified by extension into **Plain Text, Documents, Images, Audios**; each task follows the global file-type policy or overrides it. File transfers are streamed (`io.Pipe` → multipart) and never buffered in memory.

### Local files

Create a **File System** credential with an absolute **Root directory** on the server.
The task's **Folder** is relative to that root (`.` by default); sub-folders are
included by default. The folder picker, filters and file policy work as with other
file sources. Symlinks and special files are skipped. Failed inventories never
trigger deletion of missing files.

In Docker, mount the host directory read-only and use its container path in the
credential. Add a volume alongside the existing `/data` volume, for example:

```yaml
volumes:
  - hindsight-ingestion-data:/data
  - /path/on/host:/sources/documents:ro
```

Set **Root directory** to `/sources/documents`. The container runs as UID 10001,
which needs permission to read the mounted files and traverse their directories.

## How synchronization works

- Each enabled task is a gocron cron job (5-field cron + timezone) in singleton mode. Manual **Run now** goes through the same per-task guard, so a task never runs concurrently with itself (scheduled overlaps are skipped; manual requests return `409`). A global semaphore enforces `MAX_CONCURRENT_TASKS`.
- New tasks default to **Every hour** (`0 * * * *`). Existing schedules are preserved.
- **Dry-run**, available in the task menu and editor after saving, performs a full
  source inventory, applies filters and file policy, and verifies changed content
  can be read. It previews create/update/delete/unchanged/skip/failure counts and
  the first 100 items. It does not submit or delete Hindsight documents, create run
  history, or change task state, ledger or cursors. OAuth tokens can still refresh
  when needed. Destination ingestion is only exercised by a real run.
- The database is the ledger: `TaskItem` rows record source revision, desired scope, destination presence and fingerprints. The **target fingerprint** covers the source revision plus retain strategy, tags, metadata and policy revision, so unchanged items are never re-uploaded, and policy changes re-retain everything in scope.
- Destination document IDs use each source's canonical identity directly, independent of task IDs. On the first successful sync after upgrading, existing task-scoped IDs are replaced and removed. Separate tasks that point at the same source object address the same Hindsight document.
- Run order: scan source → submit changes → wait for Hindsight async operations → reconcile deletions → **commit cursor**. If anything fails, the cursor is not advanced and the next run retries from the last committed state.
- Deletions of vanished items happen only after a **complete** inventory (scan generations); partial scans never delete. Items leaving the configured scope (filters, file policy, size limit) are removed from Hindsight.
- A normal run performs a full reconciliation instead of a delta when the configured interval (default 24 h) has elapsed, after configuration changes, or when incremental sync is disabled globally.
- On startup, runs left `running`/`waiting_operations` by a previous process are marked `interrupted`; their cursor was never committed.
- If a completed execution left an active run row behind, the next run marks it `interrupted` and proceeds.

Every Hindsight document gets the fixed `ingestion` tag, plus `source:<type>`, `ingestion_task:<task_id>`, source-specific tags such as `s3_bucket:<bucket>`, and custom task tags. Metadata includes `_ingestion_source_type`, `_ingestion_task_id`, `_ingestion_source_item_id`, `_ingestion_source_revision` and provider fields. Keys starting with `_ingestion_` are reserved.

### Task change rules

| Change | Effect |
| --- | --- |
| Name | none |
| Cron / timezone | gocron job updated |
| Enable / disable | job added / removed (state preserved) |
| Credential secret rotation | none |
| Tags / metadata / retain strategy | policy revision++, matching items re-retained |
| Filters / recursive / file policy | full reconciliation on next run |
| Source credential / root / bucket / data source | cursor reset, full rebaseline |
| Destination credential / bank | locked after the first successful run — create a new task instead |

## Development

Requirements: Go 1.26.6+ (with a C compiler for SQLite/CGO), Bun 1.3+.

```bash
# frontend (outputs to backend/internal/web/dist)
cd frontend && bun install --frozen-lockfile && bun run build
# dev server with API proxy to :8080
bun run dev

# backend
cd backend
BASIC_AUTH_USERNAME=admin BASIC_AUTH_PASSWORD=dev \
CREDENTIAL_ENCRYPTION_KEY=$(openssl rand -hex 32) DB_DSN=./dev.db \
go run ./cmd/server

go test ./...
```

Database portability tests run against PostgreSQL/MySQL when DSNs are provided:

```bash
TEST_POSTGRES_DSN="host=127.0.0.1 user=... password=... dbname=... sslmode=disable" \
TEST_MYSQL_DSN="user:pass@tcp(127.0.0.1:3306)/db?parseTime=True" \
go test ./internal/database/
# full engine suite on another database
SYNC_TEST_DB_TYPE=postgres SYNC_TEST_DB_DSN="..." go test ./internal/sync/
```

API reference: [docs/API.md](docs/API.md); machine-readable contract:
[docs/openapi.json](docs/openapi.json), also served at `/api/openapi.json`.
Routes and Go request/response structs generate the OpenAPI contract. The frontend
uses generated types with `openapi-fetch` for checked paths, parameters and bodies.
After changing API structs or routes, regenerate both artifacts:

```bash
cd backend && go generate ./internal/api
cd ../frontend && bun run generate:api
```

Backend tests reject a stale OpenAPI file. Run `bun run generate:api` and check the
generated diff when reviewing API changes.

Before submitting a change, run `gofmt -l internal cmd`, `go build ./...`,
`go vet ./...`, and `go test ./...` from `backend/`, and `bun run build` from
`frontend/`. Build the frontend before building a deployable server; backend-only
builds compile without it but cannot serve the WebUI.

Validation status and remaining release checks: [docs/VALIDATION.md](docs/VALIDATION.md).

## Deployment notes

- Run exactly **one** replica: the scheduler is in-process. SQLite is single-instance only. Multi-instance deployments would require a gocron distributed elector/locker (not included).
- Back up the database volume **and** `CREDENTIAL_ENCRYPTION_KEY`.
- Put the service behind HTTPS (reverse proxy) when exposed beyond localhost; Basic Auth credentials are otherwise sent in clear text.

## License

[MIT](LICENSE).
