package googleauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func TestSubjectUsesAuthenticatedUserInfo(t *testing.T) {
	for _, response := range []string{`{"sub":"account"}`, `{}`, `{"sub":""}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fresh" {
					t.Error("missing refreshed bearer token")
				}
				io.WriteString(w, response)
			}))
			defer server.Close()
			previous := userInfoURL
			userInfoURL = server.URL
			defer func() { userInfoURL = previous }()
			cred := connectors.Credential{OAuth: &connectors.OAuthToken{AccessToken: "old"}, AccessToken: func(_ context.Context) (string, error) { return "fresh", nil }}
			subject, err := Subject(t.Context(), cred)
			if response == `{"sub":"account"}` {
				if err != nil || subject != "account" {
					t.Fatal(subject, err)
				}
			} else if err == nil {
				t.Fatal("missing subject accepted")
			}
		})
	}
	if _, err := Subject(t.Context(), connectors.Credential{}); err == nil {
		t.Fatal("disconnected credential accepted")
	}
}
