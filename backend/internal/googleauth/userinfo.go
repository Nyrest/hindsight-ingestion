// Package googleauth resolves the account behind a Google OAuth access token.
package googleauth

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

var userInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

func Subject(ctx context.Context, cred connectors.Credential) (string, error) {
	if cred.OAuth == nil || cred.OAuth.AccessToken == "" {
		return "", errors.New("not connected; use OAuth Connect")
	}
	client := httpx.New(cred.Headers, map[string]string{"Authorization": "Bearer " + cred.OAuth.AccessToken}, cred.Proxy)
	client.Token = cred.AccessToken
	var profile struct {
		Subject string `json:"sub"`
	}
	if err := client.JSON(ctx, http.MethodGet, userInfoURL, nil, &profile); err != nil {
		return "", err
	}
	if profile.Subject == "" || len(profile.Subject) > 255 {
		return "", errors.New("Google did not return a valid account subject")
	}
	return profile.Subject, nil
}
