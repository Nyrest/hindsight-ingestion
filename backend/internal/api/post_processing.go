package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"github.com/Nyrest/hindsight-ingestion/internal/postprocess"
)

type postProcessTestBody struct {
	PostProcessing postprocess.Pipeline `json:"postProcessing"`
	// Input is the sample document text.
	Input string `json:"input"`
	// Base64 marks Input as base64-encoded binary content.
	Base64     bool   `json:"base64,omitempty"`
	TaskID     string `json:"taskId,omitempty"`
	TaskName   string `json:"taskName,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
	ItemName   string `json:"itemName,omitempty"`
}

type postProcessTestDTO struct {
	OK         bool     `json:"ok"`
	OutputType string   `json:"outputType" enum:"text,binary,skip,error"`
	Output     string   `json:"output"`
	FileName   string   `json:"fileName"`
	MIMEType   string   `json:"mimeType"`
	Context    string   `json:"context"`
	Error      string   `json:"error"`
	Code       string   `json:"code"`
	Logs       []logDTO `json:"logs"`
	DurationMs int64    `json:"durationMs"`
}

// testPostProcessing runs a (possibly unsaved) configuration on sample input.
func (s *Server) testPostProcessing(w http.ResponseWriter, r *http.Request) {
	var b postProcessTestBody
	if !decode(w, r, &b) {
		return
	}
	cfg := b.PostProcessing
	cfg.Enabled = true
	cfg, err := cfg.Normalize()
	var fe *postprocess.FieldError
	if errors.As(err, &fe) {
		writeValidation(w, "Please fix the highlighted fields", map[string]string{"postProcessing." + fe.Field: fe.Message})
		return
	} else if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	global, err := s.Settings.Get(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	doc := postprocess.Document{Text: b.Input, FileName: b.ItemName}
	if b.Base64 {
		data, err := base64.StdEncoding.DecodeString(b.Input)
		if err != nil {
			writeValidation(w, "Input is not valid base64", map[string]string{"input": err.Error()})
			return
		}
		doc = postprocess.Document{Binary: true, Data: data, FileName: b.ItemName}
	}
	out := postProcessTestDTO{Code: cfg.Script(), Logs: []logDTO{}}
	env := postprocess.Env{
		HTTP: &http.Client{Transport: httpx.Transport(global.Proxy)},
		Log: func(level, msg string) {
			if len(out.Logs) < 200 {
				out.Logs = append(out.Logs, logDTO{Time: time.Now().UTC().Format(time.RFC3339Nano), Level: level, Message: msg})
			}
		},
		Context: map[string]any{
			"task": map[string]any{"id": b.TaskID, "name": b.TaskName, "sourceType": b.SourceType, "bankId": ""},
			"run":  map[string]any{"id": "", "mode": "test", "dryRun": true, "full": false, "generation": 0},
			"item": map[string]any{"id": "test", "name": b.ItemName, "path": b.ItemName, "kind": "document", "revision": "",
				"modifiedAt": "", "mimeType": "", "size": len(doc.Data) + len(doc.Text), "tags": []any{}, "metadata": map[string]any{}, "attributes": map[string]any{}},
		},
	}
	started := time.Now()
	res, err := postprocess.Run(r.Context(), out.Code, env, doc)
	out.DurationMs = time.Since(started).Milliseconds()
	switch {
	case err != nil:
		out.OutputType, out.Error = "error", err.Error()
	case res.Skip:
		out.OK, out.OutputType = true, "skip"
	case res.Binary:
		out.OK, out.OutputType, out.Output = true, "binary", base64.StdEncoding.EncodeToString(res.Data)
	default:
		out.OK, out.OutputType, out.Output = true, "text", res.Text
	}
	out.FileName, out.MIMEType, out.Context = res.FileName, res.MIMEType, res.Context
	writeJSON(w, http.StatusOK, out)
}
