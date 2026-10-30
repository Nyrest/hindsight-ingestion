package postprocess

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"

	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

//go:embed prelude.js
var preludeSource string

// The prelude is parsed once per process, on first use.
var prelude = sync.OnceValues(func() (*goja.Program, error) {
	return goja.Compile("prelude.js", preludeSource, true)
})

// Tunables.
var (
	// FetchTimeout is the default timeout of one fetch() request.
	FetchTimeout = 60 * time.Second
	// MaxResponseBytes bounds a fetch() response body.
	MaxResponseBytes int64 = 64 << 20
	maxLogMessage          = 2000
	maxCallStack           = 2048
)

// MaxDocumentBytes is independent of the streaming file limit: scripts and
// attachment serialization require multiple in-memory copies.
const MaxDocumentBytes = 16 << 20
const MaxVMDocuments = 128
const MaxVMBytes = 32 << 20
const MaxVMLifetime = 10 * time.Minute

// Document is the content handed to process(). Data is set for binary input.
type Document struct {
	Text     string
	Data     []byte
	Binary   bool
	FileName string
	MIMEType string
	Context  string
}

// Result is the normalized return value of process().
type Result struct {
	Skip     bool
	Binary   bool
	Text     string
	Data     []byte
	FileName string
	MIMEType string
	Context  string
}

// Env supplies per-call information and host services.
type Env struct {
	// Context is exposed to the script as its first argument (task, run, item…).
	Context map[string]any
	// Log receives console output.
	Log func(level, msg string)
	// HTTP performs fetch() requests.
	HTTP *http.Client
}

// VM is a goja runtime with the prelude and one user script prepared. A VM is
// used by one goroutine at a time.
type VM struct {
	key            string
	rt             *goja.Runtime
	run            goja.Callable
	program        *goja.Program
	ctx            context.Context
	env            Env
	broken         bool
	idle           time.Time
	created        time.Time
	documents      int
	processedBytes int64
}

func newVM(key, script string) (*VM, error) {
	pre, err := prelude()
	if err != nil {
		return nil, fmt.Errorf("post-processing prelude: %w", err)
	}
	prog, err := compile(script)
	if err != nil {
		return nil, fmt.Errorf("compile post-processing code: %w", err)
	}
	v := &VM{key: key, rt: goja.New(), program: prog, ctx: context.Background(), created: time.Now()}
	v.rt.SetMaxCallStackSize(maxCallStack)
	for name, fn := range map[string]func(goja.FunctionCall) goja.Value{
		"__fetch": v.fetch, "__log": v.log, "__utf8Encode": v.utf8Encode, "__utf8Decode": v.utf8Decode,
		"__atob": v.atob, "__btoa": v.btoa,
		"__extract": v.extract,
	} {
		if err := v.rt.Set(name, fn); err != nil {
			return nil, err
		}
	}
	if _, err := v.rt.RunProgram(pre); err != nil {
		return nil, fmt.Errorf("post-processing prelude: %w", err)
	}
	run, ok := goja.AssertFunction(v.rt.Get("__run"))
	if !ok {
		return nil, errors.New("post-processing prelude is missing __run")
	}
	v.run = run
	return v, nil
}

