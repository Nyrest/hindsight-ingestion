package api

import (
	"bytes"
	"os"
	"testing"
)

func TestOpenAPIContractIsCurrent(t *testing.T) {
	doc, err := OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("../../../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(doc), bytes.TrimSpace(committed)) {
		t.Fatal("OpenAPI contract is stale; run go generate ./internal/api and bun run generate:api")
	}
}
