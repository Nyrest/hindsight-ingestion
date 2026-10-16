package steam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (c *Connector) schema(ctx context.Context, client *apiClient, appID uint32, language string, full, preview bool) (gameSchema, error) {
	if !full {
		var row models.SteamSchema
		err := c.DB.WithContext(ctx).First(&row, "app_id = ? AND language = ?", appID, language).Error
		if err == nil {
			var data gameSchema
			if json.Unmarshal([]byte(row.PayloadJSON), &data) == nil {
				return data, nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return gameSchema{}, err
		}
	}
	var result schemaResponse
	if err := client.get(ctx, "ISteamUserStats/GetSchemaForGame/v2/", map[string]string{"appid": appString(appID), "l": language}, &result); err != nil {
		if inaccessibleSchema(err) {
			// This app-specific denial is not shared across credentials in the schema cache.
			return gameSchema{UnavailableReason: "Steam achievement schema is not accessible."}, nil
		}
		return gameSchema{}, err
	}
	if result.Game == nil {
		return gameSchema{}, errors.New("steam: missing game schema response")
	}
	if !preview {
		payload, err := json.Marshal(result.Game)
		if err != nil {
			return gameSchema{}, err
		}
		row := models.SteamSchema{AppID: appID, Language: language, PayloadJSON: string(payload)}
		if err = c.DB.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return gameSchema{}, err
		}
	}
	return *result.Game, nil
}

func inaccessibleSchema(err error) bool {
	var response *httpx.HTTPError
	if !errors.As(err, &response) || response.Status != http.StatusForbidden {
		return false
	}
	var body map[string]map[string]json.RawMessage
	if json.Unmarshal([]byte(response.Body), &body) != nil {
		return false
	}
	game, ok := body["game"]
	return ok && game != nil && len(game) == 0 && len(body) == 1
}

func snapshotKey(req connectors.ScanRequest, sid, language string, appID uint32) models.SteamPlayerSnapshot {
	return models.SteamPlayerSnapshot{TaskID: req.TaskID, CredentialKey: CredentialKey(req.Credential), SteamID: sid, AppID: appID, Language: language}
}

func (c *Connector) loadSnapshot(ctx context.Context, key models.SteamPlayerSnapshot) (snapshot, bool, error) {
	var row models.SteamPlayerSnapshot
	err := c.DB.WithContext(ctx).Where(&key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return snapshot{}, false, nil
	}
	if err != nil {
		return snapshot{}, false, err
	}
	var data snapshot
	if json.Unmarshal([]byte(row.PayloadJSON), &data) != nil {
		return snapshot{}, false, nil
	}
	return data, true, nil
}

func (c *Connector) saveSnapshot(ctx context.Context, key models.SteamPlayerSnapshot, data snapshot) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	key.PayloadJSON = string(payload)
	return c.DB.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&key).Error
}
