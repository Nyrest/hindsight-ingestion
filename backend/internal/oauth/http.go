package oauth

import (
	"context"
	"net/http"

	"golang.org/x/oauth2"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

// oauthContext makes the oauth2 library use the shared transport.
func oauthContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: httpx.Shared})
}
