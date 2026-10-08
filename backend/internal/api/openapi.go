package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

type schemaMap = map[string]any

type schemaBuilder struct{ schemas map[string]schemaMap }

// OpenAPIDocument derives the wire contract from registered routes and Go DTOs.
// Connector config maps remain provider-defined and are described by /connectors.
func OpenAPIDocument() ([]byte, error) {
	b := &schemaBuilder{schemas: map[string]schemaMap{}}
	paths := map[string]schemaMap{}
	for _, route := range (&Server{}).endpoints() {
		responses := schemaMap{strconv.Itoa(route.status): schemaMap{"description": http.StatusText(route.status)}}
		if route.response != nil {
			responses[strconv.Itoa(route.status)].(schemaMap)["content"] = b.content(route.response)
		}
		responses["default"] = schemaMap{"description": "API error", "content": b.content(apiError{})}
		if route.path != "/api/health" && route.path != "/api/oauth/callback" {
			responses["401"] = schemaMap{"description": "Basic Auth required", "content": schemaMap{"text/plain": schemaMap{"schema": schemaMap{"type": "string"}}}}
		}
		op := schemaMap{"responses": responses, "operationId": strings.ToLower(route.method) + strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_").Replace(route.path)}
		params := []schemaMap{}
		if strings.Contains(route.path, "{id}") {
			params = append(params, parameter("id", "path", "string", true))
		}
		if route.path == "/api/runs" || strings.HasSuffix(route.path, "/runs") {
			params = append(params, parameter("limit", "query", "integer", false), parameter("offset", "query", "integer", false), parameter("status", "query", "string", false), parameter("taskId", "query", "string", false))
		}
		if strings.HasSuffix(route.path, "/strategies") {
			params = append(params, parameter("bankId", "query", "string", true))
		}
		if route.path == "/api/tasks/{id}" && route.method == "DELETE" {
			params = append(params, parameter("deleteDocuments", "query", "boolean", false))
		}
		if strings.HasSuffix(route.path, "/tags") {
			params = append(params, parameter("bankId", "query", "string", true), parameter("q", "query", "string", false), parameter("limit", "query", "integer", false), parameter("offset", "query", "integer", false))
		}
		if route.path == "/api/oauth/callback" {
			params = append(params, parameter("state", "query", "string", true), parameter("code", "query", "string", false), parameter("error", "query", "string", false))
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if route.body != nil {
			schema := b.object(reflect.TypeOf(route.body))
			// Create handlers supply defaults; patches accept omitted properties.
			delete(schema, "required")
			if route.method == "POST" {
				switch route.path {
				case "/api/tasks":
					schema["required"] = []string{"name", "sourceType", "sourceCredentialId", "destinationCredentialId", "destinationBankId"}
				case "/api/credentials":
					schema["required"] = []string{"name", "type", "config"}
				case "/api/tasks/validate-cron":
					schema["required"] = []string{"cronExpression", "cronTimezone"}
				}
			}
			// Pointers denote omission in requests, rather than nullable response fields.
			for _, prop := range schema["properties"].(map[string]schemaMap) {
				if branches, ok := prop["anyOf"].([]schemaMap); ok {
					for k, v := range branches[0] {
						prop[k] = v
					}
					delete(prop, "anyOf")
				}
			}
			op["requestBody"] = schemaMap{"required": true, "content": schemaMap{"application/json": schemaMap{"schema": schema}}}
		}
		if route.path == "/api/health" || route.path == "/api/oauth/callback" {
			op["security"] = []any{}
		}
		if paths[route.path] == nil {
			paths[route.path] = schemaMap{}
		}
		paths[route.path][strings.ToLower(route.method)] = op
	}
	return json.MarshalIndent(schemaMap{
		"openapi": "3.1.0", "info": schemaMap{"title": "Hindsight Ingestion API", "version": httpx.Version},
		"paths": paths, "components": schemaMap{"schemas": b.schemas, "securitySchemes": schemaMap{"basicAuth": schemaMap{"type": "http", "scheme": "basic"}}},
		"security": []schemaMap{{"basicAuth": []string{}}},
	}, "", "  ")
}

func parameter(name, in, typ string, required bool) schemaMap {
	return schemaMap{"name": name, "in": in, "required": required, "schema": schemaMap{"type": typ}}
}

func (b *schemaBuilder) content(v any) schemaMap {
	return schemaMap{"application/json": schemaMap{"schema": b.schema(reflect.TypeOf(v))}}
}

func (b *schemaBuilder) schema(t reflect.Type) schemaMap {
	switch t.Kind() {
	case reflect.Pointer:
		return schemaMap{"anyOf": []schemaMap{b.schema(t.Elem()), {"type": "null"}}}
	case reflect.Interface:
		return schemaMap{}
	case reflect.String:
		return schemaMap{"type": "string"}
	case reflect.Bool:
		return schemaMap{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return schemaMap{"type": "integer"}
	case reflect.Slice:
		return schemaMap{"type": "array", "items": b.schema(t.Elem())}
	case reflect.Map:
		return schemaMap{"type": "object", "additionalProperties": b.schema(t.Elem())}
	case reflect.Struct:
		name := t.Name()
		if _, exists := b.schemas[name]; !exists {
			b.schemas[name] = schemaMap{}
			b.schemas[name] = b.object(t)
		}
		return schemaMap{"$ref": "#/components/schemas/" + name}
	default:
		panic("unsupported API type: " + t.String())
	}
}

func (b *schemaBuilder) object(t reflect.Type) schemaMap {
	props := map[string]schemaMap{}
	required := []string{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous {
			parent := b.object(field.Type)
			for k, v := range parent["properties"].(map[string]schemaMap) {
				props[k] = v
			}
			required = append(required, parent["required"].([]string)...)
			continue
		}
		name, option, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		prop := b.schema(field.Type)
		if enum := field.Tag.Get("enum"); enum != "" {
			if field.Type.Kind() == reflect.Slice {
				prop["items"].(schemaMap)["enum"] = strings.Split(enum, ",")
			} else {
				prop["enum"] = strings.Split(enum, ",")
			}
		}
		props[name] = prop
		if option != "omitempty" {
			required = append(required, name)
		}
	}
	return schemaMap{"type": "object", "properties": props, "required": required}
}

func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	doc, err := OpenAPIDocument()
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc)
}
