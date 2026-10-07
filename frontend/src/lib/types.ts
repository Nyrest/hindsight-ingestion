// Types mirroring docs/API.md. Keep field names exactly as the server sends them.

export type ApiErrorCode = "bad_request" | "validation" | "not_found" | "conflict" | "internal" | "upstream";

export interface ErrorEnvelope {
  error: string;
  code?: ApiErrorCode | string;
  fields?: Record<string, string>;
}

export type ConnectorType = "notion" | "siyuan" | "s3" | "webdav" | "google_drive" | "onedrive" | "hindsight";

// ---------- Health ----------
export interface Health {
  status: string;
  version: string;
  authEnabled: boolean;
  dbType: string;
}

// ---------- Settings ----------
export interface FilePolicy {
  plainText: boolean;
  documents: boolean;
  images: boolean;
  audios: boolean;
}

export interface Settings {
  incrementalSyncEnabled: boolean;
  fullReconcileIntervalHours: number;
  maxFileSizeMB: number;
  filePolicy: FilePolicy;
  oauthRedirectUri: string;
}

export type SettingsPatch = Partial<Omit<Settings, "oauthRedirectUri">>;

// ---------- Connectors ----------
export type FieldType = "string" | "password" | "number" | "boolean" | "select" | "url" | "textarea";

export interface FieldOption {
  value: string;
  label: string;
}

export interface FieldSpec {
  key: string;
  label: string;
  type: FieldType;
  required?: boolean;
  secret?: boolean;
  placeholder?: string;
  help?: string;
  default?: unknown;
  options?: FieldOption[];
  browse?: boolean;
  /** Kind of item the browse picker selects for this field. */
  browseKind?: string;
}

export type FilterFieldType = "string" | "boolean" | "number" | "datetime" | "enum";
export type FilterOperator = "equals" | "notEquals" | "contains" | "in" | "notIn" | "greaterThan" | "lessThan";

export interface FilterFieldSpec {
  key: string;
  label: string;
  type: FilterFieldType;
  operators: FilterOperator[];
  options?: FieldOption[];
}

export interface CredentialTypeSpec {
  type: string;
  name: string;
  description: string;
  oauth: boolean;
  fields: FieldSpec[];
}

export type IncrementalMode = "high_water_mark" | "delta_token" | "sync_token" | "inventory";
export type DeletionMode = "full_reconcile" | "scan_generation" | "delta";

export interface SourceCapabilities {
  incrementalMode: IncrementalMode | string;
  deletionMode: DeletionMode | string;
  supportsFiles: boolean;
  supportsOAuth: boolean;
  supportsAdvancedFilter: boolean;
}

export interface SourceSpec {
  type: string;
  name: string;
  credentialType: string;
  capabilities: SourceCapabilities;
  browseKinds?: string[];
  fields: FieldSpec[];
  filterFields: FilterFieldSpec[];
}

export interface Connectors {
  credentialTypes: CredentialTypeSpec[];
  sources: SourceSpec[];
}

// ---------- Credentials ----------
export type CredentialStatus = "active" | "reauth_required" | "pending_oauth" | "error";

export interface Credential {
  id: string;
  name: string;
  type: string;
  config: Record<string, unknown>;
  customHeaders: Record<string, string>;
  status: CredentialStatus;
  statusMessage: string;
  oauthConnected: boolean;
  oauthExpiresAt: string | null;
  usedByTasks: number;
  createdAt: string;
  updatedAt: string;
}

export interface CredentialCreate {
  name: string;
  type: string;
  config: Record<string, unknown>;
  customHeaders: Record<string, string>;
}

export interface CredentialPatch {
  name?: string;
  config?: Record<string, unknown>;
  customHeaders?: Record<string, string>;
}

export interface TestResult {
  ok: boolean;
  message: string;
}

export type BrowseKind = "folder" | "file" | "data_source" | "notebook" | "bucket" | "bank" | "drive" | "site";

export interface BrowseItem {
  id: string;
  name: string;
  kind: BrowseKind | string;
  path?: string;
  hasChildren: boolean;
  selectable: boolean;
}

export interface Breadcrumb {
  id: string;
  name: string;
}

export interface BrowseRequest {
  parentId: string;
  kind: string;
  /** Current source config, so pickers can scope (e.g. folders within a drive). */
  config?: Record<string, unknown>;
}

export interface BrowseResult {
  items: BrowseItem[];
  parentId: string;
  breadcrumbs: Breadcrumb[];
}

export interface StrategiesResult {
  defaultStrategy: string;
  strategies: string[];
}

