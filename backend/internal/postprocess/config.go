// Package postprocess transforms document content between the source and
// Hindsight with a user-defined JavaScript pipeline executed by goja.
//
// Steps configured in the UI are compiled to the same JavaScript a user can
// write in code mode, so a single execution path serves both modes.
package postprocess

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"github.com/google/uuid"
)

// Modes.
const (
	ModeUI   = "ui"
	ModeCode = "code"
)

// Step types.
const (
	StepReplace            = "replace"
	StepTrim               = "trim"
	StepPrepend            = "prepend"
	StepAppend             = "append"
	StepTruncate           = "truncate"
	StepCase               = "case"
	StepCollapseWhitespace = "collapse_whitespace"
	StepStripHTML          = "strip_html"
	StepRemoveLines        = "remove_lines"
	StepSkip               = "skip"
	StepHTTP               = "http"
)

var stepTypes = []string{StepReplace, StepTrim, StepPrepend, StepAppend, StepTruncate, StepCase,
	StepCollapseWhitespace, StepStripHTML, StepRemoveLines, StepSkip, StepHTTP}

// Limits.
const (
	MaxSteps    = 50
	MaxCodeSize = 256 << 10
)

// ProcessDocumentation describes the complete function contract in the editor.
const ProcessDocumentation = `/**
 * Transforms one document before it is retained to Hindsight.
 * Return a string or binary data to retain, or null to skip the document.
 *
 * @param {ProcessContext} context Task, run, source item and document information.
 * @param {ProcessInput} input Document text, or binary file data.
 * @returns {Promise<ProcessOutput>} Content to retain, optional file metadata and context, or null/undefined to skip.
 */
`

// DefaultCode is the code mode template.
const DefaultCode = ProcessDocumentation + `async function process(context, input) {
  return input;
}
`

// KeyValue is an ordered name/value pair (headers, query parameters, form fields).
type KeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PipelineStep is one UI pipeline step. Only the fields of its type are meaningful.
type PipelineStep struct {
	ID      string `json:"id"`
	Type    string `json:"type" enum:"replace,trim,prepend,append,truncate,case,collapse_whitespace,strip_html,remove_lines,skip,http"`
	Enabled bool   `json:"enabled"`

	// replace, remove_lines, skip
	Pattern     string `json:"pattern,omitempty"`
	Replacement string `json:"replacement,omitempty"`
	Regex       bool   `json:"regex,omitempty"`
	IgnoreCase  bool   `json:"ignoreCase,omitempty"`
	// Invert keeps matching lines (remove_lines) or skips non-matching documents (skip).
	Invert bool `json:"invert,omitempty"`
	// trim
	Side       string `json:"side,omitempty" enum:"both,start,end"`
	Characters string `json:"characters,omitempty"`
	// prepend, append (templated)
	Text      string `json:"text,omitempty"`
	Separator string `json:"separator,omitempty"`
	// truncate
	MaxLength int    `json:"maxLength,omitempty"`
	Suffix    string `json:"suffix,omitempty"`
	// case
	Case string `json:"case,omitempty" enum:"lower,upper"`
	// http
	Method       string     `json:"method,omitempty" enum:"GET,POST,PUT,PATCH,DELETE"`
	URL          string     `json:"url,omitempty"`
	Headers      []KeyValue `json:"headers,omitempty"`
	Query        []KeyValue `json:"query,omitempty"`
	BodyType     string     `json:"bodyType,omitempty" enum:"none,input,text,json,form"`
	Body         string     `json:"body,omitempty"`
	Form         []KeyValue `json:"form,omitempty"`
	ResponseMode string     `json:"responseMode,omitempty" enum:"text,json,binary,ignore"`
	ResponsePath string     `json:"responsePath,omitempty"`
}

// Pipeline is a task's post-processing configuration.
type Pipeline struct {
	Enabled bool           `json:"enabled"`
	Mode    string         `json:"mode" enum:"ui,code"`
	Steps   []PipelineStep `json:"steps"`
	Code    string         `json:"code"`
}

// Default returns the configuration of a task without post-processing.
func Default() Pipeline { return Pipeline{Mode: ModeUI, Steps: []PipelineStep{}, Code: ""} }

// Parse decodes a stored configuration; invalid or empty JSON yields Default.
func Parse(raw string) Pipeline {
	cfg := Default()
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	if cfg.Steps == nil {
		cfg.Steps = []PipelineStep{}
	}
	if cfg.Mode != ModeCode {
		cfg.Mode = ModeUI
	}
	return cfg
}

