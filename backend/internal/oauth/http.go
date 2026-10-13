package oauth

import (
	"context"

	"golang.org/x/oauth2"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

// oauthContext makes the oauth2 library use the shared transport.
func oauthContext(ctx context.Context, config proxy.Config) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, httpx.New(nil, nil, config).HTTP)
}
