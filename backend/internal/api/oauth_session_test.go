package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/credentials"
	"github.com/Nyrest/hindsight-ingestion/internal/models"
)

func TestOAuthCallbackRequiresInitiatingBrowser(t *testing.T) {
	e := newEnv(t)
	name := "Google"
	row := models.Credential{ID: "oauth-account"}
	if err := e.creds.Apply(&row, credentials.Input{Name: &name, Type: "google_drive", Config: map[string]any{"clientId": "client", "clientSecret": "secret"}}, true); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/credentials/"+row.ID+"/oauth/start", nil)
	req.Header.Set("Origin", "https://browser.test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var start struct{ AuthURL string }
	err = json.NewDecoder(resp.Body).Decode(&start)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("start status=%d, error=%v", resp.StatusCode, err)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/api/oauth/callback" || cookies[0].MaxAge != 600 {
		t.Fatalf("insecure OAuth cookie: %+v", cookies)
	}
	authorization, _ := url.Parse(start.AuthURL)
	state := authorization.Query().Get("state")
	if state == "" || cookies[0].Value != state {
		t.Fatal("authorization state did not match its browser cookie")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, cookie := range []*http.Cookie{nil, {Name: "hi-oauth-state", Value: "different-session"}} {
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/oauth/callback?state="+url.QueryEscape(state)+"&code=test-code", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "Invalid+OAuth+session") {
			t.Fatalf("callback accepted another browser: status=%d", resp.StatusCode)
		}
		if err := e.db.First(&row, "id = ?", row.ID).Error; err != nil || row.OAuthState != state {
			t.Fatal("invalid browser consumed OAuth state", err)
		}
	}
}