// ---------- Tasks ----------
export type FilterValue = string | number | boolean | Array<string | number>;

export interface FilterRule {
  field: string;
  operator: FilterOperator;
  value: FilterValue;
}

export interface SourceFilter {
  mode: "simple" | "advanced";
  rules: FilterRule[];
  advancedQuery: string;
}

export type FilePolicyMode = "global" | "override";

export interface TaskState {
  lastStartedAt: string | null;
  lastSuccessAt: string | null;
  lastFullReconcileAt: string | null;
  hasCursor: boolean;
}

export interface Task {
  id: string;
  name: string;
  enabled: boolean;
  sourceType: string;
  sourceCredentialId: string;
  sourceConfig: Record<string, unknown>;
  sourceFilter: SourceFilter;
  destinationCredentialId: string;
  destinationBankId: string;
  retainStrategy: string;
  customTags: string[];
  customMetadata: Record<string, string>;
  filePolicyMode: FilePolicyMode;
  filePolicy: FilePolicy;
  cronExpression: string;
  cronTimezone: string;
  configRevision: number;
  policyRevision: number;
  reconcileRequired: boolean;
  destinationLocked: boolean;
  running: boolean;
  nextRunAt: string | null;
  lastRun: Run | null;
  itemCount: number;
  state: TaskState;
  createdAt: string;
  updatedAt: string;
}

export interface TaskInput {
  name: string;
  enabled: boolean;
  sourceType: string;
  sourceCredentialId: string;
  sourceConfig: Record<string, unknown>;
  sourceFilter: SourceFilter;
  destinationCredentialId: string;
  destinationBankId: string;
  retainStrategy: string;
  customTags: string[];
  customMetadata: Record<string, string>;
  filePolicyMode: FilePolicyMode;
  filePolicy: FilePolicy;
  cronExpression: string;
  cronTimezone: string;
}

export type TaskPatch = Partial<TaskInput>;

export interface RunTriggered {
  runId: string;
}

export interface CronValidation {
  valid: boolean;
  error: string;
  nextRuns: string[];
}

// ---------- Runs ----------
export type RunStatus =
  | "pending"
  | "running"
  | "waiting_operations"
  | "succeeded"
  | "failed"
  | "interrupted"
  | "cancelled";

export const RUN_STATUSES: RunStatus[] = [
  "pending",
  "running",
  "waiting_operations",
  "succeeded",
  "failed",
  "interrupted",
  "cancelled",
];

export type TriggerType = "scheduled" | "manual";
export type SyncMode = "incremental" | "full" | "reingest";

export interface Run {
  id: string;
  taskId: string;
  taskName: string;
  triggerType: TriggerType | string;
  status: RunStatus;
  syncMode: SyncMode | string;
  scheduledFor: string | null;
  startedAt: string | null;
  finishedAt: string | null;
  discoveredCount: number;
  createdCount: number;
  updatedCount: number;
  deletedCount: number;
  unchangedCount: number;
  skippedCount: number;
  failedCount: number;
  errorMessage: string;
}

export interface Operation {
  id: string;
  remoteOperationId: string;
  type: string;
  status: string;
  retryCount: number;
  createdAt: string;
  updatedAt: string;
}

export interface LogLine {
  time: string;
  level: string;
  message: string;
}

export interface RunDetail extends Run {
  cursorBefore: string;
  cursorAfter: string;
  operations: Operation[];
  log: LogLine[];
}

export interface Paginated<T> {
  items: T[];
  total: number;
}

export interface RunsQuery {
  limit?: number;
  offset?: number;
  status?: string;
  taskId?: string;
}

// ---------- Dashboard ----------
export interface DashboardRunningTask {
  taskId: string;
  taskName: string;
  runId: string;
  startedAt: string;
}

export interface DashboardReauthCredential {
  id: string;
  name: string;
  type: string;
  status: CredentialStatus;
}

export interface DashboardNextRun {
  taskId: string;
  taskName: string;
  nextRunAt: string;
}

export interface Dashboard {
  totalTasks: number;
  enabledTasks: number;
  runningTasks: DashboardRunningTask[];
  recentFailures: Run[];
  reauthCredentials: DashboardReauthCredential[];
  nextRuns: DashboardNextRun[];
  totals: { items: number; runs24h: number; failed24h: number };
}

export const MASK = "********";

export const ACTIVE_RUN_STATUSES: RunStatus[] = ["pending", "running", "waiting_operations"];

export function isActiveRun(status: RunStatus | undefined | null): boolean {
  return !!status && ACTIVE_RUN_STATUSES.includes(status);
}
