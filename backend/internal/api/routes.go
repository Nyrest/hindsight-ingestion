package api

//go:generate go run ../../cmd/openapi ../../../docs/openapi.json

import (
	"net/http"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/hindsight"
	"github.com/Nyrest/hindsight-ingestion/internal/settings"
	"github.com/Nyrest/hindsight-ingestion/internal/sync"
)

// Route metadata is shared by the HTTP router and OpenAPI generator.
type endpoint struct {
	method, path   string
	handler        http.HandlerFunc
	body, response any
	status         int
}

func (s *Server) endpoints() []endpoint {
	return []endpoint{
		{"GET", "/api/health", s.health, nil, healthDTO{}, 200},
		{"GET", "/api/settings", s.getSettings, nil, settingsDTO{}, 200},
		{"PATCH", "/api/settings", s.patchSettings, settings.Settings{}, settingsDTO{}, 200},
		{"GET", "/api/connectors", s.listConnectors, nil, connectorsDTO{}, 200},
		{"GET", "/api/dashboard", s.dashboard, nil, dashboardDTO{}, 200},
		{"GET", "/api/credentials", s.listCredentials, nil, []credentialDTO{}, 200},
		{"POST", "/api/credentials", s.createCredential, credentialBody{}, credentialDTO{}, 201},
		{"GET", "/api/credentials/{id}", s.getCredential, nil, credentialDTO{}, 200},
		{"PATCH", "/api/credentials/{id}", s.patchCredential, credentialBody{}, credentialDTO{}, 200},
		{"DELETE", "/api/credentials/{id}", s.deleteCredential, nil, nil, 204},
		{"POST", "/api/credentials/{id}/test", s.testCredential, nil, testDTO{}, 200},
		{"POST", "/api/credentials/{id}/oauth/start", s.oauthStart, nil, oauthStartDTO{}, 200},
		{"POST", "/api/credentials/{id}/refresh", s.refreshCredential, nil, credentialDTO{}, 200},
		{"POST", "/api/credentials/{id}/browse", s.browseCredential, connectors.BrowseRequest{}, connectors.BrowseResult{}, 200},
		{"GET", "/api/credentials/{id}/tags", s.credentialTags, nil, hindsight.TagPage{}, 200},
		{"GET", "/api/credentials/{id}/strategies", s.credentialStrategies, nil, strategiesDTO{}, 200},
		{"GET", "/api/oauth/callback", s.oauthCallback, nil, nil, 302},
		{"GET", "/api/tasks", s.listTasks, nil, []taskDTO{}, 200},
		{"POST", "/api/tasks", s.createTask, taskBody{}, taskDTO{}, 201},
		{"POST", "/api/tasks/validate-cron", s.validateCron, cronBody{}, cronDTO{}, 200},
		{"GET", "/api/tasks/{id}", s.getTask, nil, taskDTO{}, 200},
		{"PATCH", "/api/tasks/{id}", s.patchTask, taskBody{}, taskDTO{}, 200},
		{"DELETE", "/api/tasks/{id}/documents", s.deleteTaskDocuments, nil, deleteDocumentsDTO{}, 200},
		{"DELETE", "/api/tasks/{id}", s.deleteTask, nil, nil, 204},
		{"POST", "/api/tasks/{id}/run", s.runTask(""), nil, runTriggeredDTO{}, 202},
		{"POST", "/api/tasks/{id}/dry-run", s.dryRunTask, nil, sync.DryRunResult{}, 200},
		{"POST", "/api/tasks/{id}/full-reconcile", s.runTask("full"), nil, runTriggeredDTO{}, 202},
		{"POST", "/api/tasks/{id}/full-reingest", s.runTask("reingest"), nil, runTriggeredDTO{}, 202},
		{"POST", "/api/tasks/{id}/cancel", s.cancelTask, nil, cancelDTO{}, 202},
		{"GET", "/api/tasks/{id}/runs", s.taskRuns, nil, runPageDTO{}, 200},
		{"GET", "/api/runs", s.listRuns, nil, runPageDTO{}, 200},
		{"GET", "/api/runs/{id}", s.getRun, nil, runDetailDTO{}, 200},
	}
}
