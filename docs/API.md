# REST API Contract

The generated [OpenAPI contract](openapi.json) is derived from the backend's route
registry and Go wire types. It is served at `GET /api/openapi.json` under the same
authentication rules as the other API endpoints. The frontend generates its types
from this file and calls the API through `openapi-fetch`.

`POST /api/tasks/{id}/dry-run` tests a saved task with a full source inventory and
reads changed content without destination writes or sync state changes. It returns
`complete`, `error`, `discoveredCount`, `createdCount`, `updatedCount`, `deletedCount`,
`unchangedCount`, `skippedCount`, `failedCount`, `items` and `truncated`. Each item has
`sourceItemId`, `name`, `path`, `action` and `reason`. Actions are `create`, `update`,
`delete`, `unchanged`, `skip`, `fail`. Counts cover the entire scan; `items` contains
the first 100 items sorted by source ID. An incomplete inventory does not infer
deletions of missing items. A source scan failure is returned in `error` with
`complete=false`; credential/setup errors use the standard error envelope. A busy
task returns `409`. The request shares the normal runner's concurrency limit and
can be cancelled using the task cancel endpoint. OAuth refresh may update tokens.

The `filesystem` source uses a credential `rootPath` (absolute server/container
directory), and task config `{ "folder": ".", "recursive": true }`.
New tasks default to hourly scheduling (`0 * * * *`) if cron is omitted.

All endpoints live under `/api`, return JSON, and are protected by HTTP Basic Auth
unless `DISABLE_AUTH=true` (except `GET /api/health`, which is always public and
returns no sensitive data, and the OAuth callback, protected by its state token).
Basic Auth rejection returns a plain-text `401` response; application errors use
the JSON envelope below.

Requests other than `GET`, `HEAD` and `OPTIONS` require `X-CSRF-Protection: 1`,
including when authentication is disabled. Cross-origin browser write requests
are rejected using Fetch Metadata or Origin checks. Rejection returns JSON with
HTTP `403` and code `csrf`. The WebUI sends the required header automatically;
direct API clients must include it as well.

Errors use a uniform envelope with an appropriate HTTP status:

```json
{ "error": "human readable message", "code": "conflict", "fields": { "name": "is required" } }
```

`code` is one of `bad_request`, `validation`, `not_found`, `conflict`, `internal`, `upstream`, `csrf`.
`fields` is optional (validation errors).

Timestamps are RFC 3339 strings (UTC) or `null`. IDs are strings (UUID v4).

## Secrets

Secret values (API keys, passwords, tokens, secret keys, custom header values) are
**write-only**. Responses replace a stored secret with the mask string `"********"`
(empty string when unset). On create/update, sending the mask string for a secret
field (or a custom header value) keeps the stored value unchanged. Sending `""`
clears it.

## Health

`GET /api/health` → `{ "status": "ok", "version": "0.1.0", "authEnabled": true, "dbType": "sqlite" }`

## Settings

`GET /api/settings` / `PATCH /api/settings` (partial body)

```json
{
  "incrementalSyncEnabled": true,
  "fullReconcileIntervalHours": 24,
  "maxFileSizeMB": 100,
  "filePolicy": { "plainText": true, "documents": true, "images": false, "audios": false },
  "oauthRedirectUri": "http://localhost:8080/api/oauth/callback"   // read-only
}
```

## Connectors (schema for schema-driven forms)

`GET /api/connectors` →

```json
{
  "credentialTypes": [
    {
      "type": "notion",
      "name": "Notion",
      "description": "Notion internal integration token",
      "oauth": false,
      "fields": [FieldSpec, ...]
    }
  ],
  "sources": [
    {
      "type": "notion",
      "name": "Notion",
      "credentialType": "notion",
      "capabilities": {
        "incrementalMode": "high_water_mark",  // high_water_mark | delta_token | sync_token | inventory
        "deletionMode": "full_reconcile",       // full_reconcile | scan_generation | delta
        "supportsFiles": false,
        "supportsOAuth": false,
        "supportsAdvancedFilter": false
      },
      "browseKinds": ["data_source"],          // what the source "root" picker selects
      "fields": [FieldSpec, ...],               // Task.sourceConfig fields
      "filterFields": [FilterFieldSpec, ...]    // Task.sourceFilter.rules fields
    }
  ]
}
```

