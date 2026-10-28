package notion

import (
	"context"
	"errors"
	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"testing"
	"time"
)

func TestClientsShareIntegrationPacing(t *testing.T) {
	cred := connectors.Credential{Config: map[string]any{"baseUrl": "https://pacing-test.invalid"}, Secrets: map[string]string{"token": "same-integration"}}
	if err := client(cred).Pace(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := client(cred).Pace(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("fresh client bypassed integration pacing", err)
	}
	cred.Secrets = map[string]string{"token": "other-integration"}
	if err := client(cred).Pace(t.Context()); err != nil {
		t.Fatal("independent integration was blocked", err)
	}
}
