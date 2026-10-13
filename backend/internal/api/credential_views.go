package api

import (
	"context"
	"net/http"

	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func (s *Server) writeCredential(w http.ResponseWriter, r *http.Request, row *models.Credential, status int) {
	usage, err := s.credentialUsage(r.Context(), []string{row.ID})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, status, s.credentialView(row, usage[row.ID]))
}

type credentialUse struct {
	SourceCredentialID      string
	DestinationCredentialID string
	UseCount                int64
}

func (s *Server) credentialUsage(ctx context.Context, ids []string) (map[string]int64, error) {
	usage := make(map[string]int64, len(ids))
	if len(ids) == 0 {
		return usage, nil
	}
	var rows []credentialUse
	err := s.DB.WithContext(ctx).Model(&models.Task{}).
		Select("source_credential_id, destination_credential_id, COUNT(*) AS use_count").
		Where("source_credential_id IN ? OR destination_credential_id IN ?", ids, ids).
		Group("source_credential_id, destination_credential_id").Scan(&rows).Error
	for _, row := range rows {
		usage[row.SourceCredentialID] += row.UseCount
		if row.DestinationCredentialID != row.SourceCredentialID {
			usage[row.DestinationCredentialID] += row.UseCount
		}
	}
	return usage, err
}