`FieldSpec`:

```json
{
  "key": "bucket",
  "label": "Bucket",
  "type": "string",          // string | password | number | boolean | select | url | textarea
  "required": true,
  "secret": false,           // password fields are always secret
  "placeholder": "my-bucket",
  "help": "Optional help text",
  "default": "",             // any JSON value
  "options": [{ "value": "a", "label": "A" }],   // for select
  "browse": true             // value can be picked via POST /api/credentials/:id/browse
}
```

`FilterFieldSpec`:

```json
{
  "key": "name",
  "label": "File name",
  "type": "string",          // string | boolean | number | datetime | enum
  "operators": ["equals", "notEquals", "contains", "in", "notIn"],
  "options": [{ "value": "x", "label": "X" }]    // for enum
}
```

Operators: `equals`, `notEquals`, `contains`, `in`, `notIn`, `greaterThan`, `lessThan`.
For `in`/`notIn` the rule value is an array.

## Credentials

Google Drive and OneDrive credentials require `config.clientId` and
`config.clientSecret`. OneDrive also accepts `config.tenant` (default `common`).
These OAuth application settings belong to the credential; there are no environment
defaults. The client secret is encrypted and returned as a masked value.

```json
Credential {
  "id": "…",
  "name": "Work Notion",
  "type": "notion",
  "config": { "baseUrl": "https://…", "token": "********" },   // secrets masked
  "customHeaders": { "CF-Access-Client-Id": "********" },      // values masked
  "status": "active",            // active | reauth_required | pending_oauth | error
  "statusMessage": "",
  "oauthConnected": false,
  "oauthExpiresAt": null,
  "usedByTasks": 2,
  "createdAt": "…",
  "updatedAt": "…"
}
```

- `GET /api/credentials` → `Credential[]`
- `POST /api/credentials` body `{ name, type, config, customHeaders }` → `201 Credential`
- `GET /api/credentials/:id` → `Credential`
- `PATCH /api/credentials/:id` body partial `{ name?, config?, customHeaders? }` → `Credential`.
  `customHeaders`, when present, replaces the whole set (masked values keep the old value for that header).
  Header names are rejected when duplicated case-insensitively.
- `DELETE /api/credentials/:id` → `204` (`409` if used by a task)
- `POST /api/credentials/:id/test` → `{ "ok": true, "message": "Connected as …" }` (always 200; `ok:false` on failure)
- `POST /api/credentials/:id/oauth/start` → `{ "authUrl": "https://accounts.google.com/…" }` — the UI navigates the
  browser to it; the provider redirects to `/api/oauth/callback`, which then redirects to
  `/credentials?oauth=success&id=…` or `/credentials?oauth=error&message=…`.
- `POST /api/credentials/:id/refresh` → `Credential` (force OAuth refresh)
- `POST /api/credentials/:id/browse` body `{ "parentId": "", "kind": "" }` →

```json
{
  "items": [
    { "id": "abc", "name": "Projects", "kind": "folder", "path": "/Projects", "hasChildren": true, "selectable": true }
  ],
  "parentId": "",
  "breadcrumbs": [{ "id": "", "name": "Root" }]
}
```

  `kind` values: `folder`, `file`, `data_source`, `notebook`, `bucket`, `bank`, `drive`, `site`.
  For a Hindsight credential, browsing the root lists banks.

- `GET /api/credentials/:id/strategies?bankId=…` → `{ "defaultStrategy": "", "strategies": ["documents", "chat"] }`
  (Hindsight credentials only).

## Tasks