// Process runs process(context, input) on one document.
func (v *VM) Process(ctx context.Context, env Env, doc Document) (res Result, err error) {
	if v.Expired() {
		v.broken = true
		return res, errors.New("post-processing runtime reached its reuse limit")
	}
	if len(doc.Text)+len(doc.Data) > MaxDocumentBytes {
		return res, fmt.Errorf("post-processing input exceeds %d MiB", MaxDocumentBytes>>20)
	}
	v.documents++
	defer func() { v.processedBytes += int64(len(doc.Text) + len(doc.Data) + len(res.Text) + len(res.Data)) }()
	v.ctx, v.env = ctx, env
	defer func() { v.ctx, v.env = context.Background(), Env{} }()

	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		v.rt.Interrupt(context.Cause(ctx))
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		v.rt.ClearInterrupt()
	}()
	if ctx.Err() != nil {
		v.broken = true
		return res, context.Cause(ctx)
	}
	if v.program != nil {
		if _, err := v.rt.RunProgram(v.program); err != nil {
			v.broken = true
			if ctx.Err() != nil {
				return res, context.Cause(ctx)
			}
			return res, fmt.Errorf("load post-processing code: %s", describe(err))
		}
		v.program = nil
	}

	var input goja.Value
	if doc.Binary {
		input = v.rt.ToValue(v.rt.NewArrayBuffer(doc.Data))
	} else {
		input = v.rt.ToValue(doc.Text)
	}
	jsCtx := map[string]any{}
	for k, val := range env.Context {
		jsCtx[k] = val
	}
	contentType := "text"
	if doc.Binary {
		contentType = "binary"
	}
	jsCtx["document"] = map[string]any{
		"contentType": contentType, "fileName": doc.FileName, "mimeType": doc.MIMEType,
		"context": doc.Context, "size": len(doc.Data) + len(doc.Text),
	}
	out, err := v.run(goja.Undefined(), v.rt.ToValue(jsCtx), input)
	if ctxErr := ctx.Err(); ctxErr != nil {
		v.broken = true
		return res, context.Cause(ctx)
	}
	if err != nil {
		v.markBroken(err)
		return res, errors.New(describe(err))
	}
	promise, ok := out.Export().(*goja.Promise)
	if !ok {
		return res, errors.New("post-processing returned an unexpected value")
	}
	switch promise.State() {
	case goja.PromiseStateRejected:
		return res, errors.New(describeValue(promise.Result()))
	case goja.PromiseStatePending:
		v.broken = true
		return res, errors.New("process() did not settle; only fetch() and steps are asynchronous")
	}
	return v.result(promise.Result().ToObject(v.rt))
}

func (v *VM) result(obj *goja.Object) (Result, error) {
	str := func(name string) string {
		val := obj.Get(name)
		if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
			return ""
		}
		return val.String()
	}
	res := Result{FileName: str("fileName"), MIMEType: str("mimeType"), Context: str("context")}
	switch str("kind") {
	case "skip":
		res.Skip = true
	case "binary":
		var data []byte
		if err := v.rt.ExportTo(obj.Get("data"), &data); err != nil {
			return res, fmt.Errorf("read binary output: %w", err)
		}
		if len(data) > MaxDocumentBytes {
			v.broken = true
			return res, fmt.Errorf("post-processing output exceeds %d MiB", MaxDocumentBytes>>20)
		}
		res.Binary, res.Data = true, bytes.Clone(data)
	default:
		res.Text = str("text")
		if len(res.Text) > MaxDocumentBytes {
			v.broken = true
			return Result{}, fmt.Errorf("post-processing output exceeds %d MiB", MaxDocumentBytes>>20)
		}
		// Small substrings must not pin a much larger source document.
		res.Text = strings.Clone(res.Text)
	}
	return res, nil
}

// Broken reports whether the VM was interrupted and must not be reused.
func (v *VM) Broken() bool { return v.broken }

func (v *VM) markBroken(err error) {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		v.broken = true
	}
}

func (v *VM) throw(format string, args ...any) {
	panic(v.rt.NewTypeError(fmt.Sprintf(format, args...)))
}

func (v *VM) log(call goja.FunctionCall) goja.Value {
	if v.env.Log != nil {
		msg := call.Argument(1).String()
		if len(msg) > maxLogMessage {
			msg = msg[:maxLogMessage] + "…"
		}
		v.env.Log(call.Argument(0).String(), msg)
	}
	return goja.Undefined()
}

func (v *VM) bytesArg(val goja.Value) []byte {
	if ab, ok := val.Export().(goja.ArrayBuffer); ok {
		return ab.Bytes()
	}
	var data []byte
	if err := v.rt.ExportTo(val, &data); err != nil {
		v.throw("expected binary data")
	}
	return data
}

func (v *VM) utf8Encode(call goja.FunctionCall) goja.Value {
	return v.rt.ToValue(v.rt.NewArrayBuffer([]byte(call.Argument(0).String())))
}

func (v *VM) utf8Decode(call goja.FunctionCall) goja.Value {
	data := v.bytesArg(call.Argument(0))
	if !utf8.Valid(data) {
		data = bytes.ToValidUTF8(data, []byte("�"))
	}
	return v.rt.ToValue(string(data))
}