// FieldError identifies an invalid configuration field relative to the config root.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Normalize validates the configuration and fills defaults.
func (c Pipeline) Normalize() (Pipeline, error) {
	if c.Mode == "" {
		c.Mode = ModeUI
	}
	if c.Mode != ModeUI && c.Mode != ModeCode {
		return c, &FieldError{"mode", "Must be ui or code"}
	}
	if len(c.Steps) > MaxSteps {
		return c, &FieldError{"steps", fmt.Sprintf("At most %d steps are allowed", MaxSteps)}
	}
	if len(c.Code) > MaxCodeSize {
		return c, &FieldError{"code", fmt.Sprintf("Code must not exceed %d KB", MaxCodeSize>>10)}
	}
	steps := make([]PipelineStep, 0, len(c.Steps))
	for i, s := range c.Steps {
		s, err := s.normalize()
		if err != nil {
			err.Field = "steps." + strconv.Itoa(i) + "." + err.Field
			return c, err
		}
		steps = append(steps, s)
	}
	c.Steps = steps
	if c.Enabled && c.Mode == ModeCode {
		if strings.TrimSpace(c.Code) == "" {
			return c, &FieldError{"code", "Code is required"}
		}
		if _, err := compile(c.Code); err != nil {
			return c, &FieldError{"code", err.Error()}
		}
	}
	return c, nil
}

func (s PipelineStep) normalize() (PipelineStep, *FieldError) {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if !slices.Contains(stepTypes, s.Type) {
		return s, &FieldError{"type", "Unknown step type"}
	}
	switch s.Type {
	case StepReplace, StepRemoveLines, StepSkip:
		if s.Pattern == "" {
			return s, &FieldError{"pattern", "Pattern is required"}
		}
	case StepTrim:
		if s.Side == "" {
			s.Side = "both"
		}
		if s.Side != "both" && s.Side != "start" && s.Side != "end" {
			return s, &FieldError{"side", "Must be both, start or end"}
		}
	case StepTruncate:
		if s.MaxLength <= 0 {
			return s, &FieldError{"maxLength", "Must be a positive number"}
		}
	case StepCase:
		if s.Case == "" {
			s.Case = "lower"
		}
		if s.Case != "lower" && s.Case != "upper" {
			return s, &FieldError{"case", "Must be lower or upper"}
		}
	case StepHTTP:
		s.URL = strings.TrimSpace(s.URL)
		if s.URL == "" {
			return s, &FieldError{"url", "URL is required"}
		}
		s.Method = strings.ToUpper(s.Method)
		if s.Method == "" {
			s.Method = "POST"
		}
		if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, s.Method) {
			return s, &FieldError{"method", "Unsupported HTTP method"}
		}
		if s.BodyType == "" {
			s.BodyType = "none"
		}
		if !slices.Contains([]string{"none", "input", "text", "json", "form"}, s.BodyType) {
			return s, &FieldError{"bodyType", "Unsupported body type"}
		}
		if s.ResponseMode == "" {
			s.ResponseMode = "text"
		}
		if !slices.Contains([]string{"text", "json", "binary", "ignore"}, s.ResponseMode) {
			return s, &FieldError{"responseMode", "Unsupported response mode"}
		}
	}
	return s, nil
}

// Script returns the JavaScript executed for this configuration, or "" when
// post-processing is disabled.
func (c Pipeline) Script() string {
	if !c.Enabled {
		return ""
	}
	if c.Mode == ModeCode {
		return c.Code
	}
	return Generate(c.Steps)
}

// ExecutionKey identifies the active logic, ignoring disabled UI steps.
func (c Pipeline) ExecutionKey() string {
	if !c.Enabled || c.Mode == ModeCode {
		return c.Script()
	}
	steps := make([]PipelineStep, 0, len(c.Steps))
	for _, step := range c.Steps {
		if step.Enabled {
			steps = append(steps, step)
		}
	}
	return Generate(steps)
}

// Generate compiles UI steps to code mode JavaScript. Disabled steps stay commented.
func Generate(steps []PipelineStep) string {
	var b strings.Builder
	b.WriteString("// Generated from post-processing steps.\n")
	b.WriteString(ProcessDocumentation)
	b.WriteString("async function process(context, input) {\n")
	for _, s := range steps {
		prefix := "  "
		if !s.Enabled {
			prefix += "// "
		}
		fmt.Fprintf(&b, "%sinput = await steps.%s(context, input, %s);\n", prefix, stepFunction(s.Type), stepOptions(s))
	}
	b.WriteString("  return input;\n}\n")
	return b.String()
}

func stepFunction(t string) string {
	parts := strings.Split(t, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// stepOptions renders the step's fields (without identity) as a JSON object
// literal with sorted keys.
func stepOptions(s PipelineStep) string {
	s.ID, s.Type, s.Enabled = "", "", false
	raw, _ := json.Marshal(s)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "id")
	delete(fields, "type")
	delete(fields, "enabled")
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(fields)
	return strings.TrimSpace(out.String())
}

// Key identifies a script for runtime reuse.
func Key(scope, script string) string {
	sum := sha256.Sum256([]byte(script))
	return scope + ":" + hex.EncodeToString(sum[:])
}

func compile(script string) (*goja.Program, error) {
	return goja.Compile("process.js", script, false)
}
