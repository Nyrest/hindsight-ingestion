// Package steam ingests Steam profiles and games as Markdown documents.
package steam

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"gorm.io/gorm"
)

const SourceType = "steam"
const rendererVersion = 1
const cursorVersion = 2

type Connector struct{ DB *gorm.DB }

// Steam achievement endpoints use these language names for their l parameter.
var languages = []connectors.Option{
	{Value: "english", Label: "English"}, {Value: "arabic", Label: "العربية"},
	{Value: "bulgarian", Label: "български език"}, {Value: "schinese", Label: "简体中文"},
	{Value: "tchinese", Label: "繁體中文"}, {Value: "czech", Label: "čeština"},
	{Value: "danish", Label: "Dansk"}, {Value: "dutch", Label: "Nederlands"},
	{Value: "finnish", Label: "Suomi"}, {Value: "french", Label: "Français"},
	{Value: "german", Label: "Deutsch"}, {Value: "greek", Label: "Ελληνικά"},
	{Value: "hungarian", Label: "Magyar"}, {Value: "indonesian", Label: "Bahasa Indonesia"},
	{Value: "italian", Label: "Italiano"}, {Value: "japanese", Label: "日本語"},
	{Value: "koreana", Label: "한국어"}, {Value: "malay", Label: "Bahasa Melayu"},
	{Value: "norwegian", Label: "Norsk"}, {Value: "polish", Label: "Polski"},
	{Value: "portuguese", Label: "Português"}, {Value: "brazilian", Label: "Português-Brasil"},
	{Value: "romanian", Label: "Română"}, {Value: "russian", Label: "Русский"},
	{Value: "spanish", Label: "Español-España"}, {Value: "latam", Label: "Español-Latinoamérica"},
	{Value: "swedish", Label: "Svenska"}, {Value: "thai", Label: "ไทย"},
	{Value: "turkish", Label: "Türkçe"}, {Value: "ukrainian", Label: "Українська"},
	{Value: "vietnamese", Label: "Tiếng Việt"},
}

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: SourceType, Name: "Steam", Description: "Steam Web API key for profiles, games and achievements",
		Fields: []connectors.FieldSpec{{Key: "apiKey", Label: "Steam Web API key", Type: connectors.FieldPassword, Required: true, Secret: true,
			Help: "Create a user API key at steamcommunity.com/dev/apikey. Private friends and achievements may be unavailable."}},
		Validate: validateKey,
	})
	connectors.RegisterSource(&Connector{})
}

func (*Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: SourceType, Name: "Steam", CredentialTypes: []string{SourceType}, BrowseKinds: []string{},
		Capabilities: connectors.Capabilities{IncrementalMode: connectors.IncrementalPollingInventory, DeletionMode: connectors.DeletionScanGeneration},
		Fields: []connectors.FieldSpec{
			{Key: "steamId", Label: "SteamID64", Type: connectors.FieldString, Required: true, Effect: connectors.EffectRebaseline,
				Placeholder: "76561198188596121", Help: "Numeric Steam account ID. Syncs one profile and one document per game; successful empty libraries remove existing game documents."},
			{Key: "language", Label: "Achievement language", Type: connectors.FieldSelect, Default: "english", Options: languages, Effect: connectors.EffectRebaseline,
				Help: "Normal sync refreshes active and new games. Full reconciliation refreshes all games, including inactive games."},
			{Key: "includeAchievements", Label: "Include achievement details", Type: connectors.FieldBoolean, Default: false, Effect: connectors.EffectReconcile,
				Help: "When off, only achievement counts and progress are included."},
		}, FilterFields: []connectors.FilterFieldSpec{},
	}
}

func validateKey(cred connectors.Credential) error {
	k := strings.TrimSpace(cred.String("apiKey"))
	if len(k) != 32 {
		return errors.New("Steam API key must contain 32 hexadecimal characters")
	}
	for _, c := range k {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return errors.New("Steam API key must contain 32 hexadecimal characters")
		}
	}
	return nil
}

func settings(cfg map[string]any) (string, string, error) {
	sid, ok := cfg["steamId"].(string)
	n, err := strconv.ParseUint(sid, 10, 64)
	if !ok || len(sid) != 17 || err != nil || n>>56 != 1 || (n>>52)&15 != 1 || (n>>32)&0xfffff != 1 || uint32(n) == 0 {
		return "", "", &connectors.FieldError{Field: "steamId", Message: "Must be a valid individual SteamID64"}
	}
	language := connectors.AsString(cfg["language"])
	if language == "" {
		language = "english"
	}
	for _, option := range languages {
		if option.Value == language {
			return sid, language, nil
		}
	}
	return "", "", &connectors.FieldError{Field: "language", Message: "Unsupported Steam achievement language"}
}

func (c *Connector) ValidateConfig(cfg map[string]any, filter connectors.Filter) error {
	if _, _, err := settings(cfg); err != nil {
		return err
	}
	return connectors.ValidateFilter(filter, c.Info().FilterFields, false)
}

func (*Connector) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{Items: []connectors.BrowseItem{}, Breadcrumbs: []connectors.Breadcrumb{}}, nil
}

func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	client, err := c.client(cred)
	if err != nil {
		return "", err
	}
	var response playerResponse
	if err = client.get(ctx, "ISteamUser/GetPlayerSummaries/v2/", map[string]string{"steamids": "76561197960435530"}, &response); err != nil {
		return "", err
	}
	if response.Response == nil || len(response.Response.Players) != 1 {
		return "", errors.New("steam: invalid player summaries response")
	}
	return "Steam Web API key verified", nil
}
