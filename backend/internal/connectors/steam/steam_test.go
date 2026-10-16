package steam

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/database"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

const testSteamID = "76561198188596121"

var testCredential = connectors.Credential{ID: "test-credential", Type: SourceType, Secrets: map[string]string{"apiKey": strings.Repeat("a", 32)}}

type fixture struct {
	username          string
	owned             string
	recent            string
	friends           string
	friendsStatus     int
	schema            string
	schemaStatus      int
	schemaFailures    int
	schemaRedirect    string
	achievements      string
	achievementStatus int
	calls             map[string]int
}

func setup(t *testing.T) (*Connector, *fixture) {
	t.Helper()
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "steam.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); sqlDB.Close() })
	f := &fixture{username: "Player", owned: `{"response":{"game_count":2,"games":[{"appid":620,"name":"Portal 2","playtime_forever":60},{"appid":400,"name":"Portal","playtime_forever":0}]}}`, recent: `{"response":{"total_count":0}}`, friends: `{"friendslist":{"friends":[{"steamid":"76561197960435530","relationship":"friend","friend_since":1}]}}`, schema: `{"game":{"gameName":"Portal","availableGameStats":{"achievements":[{"name":"B","displayName":"Locked achievement","hidden":1},{"name":"A","displayName":"已完成","description":"Description"}]}}}`, achievements: `{"playerstats":{"success":true,"achievements":[{"apiname":"A","achieved":1,"unlocktime":100},{"apiname":"B","achieved":0}]}}`, calls: map[string]int{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-webapi-key") != testCredential.String("apiKey") || r.URL.Query().Get("key") != "" {
			t.Error("API key was not confined to the authentication header")
		}
		f.calls[r.URL.Path]++
		// Network semantics are exercised without waiting one second per fixture request.
		pacing.Lock()
		delete(pacing.next, CredentialKey(testCredential))
		pacing.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ISteamUser/GetPlayerSummaries/v2/":
			fmt.Fprintf(w, `{"response":{"players":[{"steamid":%q,"personaname":%q,"profileurl":"https://steamcommunity.com/id/player/"}]}}`, testSteamID, f.username)
		case "/IPlayerService/GetOwnedGames/v1/":
			io.WriteString(w, f.owned)
		case "/IPlayerService/GetRecentlyPlayedGames/v1/":
			io.WriteString(w, f.recent)
		case "/ISteamUser/GetFriendList/v1/":
			if f.friendsStatus != 0 {
				w.WriteHeader(f.friendsStatus)
			}
			io.WriteString(w, f.friends)
		case "/ISteamUserStats/GetSchemaForGame/v2/":
			if f.schemaRedirect != "" {
				w.Header().Set("Location", f.schemaRedirect)
				w.WriteHeader(http.StatusFound)
				return
			}
			if f.calls[r.URL.Path] <= f.schemaFailures {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if f.schemaStatus != 0 {
				w.WriteHeader(f.schemaStatus)
			}
			io.WriteString(w, f.schema)
		case "/ISteamUserStats/GetPlayerAchievements/v1/":
			if f.achievementStatus != 0 {
				w.WriteHeader(f.achievementStatus)
			}
			io.WriteString(w, f.achievements)
		default:
			t.Error("unexpected Steam endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	address, _ := url.Parse(server.URL)
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address.Host)
	}}
	old := httpx.Shared
	httpx.Shared = transport
	t.Cleanup(func() { httpx.Shared = old; transport.CloseIdleConnections() })
	return &Connector{DB: db}, f
}

