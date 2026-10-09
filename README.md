<div align="center">
  <h1>🧠 Hindsight Ingestion</h1>
  <p>Keep your notes and files in sync with Hindsight, automatically.</p>
</div>

Sync **Notion, SiYuan, S3, WebDAV, Google Drive, OneDrive and local files** into [Hindsight](https://github.com/vectorize-io/hindsight) memory banks. Self-host with Docker and manage everything from the WebUI.

[Features](#-features) · [Quick start](#-quick-start) · [Sources](#-supported-sources) · [Usage](#-usage) · [Configuration](#-configuration) · [Development](#-development)

## ✨ Features

- ⚡ **Smart incremental sync** — Native change feeds, sync tokens and revision tracking detect what changed. Upload new or updated content and skip the rest.
- 🌐 **Multiple sources, one memory hub** — Bring Notion, SiYuan, S3, WebDAV, Google Drive, OneDrive and local files into Hindsight. Route each task to the bank you choose.
- 🖼️ **Multimodal ingestion** — Sync text, documents and multimodal files from S3, WebDAV, Google Drive, OneDrive and local storage into Hindsight.
- 🔄 **Stay in sync, deletions included** — Clean up documents when source items disappear or leave your sync scope. Incomplete scans never treat missing items as deletions.
- 🚀 **Streaming file ingestion** — Stream files directly into Hindsight without loading entire uploads into memory. Export Google Docs, Sheets and Slides as Markdown, CSV and PDF.
- 🎛️ **Your sync, your rules** — Combine filters, file types, retain strategies, tags and metadata. Set a schedule and timezone for every task, or run on demand.
- 🛡️ **Built to recover** — Persisted checkpoints and operation retries recover from failures and restarts. Overlap protection keeps each task from running twice at once.
- 🧪 **Preview and trace every change** — Dry-run shows planned changes before ingestion. Follow real runs through sync counters, Hindsight operations and detailed logs.
- 🔐 **Self-hosted, secrets encrypted** — Deploy with Docker, encrypt credentials with AES-256-GCM, and choose SQLite, PostgreSQL or MySQL for persistent storage.

## 🚀 Quick start

You need **Docker with Compose**, an existing **Hindsight instance**, and access to a source below.

### 1. Download and configure

```bash
git clone https://github.com/Nyrest/hindsight-ingestion.git
cd hindsight-ingestion
cp .env.example .env
```

In PowerShell, use `Copy-Item .env.example .env` for the copy step.

Edit `.env` and fill in:

| Variable | What to enter |
| --- | --- |
| `BASIC_AUTH_PASSWORD` | WebUI login password. The username defaults to `admin`. |
| `CREDENTIAL_ENCRYPTION_KEY` | Generate with `openssl rand -hex 32`, or use a random passphrase of at least 16 characters. |

Keep the encryption key for restarts, upgrades and backups. Losing or changing it makes saved credentials unreadable.

### 2. Start the service

```bash
docker compose up -d --build
```

Open [http://localhost:8080](http://localhost:8080) and sign in. Docker builds the application; no local Go or Bun installation is needed.

### 3. Create your first task

1. In **Credentials**, add a **Hindsight** connection with its URL and optional API key, then a source connection. Use **Test** to check them. Google Drive and OneDrive also require OAuth setup and **Connect**.
2. In **Tasks**, choose the source, content to sync and destination bank. Leave the retain strategy empty to use the bank's default.
3. Choose a schedule and timezone; the default is **Every hour**. Save the task disabled if you want to preview first.
4. Use **Dry-run** to preview, then **Run now** to sync. Check **Runs** for results and enable the task for scheduled syncs.

## 🔌 Supported Sources

| Source | Supported | Text | Multimodal | Incremental Sync |
| :--- | :---: | :---: | :---: | :---: |
| **Notion** | ✅ | ✅ | ✅ Images | ✅ |
| **SiYuan** | ✅ | ✅ | ✅ Images | ✅ |
| **S3 / S3-compatible storage** | ✅ | ✅ | ✅ | ✅ |
| **WebDAV** | ✅ | ✅ | ✅ | ✅ |
| **Google Drive** | ✅ | ✅ | ✅ | ✅ |
| **OneDrive** | ✅ | ✅ | ✅ | ✅ |
| **File System** | ✅ | ✅ | ✅ | ✅ |

### Local files in Docker

Add a read-only directory mount to the service in [docker-compose.yml](docker-compose.yml):

```yaml
volumes:
  - hindsight-ingestion-data:/data
  - /path/on/host:/sources/documents:ro
```

## 📖 Usage

Task actions are available from the **Tasks** menu:

| Action | When to use it |
| --- | --- |
| **Run now** | Sync without waiting for the next scheduled run. |
| **Dry-run** | Preview changes without writing to Hindsight or changing sync progress. |
| **Full reconcile** | Scan the whole source to check for changes and deletions. Unchanged content is still skipped. |
| **Full re-ingest** | Upload every matching item again, including unchanged content. |
| **Cancel run** | Stop the current run. Changes already made in Hindsight are not rolled back. |
| **Delete documents** | Pause the task and delete its tagged Hindsight documents and associated memories. Source content stays intact; retry if cleanup fails. |
| **Disable** | Pause scheduled syncs while keeping the task's configuration and progress. |

### Sync behavior

- **Deletions sync too.** Items removed from the source or excluded by filters, file types or size limits are removed from Hindsight. Incomplete scans do not delete missing items.
- **Notion and SiYuan deletions use full scans.** The default reconcile interval is **24 hours**. Change it in Settings or use **Full reconcile** to check sooner.
- **Task changes apply on the next run.** Tags, metadata or retain strategy changes re-ingest matching items. Source or root changes restart the baseline scan.
- **Destinations lock after the first successful run.** Create another task to change banks or Hindsight connections.
- **Overlapping tasks share documents.** Tasks syncing the same source item into the same bank can overwrite or delete each other's documents.
- **Each task runs once at a time.** Progress survives restarts and advances only after success; failed runs retry from the last committed progress.

### Proxies and observation scopes

In **Settings**, choose **Default** to follow the server's proxy environment, **No proxy** for a direct connection, or an **HTTP**, **HTTPS** or **SOCKS5** proxy with a `host:port` address and optional authentication. Credentials inherit this choice and can override it. Saved settings apply to newly created clients without a restart. Environment changes require restarting the service. Default follows [Go's proxy environment rules](https://pkg.go.dev/net/http#ProxyFromEnvironment); Docker must receive proxy variables inside the container, for example:

```yaml
services:
  hindsight-ingestion:
    environment:
      HTTP_PROXY: ${HTTP_PROXY:-}
      HTTPS_PROXY: ${HTTPS_PROXY:-}
      NO_PROXY: ${NO_PROXY:-}
```

Choose an **Observation scope** globally or per task: **Combined**, **Shared**, **Per tag**, **All combinations**, or **Custom**. Custom groups support existing bank tags, new tags, and dynamic **Current task**, **Current source** and **Current source group** references. Each group becomes one scope; literal tags do not change document tags. All combinations can grow exponentially (2ⁿ − 1 scopes for n tags). Changing scopes re-retains text on the next full scan. Observation scopes apply to Notion and SiYuan text and inline content; file uploads use Combined, following the [Hindsight request model](https://github.com/vectorize-io/hindsight/blob/main/hindsight-api-slim/hindsight_api/api/http.py). See [retain documentation](https://hindsight.vectorize.io/developer/api/retain) for rule behavior.

The task menu's **Delete documents** action pauses the task and removes only documents tagged `ingestion_task:<id>` and their associated memories from its destination bank. It follows [Hindsight's strict document filtering](https://hindsight.vectorize.io/developer/api/documents). **Delete** also offers an optional **Also delete documents in Hindsight** checkbox, unchecked by default. Sources are unaffected. Failed cleanup keeps progress and the paused task for retry; re-enable the task to import again after successful cleanup.

### Inline images in Notion and SiYuan

Enable **Inline multimodal content** in Settings (off by default), or override it for an individual task. Independently enable **Images** in the effective file policy. Both conditions must be enabled to send body images together with their surrounding text; otherwise the original Markdown is synced. The note body does not depend on the Plain text file policy, and standalone files remain controlled solely by file policy.

Markdown images (including references) and HTML `<img>` elements retain their position and captions. Code examples are excluded. Supported formats are PNG, JPEG, GIF and WebP; no conversion is performed. Each image is limited to the smaller of the configured maximum file size and 20 MiB. Each note is limited to 50 image blocks and the smaller of the configured maximum file size and 100 MiB of image data. Repeated images count toward these document limits.

Notion signed image URLs are refreshed once when expired. SiYuan attachments use authenticated `/assets/...` requests with the notebook location; third-party images and redirects to other origins never receive source credentials. Covers, icons, file properties and other inline media are outside the current scope.

Unreadable, unsupported or oversized images retain their original Markdown and produce warnings in run logs and Dry-run. Successfully submitted notes with missing images advance the cursor and are retried during periodic full reconciliation, **Full reconcile** or **Full re-ingest**. Turning off either image condition replaces the document with text and clears the image retry marker. Configuration changes trigger reconciliation even for unedited notes. Replacing an image without changing the note revision requires **Full re-ingest**.

The destination must support the [Hindsight ordered content blocks interface](https://hindsight.vectorize.io/blog/2026/09/16/screenshot-agent-memory) and have a vision model configured. A rejected multimodal request fails the run; it is not silently converted to text. Dry-run may download images for verification but never writes Hindsight documents, run history, ledger rows or cursors.

## ⚙️ Configuration

Use `.env` for the bundled Compose deployment. Configure source credentials, tasks and global sync settings in the WebUI.

| Variable | Required | Default / purpose |
| --- | :---: | --- |
| `BASIC_AUTH_USERNAME` | ✅ | WebUI and API username. Compose default: `admin`. |
| `BASIC_AUTH_PASSWORD` | ✅ | WebUI and API password. |
| `CREDENTIAL_ENCRYPTION_KEY` | ✅ | Credential encryption key: 64 hex characters, base64-encoded 32 bytes, or a passphrase of at least 16 characters. |
| `PUBLIC_URL` | — | Compose default: `http://localhost:8080`. Use your external service URL for OAuth. |
| `MAX_CONCURRENT_TASKS` | — | `4`. Maximum number of task runs at the same time. |
| `LOG_LEVEL` | — | `info`. Also accepts `debug`, `warn` and `error`. |
| `TZ` | — | `UTC`. Default application timezone. |
| `DISABLE_AUTH` | — | `false`. Set to `true` to allow access without signing in; DO THIS ONLY IF YOU HAVE EXTERNAL AUTHENTICATION SETUP LIKE CLOUDFLARE ACCESS. |

For these additional variables, edit the Compose service's `environment` section. Setting them only in `.env` does not override the bundled configuration.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DB_TYPE` | `sqlite` | Database: `sqlite`, `postgres` or `mysql`. |
| `DB_DSN` | `/data/hindsight-ingestion.db` for SQLite | SQLite file path or database connection string. Required for PostgreSQL and MySQL. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `NOTION_API_BASE` | `https://api.notion.com/v1` | Alternative Notion API URL for a proxy or local testing. |

### Docker releases

Publishing a GitHub Release builds `linux/amd64` and `linux/arm64` images at `ghcr.io/nyrest/hindsight-ingestion`.

A release tagged `v1.2.3` publishes `v1.2.3`, `1.2.3`, `1.2`, `1` and `latest`. Pre-releases publish their version tags without updating `latest`; `0.x` releases omit the `0` tag.

## 🛠️ Development

<details>
<summary>Build, run and check the project locally</summary>

Requires **Go 1.26.6+**, a C compiler for SQLite, and **Bun 1.3+**. From the repository root, in Bash:

```bash
cd frontend
bun install --frozen-lockfile
bun run build
cd ../backend
export BASIC_AUTH_USERNAME=admin
export BASIC_AUTH_PASSWORD=dev
export CREDENTIAL_ENCRYPTION_KEY=$(openssl rand -hex 32)
export DB_DSN=./dev.db
go run ./cmd/server
```

Reuse the encryption key on subsequent starts. Run `bun run dev` in `frontend/` for the development UI; it proxies the API to port `8080`.

Before submitting a change, run `gofmt -l internal cmd`, `go build ./...`, `go vet ./...` and `go test ./...` from `backend/`, and `bun run build` from `frontend/`. Build the frontend first when producing a server that serves the WebUI.

After changing API routes or request/response types, regenerate the contract and frontend types:

```bash
cd backend
go generate ./internal/api
cd ../frontend
bun run generate:api
```

Backend tests detect a stale OpenAPI contract.

</details>

[API reference](docs/API.md) · [OpenAPI contract](docs/openapi.json) · [Validation status](docs/VALIDATION.md)

## 📜 License

[MIT](LICENSE).
