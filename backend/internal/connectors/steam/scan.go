package steam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func appString(id uint32) string { return strconv.FormatUint(uint64(id), 10) }
func hash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func playtimeHash(g game) string {
	return hash(struct {
		Total                                           int64
		Recent, Windows, Mac, Linux, Deck, Disconnected *int64
		LastPlayed                                      int64
	}{
		g.Playtime, g.Recent, g.Windows, g.Mac, g.Linux, g.Deck, g.Disconnected, g.LastPlayed,
	})
}

func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	sid, language, err := settings(req.Config)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	client, err := c.client(req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	if c.DB == nil {
		return connectors.ScanResult{}, errors.New("steam: database is unavailable")
	}
	key := CredentialKey(req.Credential)
	var previous cursor
	if len(req.Cursor) > 0 && json.Unmarshal(req.Cursor, &previous) != nil {
		req.Full = true
	}
	if previous.Version != cursorVersion || previous.SteamID != sid || previous.Language != language || previous.CredentialKey != key {
		req.Full = true
	}
	data, err := client.profile(ctx, sid)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	p := data.Player
	active := map[uint32]bool{}
	for _, g := range data.Recent {
		active[g.AppID] = true
	}
	var failures []error
	profile := item("steam_profile:"+sid, "Steam Profile: "+p.Username, sid, 0, language, renderProfile(data))
	if data.FriendsError != nil {
		profile.ContentError = data.FriendsError.Error()
		failures = append(failures, data.FriendsError)
	}
	if err := req.Emit(profile); err != nil {
		return connectors.ScanResult{}, err
	}
	if data.FriendsPrivate && req.Log != nil {
		req.Log("warn", "Steam friend list is private; syncing accessible profile data")
	}
	knownGames := make(map[uint32]bool, len(previous.Games))
	for _, appID := range previous.Games {
		knownGames[appID] = true
	}
	next := cursor{Version: cursorVersion, SteamID: sid, Language: language, CredentialKey: key, Games: make([]uint32, 0, len(data.Games))}
	for _, g := range data.Games {
		if err := ctx.Err(); err != nil {
			return connectors.ScanResult{Error: errors.Join(append(failures, err)...)}, nil
		}
		cacheKey := snapshotKey(req, sid, language, g.AppID)
		cached, found, fetchErr := c.loadSnapshot(ctx, cacheKey)
		playedHash := playtimeHash(g)
		known := knownGames[g.AppID]
		if fetchErr == nil && (req.Full || !known || !found || active[g.AppID] || g.Recent != nil && *g.Recent > 0 || cached.PlaytimeHash != playedHash) {
			var schema gameSchema
			schema, fetchErr = c.schema(ctx, client, g.AppID, language, req.Full, req.Preview)
			if fetchErr == nil {
				cached.Achievements, fetchErr = fetchAchievements(ctx, client, sid, g.AppID, language, schema)
				cached.PlaytimeHash = playedHash
				if fetchErr == nil && !req.Preview {
					fetchErr = c.saveSnapshot(ctx, cacheKey, cached)
				}
			}
		}
		name := g.Name
		if name == "" {
			name = "Steam App " + appString(g.AppID)
		}
		gameItem := item("steam_game:"+sid+":"+appString(g.AppID), name, sid, g.AppID, language, renderGame(p, g, cached.Achievements, connectors.AsBool(req.Config["includeAchievements"])))
		if fetchErr != nil {
			gameItem.ContentError = fetchErr.Error()
			failures = append(failures, fmt.Errorf("app %d: %w", g.AppID, fetchErr))
		} else {
			next.Games = append(next.Games, g.AppID)
		}
		if err := req.Emit(gameItem); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	if err := errors.Join(failures...); err != nil {
		return connectors.ScanResult{Error: err}, nil
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	return connectors.ScanResult{Cursor: encoded, Complete: true}, nil
}

func item(id, name, sid string, app uint32, language, text string) connectors.SourceItem {
	content := connectors.SourceContent{Text: text, MIMEType: "text/markdown", Context: name + " for Steam player " + sid, TimestampUnset: true}
	opaque, _ := json.Marshal(content)
	metadata := map[string]string{"steam_id": sid, "steam_document_type": "profile", "steam_language": language}
	if app != 0 {
		metadata["steam_app_id"] = appString(app)
		metadata["steam_document_type"] = "game"
	}
	return connectors.SourceItem{ID: id, Name: name, Path: name, Kind: connectors.KindDocument, MIMEType: "text/markdown", Revision: hash(struct {
		Version        int
		Language, Text string
	}{rendererVersion, language, text}),
		Metadata: metadata, Opaque: opaque, Attributes: map[string]any{}}
}

func (*Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	if err := ctx.Err(); err != nil {
		return connectors.SourceContent{}, err
	}
	var content connectors.SourceContent
	if json.Unmarshal(req.Item.Opaque, &content) != nil || content.Text == "" {
		return connectors.SourceContent{}, errors.New("steam: invalid document content")
	}
	return content, nil
}
