package api

import "github.com/Nyrest/hindsight-ingestion/internal/connectors"

type healthDTO struct {
	Status      string `json:"status" enum:"active,reauth_required,pending_oauth,error"`
	Version     string `json:"version"`
	AuthEnabled bool   `json:"authEnabled"`
	DBType      string `json:"dbType"`
}

type connectorsDTO struct {
	CredentialTypes []connectors.CredentialType `json:"credentialTypes"`
	Sources         []connectors.SourceInfo     `json:"sources"`
}

type testDTO struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}
type oauthStartDTO struct {
	AuthURL string `json:"authUrl"`
}
type strategiesDTO struct {
	DefaultStrategy string   `json:"defaultStrategy"`
	Strategies      []string `json:"strategies"`
}
type runTriggeredDTO struct {
	RunID string `json:"runId"`
}
type cancelDTO struct {
	Cancelled bool `json:"cancelled"`
}
type cronBody struct {
	CronExpression string `json:"cronExpression"`
	CronTimezone   string `json:"cronTimezone"`
}
type cronDTO struct {
	Valid    bool     `json:"valid"`
	Error    string   `json:"error"`
	NextRuns []string `json:"nextRuns"`
}
type runPageDTO struct {
	Items []runDTO `json:"items"`
	Total int64    `json:"total"`
}
type logDTO struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}
type runDetailDTO struct {
	runDTO
	CursorBefore string         `json:"cursorBefore"`
	CursorAfter  string         `json:"cursorAfter"`
	Operations   []operationDTO `json:"operations"`
	Log          []logDTO       `json:"log"`
}
type nextRun struct {
	TaskID    string `json:"taskId"`
	TaskName  string `json:"taskName"`
	NextRunAt string `json:"nextRunAt"`
}
type running struct {
	TaskID    string  `json:"taskId"`
	TaskName  string  `json:"taskName"`
	RunID     string  `json:"runId"`
	StartedAt *string `json:"startedAt"`
	Status    string  `json:"status" enum:"pending,running,waiting_operations,succeeded,failed,interrupted,cancelled"`
}
type credRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status" enum:"active,reauth_required,pending_oauth,error"`
}
type dashboardTotals struct {
	Items     int64 `json:"items"`
	Runs24h   int64 `json:"runs24h"`
	Failed24h int64 `json:"failed24h"`
}
type dashboardDTO struct {
	TotalTasks        int             `json:"totalTasks"`
	EnabledTasks      int             `json:"enabledTasks"`
	RunningTasks      []running       `json:"runningTasks"`
	RecentFailures    []runDTO        `json:"recentFailures"`
	ReauthCredentials []credRef       `json:"reauthCredentials"`
	NextRuns          []nextRun       `json:"nextRuns"`
	Totals            dashboardTotals `json:"totals"`
}
