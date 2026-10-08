// Wire types are generated from backend Go DTOs through OpenAPI.
import type { components, paths } from "./openapi";

export type ErrorEnvelope = components["schemas"]["apiError"];
export type Health = components["schemas"]["healthDTO"];
export type FilePolicy = components["schemas"]["FilePolicy"];
export type ProxyConfig = components["schemas"]["Config"];
export type ObservationScope = components["schemas"]["Scope"];
export type ScopeTag = components["schemas"]["TagRef"];
export type Settings = components["schemas"]["settingsDTO"];
export type FieldOption = components["schemas"]["Option"];
export type FieldSpec = components["schemas"]["FieldSpec"];
export type FilterFieldSpec = components["schemas"]["FilterFieldSpec"];
export type CredentialTypeSpec = components["schemas"]["CredentialType"];
export type SourceCapabilities = components["schemas"]["Capabilities"];
export type SourceSpec = components["schemas"]["SourceInfo"];
export type Connectors = components["schemas"]["connectorsDTO"];
export type Credential = components["schemas"]["credentialDTO"];
export type TestResult = components["schemas"]["testDTO"];
export type BrowseItem = components["schemas"]["BrowseItem"];
export type Breadcrumb = components["schemas"]["Breadcrumb"];
export type BrowseRequest = paths["/api/credentials/{id}/browse"]["post"]["requestBody"]["content"]["application/json"];
export type BrowseResult = components["schemas"]["BrowseResult"];
export type StrategiesResult = components["schemas"]["strategiesDTO"];
export type FilterRule = components["schemas"]["FilterRule"];
export type SourceFilter = components["schemas"]["Filter"];
export type TaskState = components["schemas"]["taskStateDTO"];
export type Task = components["schemas"]["taskDTO"];
export type RunTriggered = components["schemas"]["runTriggeredDTO"];
export type CronValidation = components["schemas"]["cronDTO"];
export type Run = components["schemas"]["runDTO"];
export type RunDetail = components["schemas"]["runDetailDTO"];
export type Operation = components["schemas"]["operationDTO"];
export type LogLine = components["schemas"]["logDTO"];
export type DashboardRunningTask = components["schemas"]["running"];
export type DashboardReauthCredential = components["schemas"]["credRef"];
export type DashboardNextRun = components["schemas"]["nextRun"];
export type Dashboard = components["schemas"]["dashboardDTO"];
export type DryRunResult = components["schemas"]["DryRunResult"];
export type DryRunItem = components["schemas"]["DryRunItem"];

export type SettingsPatch = Partial<Omit<Settings, "oauthRedirectUri">>;
export type CredentialCreate = paths["/api/credentials"]["post"]["requestBody"]["content"]["application/json"];
export type CredentialPatch = paths["/api/credentials/{id}"]["patch"]["requestBody"]["content"]["application/json"];
export type TaskInput = paths["/api/tasks"]["post"]["requestBody"]["content"]["application/json"];
export type TaskPatch = paths["/api/tasks/{id}"]["patch"]["requestBody"]["content"]["application/json"];
export type RunsQuery = NonNullable<paths["/api/runs"]["get"]["parameters"]["query"]>;
export type ApiErrorCode = ErrorEnvelope["code"];
export type FieldType = FieldSpec["type"];
export type FilterFieldType = FilterFieldSpec["type"];
export type FilterOperator = FilterRule["operator"];
export type FilterValue = FilterRule["value"];
export type CredentialStatus = Credential["status"];
export type FilePolicyMode = Task["filePolicyMode"];
export type RunStatus = Run["status"];
export type TriggerType = Run["triggerType"];
export type SyncMode = Run["syncMode"];
export type IncrementalMode = SourceCapabilities["incrementalMode"];
export type DeletionMode = SourceCapabilities["deletionMode"];
export type BrowseKind = BrowseItem["kind"];
export interface Paginated<T> { items: T[]; total: number }

export const RUN_STATUSES: RunStatus[] = [
  "pending",
  "running",
  "waiting_operations",
  "succeeded",
  "failed",
  "interrupted",
  "cancelled",
];

export const MASK = "********";

export const ACTIVE_RUN_STATUSES: RunStatus[] = ["pending", "running", "waiting_operations"];

export function isActiveRun(status: RunStatus | undefined | null): boolean {
  return !!status && ACTIVE_RUN_STATUSES.includes(status);
}
