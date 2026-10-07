// Command openapi exports the backend's contract for client generation.
package main

import (
	"github.com/Nyrest/hindsight-ingestion/internal/api"
	"log"
	"os"
)

func main() {
	doc, err := api.OpenAPIDocument()
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) != 2 {
		log.Fatal("usage: openapi <output.json>")
	}
	if err := os.WriteFile(os.Args[1], append(doc, '\n'), 0644); err != nil {
		log.Fatal(err)
	}
}