func scan(t *testing.T, c *Connector, cur json.RawMessage, full, preview bool) (connectors.ScanResult, []connectors.SourceItem) {
	t.Helper()
	var items []connectors.SourceItem
	res, err := c.Scan(t.Context(), connectors.ScanRequest{TaskID: "test-task", Credential: testCredential, Config: map[string]any{"steamId": testSteamID, "language": "schinese"}, Cursor: cur, Full: full, Preview: preview, Emit: func(item connectors.SourceItem) error { items = append(items, item); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return res, items
}

func TestScanIncrementalFullAndRestart(t *testing.T) {
	c, f := setup(t)
	first, items := scan(t, c, nil, false, false)
	if first.Error != nil || !first.Complete || len(items) != 3 {
		t.Fatalf("baseline: %+v items=%d", first, len(items))
	}
	if items[0].ID != "steam_profile:"+testSteamID || items[1].ID != "steam_game:"+testSteamID+":400" {
		t.Fatal("unexpected item IDs", items)
	}
	content, err := c.OpenContent(t.Context(), connectors.ContentRequest{Item: items[1]})
	if err != nil || !strings.Contains(content.Text, "## Player") || !strings.Contains(content.Text, "已完成") || !strings.Contains(content.Text, "## Locked Achievements") || !strings.Contains(content.Text, "Completion: 50.00%") {
		t.Fatal("incomplete game document", err, content.Text)
	}
	beforeAchievements := f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"]
	beforeSchema := f.calls["/ISteamUserStats/GetSchemaForGame/v2/"]
	// A new connector instance reads its snapshots from the database.
	c = &Connector{DB: c.DB}
	second, unchanged := scan(t, c, first.Cursor, false, false)
	if second.Error != nil || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != beforeAchievements || f.calls["/ISteamUserStats/GetSchemaForGame/v2/"] != beforeSchema {
		t.Fatal("inactive games were refreshed")
	}
	for i := range items {
		if items[i].Revision != unchanged[i].Revision {
			t.Fatal("unchanged data changed revision")
		}
	}
	f.username = "Renamed Player"
	third, renamed := scan(t, c, second.Cursor, false, false)
	if third.Error != nil || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != beforeAchievements {
		t.Fatal("rename queried inactive achievements")
	}
	for i := range items {
		if items[i].Revision == renamed[i].Revision {
			t.Fatal("username omitted from revision")
		}
	}
	f.recent = `{"response":{"games":[{"appid":620,"name":"Portal 2","playtime_2weeks":30}]}}`
	active, _ := scan(t, c, third.Cursor, false, false)
	if active.Error != nil || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != beforeAchievements+1 {
		t.Fatal("active game was not refreshed")
	}
	_, _ = scan(t, c, active.Cursor, false, false)
	if f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != beforeAchievements+2 {
		t.Fatal("active game polling used an interval")
	}
	full, _ := scan(t, c, active.Cursor, true, false)
	if full.Error != nil || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != beforeAchievements+4 || f.calls["/ISteamUserStats/GetSchemaForGame/v2/"] != beforeSchema+2 {
		t.Fatal("full reconciliation did not refresh all games")
	}
}

func TestLegacyCursorRebaselineAndGameReappearance(t *testing.T) {
	c, f := setup(t)
	first, original := scan(t, c, nil, false, false)
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	legacy, _ := json.Marshal(map[string]any{"version": 1, "steamId": testSteamID, "language": "schinese", "credentialKey": CredentialKey(testCredential), "games": map[uint32]string{400: original[1].Revision, 620: original[2].Revision}})
	before := f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"]
	schemas := f.calls["/ISteamUserStats/GetSchemaForGame/v2/"]
	rebased, items := scan(t, c, legacy, false, false)
	if rebased.Error != nil || !rebased.Complete || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != before+2 || f.calls["/ISteamUserStats/GetSchemaForGame/v2/"] != schemas+2 {
		t.Fatal("legacy cursor did not refresh all game data", rebased)
	}
	var compact struct {
		Version int      `json:"version"`
		Games   []uint32 `json:"games"`
	}
	if err := json.Unmarshal(rebased.Cursor, &compact); err != nil || compact.Version != 2 || len(compact.Games) != 2 || compact.Games[0] != 400 || compact.Games[1] != 620 {
		t.Fatal("cursor does not contain sorted AppIDs at version 2", string(rebased.Cursor), err)
	}
	for i := range items {
		if items[i].Revision != original[i].Revision {
			t.Fatal("cursor migration changed document revision")
		}
	}
	ordinary, _ := scan(t, c, rebased.Cursor, false, false)
	if ordinary.Error != nil || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != before+2 {
		t.Fatal("cursor migration repeatedly forces full scans")
	}
	owned := f.owned
	f.owned = `{"response":{}}`
	empty, _ := scan(t, c, ordinary.Cursor, false, false)
	if empty.Error != nil || !empty.Complete {
		t.Fatal(empty)
	}
	f.owned = owned
	reappeared, _ := scan(t, c, empty.Cursor, false, false)
	if reappeared.Error != nil || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != before+4 {
		t.Fatal("reappearing games reused stale snapshots", reappeared)
	}
}

func TestCursorTenThousandGames(t *testing.T) {
	games := make([]uint32, 10000)
	for i := range games {
		games[i] = 1000000 + uint32(i)
	}
	encoded, err := json.Marshal(cursor{Version: cursorVersion, SteamID: testSteamID, Language: "english", CredentialKey: "key", Games: games})
	if err != nil {
		t.Fatal(err)
	}
	var restored cursor
	if err := json.Unmarshal(encoded, &restored); err != nil || len(restored.Games) != 10000 || restored.Games[9999] != games[9999] {
		t.Fatal("large cursor did not roundtrip", err)
	}
	if len(encoded) > 81000 {
		t.Fatalf("cursor retained redundant per-game data: %d bytes", len(encoded))
	}
}

func TestPrivacyNoStatsAndUnknownStates(t *testing.T) {
	c, f := setup(t)
	f.friendsStatus = 401
	f.friends = ""
	f.achievementStatus = 403
	f.achievements = `{"playerstats":{"success":false,"error":"Profile is not public"}}`
	res, items := scan(t, c, nil, true, false)
	if res.Error != nil || !res.Complete {
		t.Fatal("privacy failed accessible inventory", res)
	}
	profile, _ := c.OpenContent(t.Context(), connectors.ContentRequest{Item: items[0]})
	game, _ := c.OpenContent(t.Context(), connectors.ContentRequest{Item: items[1]})
	if !strings.Contains(profile.Text, "friend list is private") || !strings.Contains(game.Text, "Status: Unavailable") || !strings.Contains(game.Text, "Profile is not public") || !strings.Contains(game.Text, "- Unknown: 2\n") || strings.Contains(game.Text, "## Unknown Achievements") || strings.Contains(game.Text, "Completion: 0.00%") || strings.Contains(game.Text, "### ") {
		t.Fatal("private achievements were listed or privacy status was lost", game.Text)
	}
	f.achievementStatus = 200
	f.achievements = `{"playerstats":{"success":true,"achievements":[{"apiname":"A","achieved":1,"unlocktime":100}]}}`
	res, items = scan(t, c, res.Cursor, true, false)
	game, _ = c.OpenContent(t.Context(), connectors.ContentRequest{Item: items[1]})
	if res.Error != nil || !strings.Contains(game.Text, "## Unlocked Achievements") || !strings.Contains(game.Text, "已完成") || !strings.Contains(game.Text, "Completion: Unavailable") || !strings.Contains(game.Text, "- Unknown: 1\n") || strings.Contains(game.Text, "## Unknown Achievements") || strings.Contains(game.Text, "Locked achievement") {
		t.Fatal("partial achievements omitted known entries or included unknown entries", game.Text)
	}
	f.schema = `{"game":{}}`
	f.achievementStatus = 400
	f.achievements = `{"playerstats":{"success":false,"error":"Requested app has no stats"}}`
	res, items = scan(t, c, res.Cursor, true, false)
	game, _ = c.OpenContent(t.Context(), connectors.ContentRequest{Item: items[1]})
	if res.Error != nil || !strings.Contains(game.Text, "Status: No Achievements") {
		t.Fatal("explicit no-stats response failed", res, game.Text)
	}
	f.achievementStatus = 403
	f.achievements = `{"playerstats":{"success":false,"error":"Access denied"}}`
	res, items = scan(t, c, res.Cursor, true, false)
	if res.Error == nil || res.Complete || len(res.Cursor) != 0 || items[1].ContentError == "" {
		t.Fatal("generic 403 was treated as privacy", res)
	}
}

func TestPreviewDoesNotWriteCachesAndEmptyInventoryIsComplete(t *testing.T) {
	c, f := setup(t)
	res, _ := scan(t, c, nil, true, true)
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	for _, model := range []any{&models.SteamSchema{}, &models.SteamPlayerSnapshot{}} {
		var count int64
		c.DB.Model(model).Count(&count)
		if count != 0 {
			t.Fatal("preview persisted content cache")
		}
	}
	var usage models.SteamAPIUsage
	c.DB.First(&usage)
	if usage.Requests != 8 {
		t.Fatal("preview did not count real requests", usage.Requests)
	}
	for _, empty := range []string{`{"response":{"game_count":0,"games":[]}}`, `{"response":{}}`} {
		f.owned = empty
		res, items := scan(t, c, res.Cursor, false, false)
		if res.Error != nil || !res.Complete || len(items) != 1 {
			t.Fatal("empty inventory was blocked", res, len(items))
		}
	}
	f.owned = `{"response":null}`
	_, err := c.Scan(t.Context(), connectors.ScanRequest{TaskID: "test-task", Credential: testCredential, Config: map[string]any{"steamId": testSteamID}, Emit: func(connectors.SourceItem) error { return nil }})
	if err == nil {
		t.Fatal("invalid inventory accepted")
	}
}

func TestValidationCancellationAndBudget(t *testing.T) {
	c, _ := setup(t)
	for _, sid := range []any{nil, 76561198188596121.0, "7656119818859612", "7656119818859612x", "76561197960265728"} {
		if c.ValidateConfig(map[string]any{"steamId": sid}, connectors.Filter{}) == nil {
			t.Fatal("invalid SteamID accepted", sid)
		}
	}
	if c.ValidateConfig(map[string]any{"steamId": testSteamID, "language": "not-a-language"}, connectors.Filter{}) == nil {
		t.Fatal("invalid language accepted")
	}
	if _, err := c.ValidateCredential(t.Context(), testCredential); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(c.DB); err != nil {
		t.Fatal("repeat migration", err)
	}
	key := "budget-test"
	day := time.Now().UTC().Format("2006-01-02")
	if err := c.DB.Create(&models.SteamAPIUsage{KeyHash: key, Day: day, Requests: dailyBudget - 1}).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 5; i++ {
		wg.Go(func() {
			if reserve(t.Context(), c.DB, key, time.Now()) == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if successes != 1 {
		t.Fatal("budget reservation was not atomic", successes)
	}
	if err := reserve(t.Context(), c.DB, key, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal("new UTC day retained old budget", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(pace(ctx, "canceled"), context.Canceled) {
		t.Fatal("canceled pacing continued")
	}
	if _, err := c.Scan(ctx, connectors.ScanRequest{Config: map[string]any{"steamId": testSteamID}, Credential: testCredential}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled scan continued", err)
	}
}

func TestPacingSharesKeyAndCancelsWait(t *testing.T) {
	key := "pacing-test"
	if err := pace(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := pace(ctx, key); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 200*time.Millisecond {
		t.Fatal("shared key did not wait or cancellation was delayed", err)
	}
	if err := pace(t.Context(), "independent-key"); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaAccessDenialAndTransientFailures(t *testing.T) {
	c, f := setup(t)
	client, err := c.client(testCredential)
	if err != nil {
		t.Fatal(err)
	}
	client.http.MaxRetries = 0
	f.schemaStatus = 403
	f.schema = `{"game":{}}`
	schema, err := c.schema(t.Context(), client, 2385180, "schinese", true, false)
	if err != nil || schema.UnavailableReason == "" {
		t.Fatal("playtest schema denial failed accessible game", err)
	}
	var count int64
	c.DB.Model(&models.SteamSchema{}).Count(&count)
	if count != 0 {
		t.Fatal("credential-dependent denial entered shared schema cache")
	}
	f.achievementStatus = 403
	f.achievements = `{"playerstats":{"success":false,"error":"Profile is not public"}}`
	data, err := fetchAchievements(t.Context(), client, testSteamID, 2385180, "schinese", schema)
	text := renderGame(player{SteamID: testSteamID}, game{AppID: 2385180, Name: "TEVI Playtest"}, data)
	if err != nil || data.Status != "Unavailable" || !strings.Contains(text, "Total: Unavailable") || strings.Contains(text, "Completion: 0.00%") {
		t.Fatal("inaccessible schema invented zero progress", err, text)
	}
	f.schemaStatus = 503
	if _, err := c.schema(t.Context(), client, 2385180, "schinese", true, false); err == nil {
		t.Fatal("503 treated as inaccessible schema")
	}
	f.schemaStatus = 403
	f.schema = `{"game":{"error":"Access denied"}}`
	if _, err := c.schema(t.Context(), client, 2385180, "schinese", true, false); err == nil {
		t.Fatal("generic 403 treated as app-specific schema denial")
	}
	f.achievementStatus = 503
	if _, err := fetchAchievements(t.Context(), client, testSteamID, 2385180, "schinese", schema); err == nil {
		t.Fatal("503 treated as private player achievements")
	}
}

func TestNewGamesAndChangedPlaytimeRefreshImmediately(t *testing.T) {
	c, f := setup(t)
	first, _ := scan(t, c, nil, true, false)
	before := f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"]
	f.owned = `{"response":{"games":[{"appid":620,"name":"Portal 2","playtime_forever":61},{"appid":400,"name":"Portal","playtime_forever":0},{"appid":730,"name":"CS2","playtime_forever":0}]}}`
	second, items := scan(t, c, first.Cursor, false, false)
	if second.Error != nil || len(items) != 4 || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != before+2 {
		t.Fatal("new game or changed playtime did not refresh", second)
	}
	f.owned = strings.Replace(f.owned, "Portal 2", "Portal 2 Renamed", 1)
	third, renamed := scan(t, c, second.Cursor, false, false)
	if third.Error != nil || items[2].Revision == renamed[2].Revision || f.calls["/ISteamUserStats/GetPlayerAchievements/v1/"] != before+2 {
		t.Fatal("game rename did not use cached complete achievements")
	}
}

func TestRetriesCountTowardBudgetAndRedirectsAreRejected(t *testing.T) {
	c, f := setup(t)
	client, err := c.client(testCredential)
	if err != nil {
		t.Fatal(err)
	}
	client.http.MaxRetries = 1
	f.schemaFailures = 1
	var out schemaResponse
	path := "ISteamUserStats/GetSchemaForGame/v2/"
	if err := client.get(t.Context(), path, map[string]string{"appid": "620"}, &out); err != nil {
		t.Fatal(err)
	}
	var usage models.SteamAPIUsage
	c.DB.First(&usage)
	if usage.Requests != 2 || f.calls["/"+path] != 2 {
		t.Fatal("HTTP retry did not reserve a second request", usage.Requests)
	}
	f.schemaRedirect = "https://example.invalid/"
	if err := client.get(t.Context(), path, map[string]string{"appid": "620"}, &out); !httpx.IsStatus(err, http.StatusFound) {
		t.Fatal("fixed API endpoint followed a redirect", err)
	}
}