```json
Task {
  "id": "…",
  "name": "Notion → Personal",
  "enabled": true,
  "sourceType": "notion",
  "sourceCredentialId": "…",
  "sourceConfig": { "dataSourceId": "…" },
  "sourceFilter": {
    "mode": "simple",              // simple | advanced
    "rules": [{ "field": "name", "operator": "contains", "value": "notes" }],
    "advancedQuery": ""            // raw provider query (Google Drive q) when mode=advanced
  },
  "destinationCredentialId": "…",
  "destinationBankId": "personal",
  "retainStrategy": "",            // empty → bank default
  "customTags": ["team:alpha"],
  "customMetadata": { "project": "x" },   // keys starting with "_ingestion_" are rejected
  "filePolicyMode": "global",      // global | override
  "filePolicy": { "plainText": true, "documents": true, "images": false, "audios": false },
  "cronExpression": "*/15 * * * *",
  "cronTimezone": "UTC",
  "configRevision": 3,
  "policyRevision": 1,
  "reconcileRequired": false,
  "destinationLocked": true,       // true after the first successful run
  "running": false,
  "nextRunAt": "…" | null,
  "lastRun": Run | null,
  "itemCount": 120,
  "state": { "lastStartedAt": null, "lastSuccessAt": null, "lastFullReconcileAt": null, "hasCursor": false },
  "createdAt": "…",
  "updatedAt": "…"
}
```

- `GET /api/tasks` → `Task[]`
- `POST /api/tasks` → `201 Task`
- `GET /api/tasks/:id` → `Task`
- `PATCH /api/tasks/:id` (partial; same fields) → `Task`. Changing destination after `destinationLocked` → `422`.
- `DELETE /api/tasks/:id` → `204` (`409` while running)
- `POST /api/tasks/:id/run` → `202 { "runId": "…" }`, `409` if already running
  Stale active run rows without a live execution are marked `interrupted` before starting a new run.
- `POST /api/tasks/:id/full-reconcile` → same as run, forces a full inventory scan
- `POST /api/tasks/:id/full-reingest` → same as run, re-retains every matching item
- `POST /api/tasks/:id/cancel` → `202` cancels the active run (`409` if not running)
- `GET /api/tasks/:id/runs?limit=50&offset=0` → `{ "items": Run[], "total": 10 }`
- `POST /api/tasks/validate-cron` body `{ "cronExpression": "…", "cronTimezone": "…" }` →
  `{ "valid": true, "error": "", "nextRuns": ["…", "…", "…"] }`

## Runs

```json
Run {
  "id": "…",
  "taskId": "…",
  "taskName": "…",
  "triggerType": "scheduled",      // scheduled | manual
  "status": "succeeded",           // pending | running | waiting_operations | succeeded | failed | interrupted | cancelled
  "syncMode": "incremental",       // incremental | full | reingest
  "scheduledFor": null,
  "startedAt": "…",
  "finishedAt": "…",
  "discoveredCount": 0, "createdCount": 0, "updatedCount": 0, "deletedCount": 0,
  "unchangedCount": 0, "skippedCount": 0, "failedCount": 0,
  "errorMessage": ""
}
```

- `GET /api/runs?limit=50&offset=0&status=failed&taskId=…` → `{ "items": Run[], "total": 10 }`
- `GET /api/runs/:id` → `Run & { "cursorBefore": "…", "cursorAfter": "…", "operations": Operation[], "log": LogLine[] }`
  - `Operation { id, remoteOperationId, type, status, retryCount, createdAt, updatedAt }`
  - `LogLine { time, level, message }` (most recent 500)

## Dashboard

`GET /api/dashboard` →

```json
{
  "totalTasks": 4,
  "enabledTasks": 3,
  "runningTasks": [{ "taskId": "…", "taskName": "…", "runId": "…", "startedAt": "…" }],
  "recentFailures": [Run, ...],          // last 10 failed/interrupted runs
  "reauthCredentials": [{ "id": "…", "name": "…", "type": "google_drive", "status": "reauth_required" }],
  "nextRuns": [{ "taskId": "…", "taskName": "…", "nextRunAt": "…" }],
  "totals": { "items": 1234, "runs24h": 30, "failed24h": 1 }
}
```
