package steam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

func fetchAchievements(ctx context.Context, client *apiClient, sid string, app uint32, language string, schema gameSchema) (achievements, error) {
	var result achievementResponse
	err := client.get(ctx, "ISteamUserStats/GetPlayerAchievements/v1/", map[string]string{"steamid": sid, "appid": appString(app), "l": language}, &result)
	responseOK := err == nil
	reason := achievementError(err)
	if err == nil {
		if result.PlayerStats == nil {
			return achievements{}, errors.New("steam: missing player achievement response")
		}
		if !result.PlayerStats.Success {
			reason = result.PlayerStats.Error
			err = fmt.Errorf("steam: unsuccessful achievements response for app %d", app)
		}
		if result.PlayerStats.SteamID != "" && result.PlayerStats.SteamID != sid {
			return achievements{}, errors.New("steam: mismatched player achievement response")
		}
	}
	private := strings.EqualFold(reason, "Profile is not public") && (responseOK || httpx.IsStatus(err, http.StatusForbidden))
	noStats := strings.EqualFold(reason, "Requested app has no stats") && (responseOK || httpx.IsStatus(err, http.StatusBadRequest))
	if err != nil && !private && !noStats {
		return achievements{}, err
	}
	data := achievements{Status: "Available", Items: []achievement{}}
	byName := map[string]achievement{}
	if schema.Available != nil {
		for _, a := range schema.Available.Achievements {
			if a.Name == "" {
				return achievements{}, errors.New("steam: missing schema achievement API name")
			}
			name := a.DisplayName
			if name == "" {
				name = a.Name
			}
			byName[a.Name] = achievement{APIName: a.Name, Name: name, Description: a.Description, Hidden: a.Hidden != 0, Icon: a.Icon, LockedIcon: a.LockedIcon, State: "Unknown"}
		}
	}
	if private || noStats {
		if noStats && len(byName) == 0 {
			data.Status = "No Achievements"
		} else {
			data.Status = "Unavailable"
			data.Reason = reason
		}
	} else {
		for _, a := range result.PlayerStats.Achievements {
			if a.Name == "" || a.Achieved == nil || (*a.Achieved != 0 && *a.Achieved != 1) {
				return achievements{}, errors.New("steam: invalid player achievement state")
			}
			entry, exists := byName[a.Name]
			if !exists {
				entry = achievement{APIName: a.Name, Name: a.DisplayName, Description: a.Description}
				if entry.Name == "" {
					entry.Name = a.Name
				}
			}
			entry.State = "Locked"
			if *a.Achieved == 1 {
				entry.State = "Unlocked"
				entry.UnlockedAt = a.UnlockedAt
			}
			byName[a.Name] = entry
		}
		if len(byName) == 0 {
			data.Status = "No Achievements"
		}
	}
	if schema.UnavailableReason != "" && data.Status != "No Achievements" {
		if data.Status == "Available" {
			data.Status = "Partial"
		}
		data.Reason = strings.TrimSpace(data.Reason + " " + schema.UnavailableReason)
	}
	for _, a := range byName {
		data.Items = append(data.Items, a)
		if a.State == "Unknown" && data.Status == "Available" {
			data.Status = "Partial"
		}
	}
	sort.Slice(data.Items, func(i, j int) bool { return data.Items[i].APIName < data.Items[j].APIName })
	return data, nil
}