// btoa encodes a binary string (code units 0-255) as base64.
func (v *VM) btoa(call goja.FunctionCall) goja.Value {
	s := call.Argument(0).String()
	raw := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			v.throw("btoa: string contains characters outside of the Latin1 range")
		}
		raw = append(raw, byte(r))
	}
	return v.rt.ToValue(base64.StdEncoding.EncodeToString(raw))
}

// atob decodes base64 into a binary string.
func (v *VM) atob(call goja.FunctionCall) goja.Value {
	s := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, call.Argument(0).String())
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	if err != nil {
		v.throw("atob: invalid base64")
	}
	runes := make([]rune, len(raw))
	for i, b := range raw {
		runes[i] = rune(b)
	}
	return v.rt.ToValue(string(runes))
}

// fetch performs a blocking HTTP request; the prelude wraps it in a Promise.
func (v *VM) fetch(call goja.FunctionCall) goja.Value {
	req := call.Argument(0).ToObject(v.rt)
	url := req.Get("url").String()
	method := req.Get("method").String()
	timeout := FetchTimeout
	if t := req.Get("timeout"); t != nil && !goja.IsUndefined(t) && !goja.IsNull(t) && t.ToFloat() > 0 {
		timeout = time.Duration(t.ToFloat() * float64(time.Millisecond))
	}
	var body io.Reader
	if b := req.Get("body"); b != nil && !goja.IsUndefined(b) && !goja.IsNull(b) {
		if s, ok := b.Export().(string); ok {
			body = strings.NewReader(s)
		} else {
			body = bytes.NewReader(v.bytesArg(b))
		}
	}
	ctx, cancel := context.WithTimeout(v.ctx, timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		v.throw("fetch: %v", err)
	}
	var headers [][]string
	if err := v.rt.ExportTo(req.Get("headers"), &headers); err == nil {
		for _, h := range headers {
			if len(h) == 2 {
				httpReq.Header.Set(h[0], h[1])
			}
		}
	}
	if httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", httpx.UserAgent)
	}
	resp, data, err := v.request(httpReq)
	if err != nil {
		v.throw("fetch: %v", err)
	}
	respHeaders := make([]any, 0, len(resp.Header))
	for k, vals := range resp.Header {
		respHeaders = append(respHeaders, []any{strings.ToLower(k), strings.Join(vals, ", ")})
	}
	statusText := strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode)))
	return v.rt.ToValue(map[string]any{
		"status": resp.StatusCode, "statusText": statusText, "url": resp.Request.URL.String(),
		"headers": respHeaders, "body": v.rt.NewArrayBuffer(data),
	})
}

func (v *VM) request(httpReq *http.Request) (*http.Response, []byte, error) {
	client := v.env.HTTP
	if client == nil {
		client = &http.Client{Transport: httpx.Shared}
	}
	requestClient := *client
	custom := make(map[string]string, len(httpReq.Header))
	for name := range httpReq.Header {
		if name != "User-Agent" && name != "Content-Type" {
			custom[name] = httpReq.Header.Get(name)
		}
	}
	guard := &httpx.Client{CustomHeaders: custom}
	requestClient.CheckRedirect = guard.CheckRedirect
	resp, err := requestClient.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	v.processedBytes += int64(len(data))
	if int64(len(data)) > MaxResponseBytes {
		return nil, nil, fmt.Errorf("response exceeds %d MB", MaxResponseBytes>>20)
	}
	return resp, data, nil
}

func describe(err error) string {
	var exc *goja.Exception
	if errors.As(err, &exc) {
		return describeValue(exc.Value())
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return fmt.Sprint(interrupted.Value())
	}
	return err.Error()
}

func describeValue(val goja.Value) string {
	if obj, ok := val.(*goja.Object); ok {
		if msg := obj.Get("message"); msg != nil && !goja.IsUndefined(msg) {
			name := "Error"
			if n := obj.Get("name"); n != nil && !goja.IsUndefined(n) {
				name = n.String()
			}
			return name + ": " + msg.String()
		}
	}
	return val.String()
}

// Expired bounds state retained by user-script globals even under continuous use.
func (v *VM) Expired() bool {
	return v.documents >= MaxVMDocuments || v.processedBytes >= MaxVMBytes || time.Since(v.created) >= MaxVMLifetime
}
