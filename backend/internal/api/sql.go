package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors/sqlsource"
)

func (s *Server) previewSQL(w http.ResponseWriter, r *http.Request) {
	var body sqlsource.PreviewRequest
	if !decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cred, err := s.Creds.Load(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !sqlsource.IsCredential(cred.Type) {
		writeValidation(w, "Select a SQL database credential", map[string]string{"credentialId": "must be a SQL database credential"})
		return
	}
	connector := &sqlsource.Connector{}
	if s.Cfg.DBType == "sqlite" {
		connector.ApplicationSQLitePath = s.Cfg.DBDSN
	}
	result, err := connector.Preview(ctx, cred, body)
	if err != nil {
		writeValidation(w, err.Error(), map[string]string{"query": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
