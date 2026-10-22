package postprocess

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, cfg Pipeline, doc Document) Result {
	t.Helper()
	cfg.Enabled = true
	cfg, err := cfg.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), cfg.Script(), Env{Context: map[string]any{"item": map[string]any{"name": "Note"}}}, doc)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, cfg.Script())
	}
	return res
}

func TestTextSteps(t *testing.T) {
	cfg := Pipeline{Steps: []PipelineStep{
		{Type: StepReplace, Enabled: true, Pattern: `(\d+)-(\d+)`, Replacement: "$2/$1", Regex: true},
		{Type: StepReplace, Enabled: true, Pattern: "a.b", Replacement: "$x"},
		{Type: StepReplace, Enabled: false, Pattern: "keep", Replacement: "gone"},
		{Type: StepRemoveLines, Enabled: true, Pattern: "^#", Regex: true},
		{Type: StepCollapseWhitespace, Enabled: true},
		{Type: StepTrim, Enabled: true, Side: "end", Characters: "."},
		{Type: StepPrepend, Enabled: true, Text: "Title: {{item.name}}", Separator: "\n"},
		{Type: StepCase, Enabled: true, Case: "upper"},
	}}
	res := run(t, cfg, Document{Text: "# comment\n12-34  a.b axb keep\n\n\n\nend..."})
	want := "TITLE: NOTE\n34/12 $X AXB KEEP\n\nEND"
	if res.Text != want {
		t.Fatalf("got %q, want %q", res.Text, want)
	}
}

func TestTruncateStripHTMLAndSkip(t *testing.T) {
	res := run(t, Pipeline{Steps: []PipelineStep{
		{Type: StepStripHTML, Enabled: true},
		{Type: StepTruncate, Enabled: true, MaxLength: 5, Suffix: "…"},
	}}, Document{Text: "<p>héllo&amp;world</p><script>x()</script>"})
	if res.Text != "héllo…" {
		t.Fatalf("got %q", res.Text)
	}
	res = run(t, Pipeline{Steps: []PipelineStep{{Type: StepSkip, Enabled: true, Pattern: "draft", IgnoreCase: true}}}, Document{Text: "DRAFT notes"})
	if !res.Skip {
		t.Fatal("expected skip")
	}
	res = run(t, Pipeline{Steps: []PipelineStep{{Type: StepSkip, Enabled: true, Pattern: "public", Invert: true}}}, Document{Text: "public notes"})
	if res.Skip || res.Text != "public notes" {
		t.Fatalf("unexpected %+v", res)
	}
}

func TestBinaryPassesThroughTextSteps(t *testing.T) {
	res := run(t, Pipeline{Steps: []PipelineStep{{Type: StepTrim, Enabled: true}}}, Document{Binary: true, Data: []byte{0, 1, 2}})
	if !res.Binary || string(res.Data) != "\x00\x01\x02" {
		t.Fatalf("unexpected %+v", res)
	}
}

func TestCodeModeBinaryToTextAndObjects(t *testing.T) {
	code := `async function process(context, input) {
  const text = new TextDecoder().decode(input);
  console.log("decoded", text.length);
  return { content: text.toUpperCase() + " " + context.document.contentType, fileName: "x.md", context: atob(btoa("hi")) };
}`
	var logs []string
	res, err := Run(context.Background(), code, Env{Log: func(level, msg string) { logs = append(logs, level+":"+msg) }},
		Document{Binary: true, Data: []byte("héllo")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "HÉLLO binary" || res.FileName != "x.md" || res.Context != "hi" {
		t.Fatalf("unexpected %+v", res)
	}
	if len(logs) != 1 || logs[0] != "info:decoded 5" {
		t.Fatalf("logs %v", logs)
	}
	res, err = Run(context.Background(), `function process(c, input) { return new TextEncoder().encode(input + "!"); }`, Env{}, Document{Text: "a"})
	if err != nil || !res.Binary || string(res.Data) != "a!" {
		t.Fatalf("unexpected %+v %v", res, err)
	}
}

func TestErrors(t *testing.T) {
	for script, want := range map[string]string{
		`function process() { throw new Error("boom"); }`:      "Error: boom",
		`async function process() { null.x; }`:                 "TypeError",
		`function other() {}`:                                  "Define a function named process",
		`function process() { return new Promise(() => {}); }`: "did not settle",
	} {
		_, err := Run(context.Background(), script, Env{}, Document{Text: "x"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v, want %q", script, err, want)
		}
	}
	if _, err := (Pipeline{Enabled: true, Mode: ModeCode, Code: "function ("}).Normalize(); err == nil {
		t.Fatal("expected a syntax error")
	}
	if _, err := (Pipeline{Steps: []PipelineStep{{Type: StepHTTP}}}).Normalize(); err == nil || !strings.Contains(err.Error(), "steps.0.url") {
		t.Fatalf("got %v", err)
	}
}

func TestTimeoutInterruptsAndBreaksVM(t *testing.T) {
	old := DocumentTimeout
	DocumentTimeout = 50 * time.Millisecond
	defer func() { DocumentTimeout = old }()
	pool := &Pool{MaxIdle: 2, IdleTTL: time.Minute}
	v, err := pool.Acquire("k", `function process() { for (;;) {} }`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Process(context.Background(), Env{}, Document{Text: "x"}); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("got %v", err)
	}
	pool.Release(v)
	if pool.Idle() != 0 {
		t.Fatal("an interrupted VM must not be reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, `function process() { for (;;) {} }`, Env{}, Document{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestPoolReusesAndEvicts(t *testing.T) {
	pool := &Pool{MaxIdle: 1, IdleTTL: 30 * time.Millisecond}
	script := `let calls = 0; function process(c, input) { calls++; return input + calls; }`
	v, err := pool.Acquire("a", script)
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := v.Process(context.Background(), Env{}, Document{Text: "x"}); res.Text != "x1" {
		t.Fatalf("got %q", res.Text)
	}
	pool.Release(v)
	again, _ := pool.Acquire("a", script)
	if again != v {
		t.Fatal("expected the idle VM to be reused")
	}
	if res, _ := again.Process(context.Background(), Env{}, Document{Text: "x"}); res.Text != "x2" {
		t.Fatalf("got %q", res.Text)
	}
	other, _ := pool.Acquire("b", script)
	pool.Release(again)
	pool.Release(other)
	if pool.Idle() != 1 {
		t.Fatalf("idle %d, want 1", pool.Idle())
	}
	deadline := time.Now().Add(2 * time.Second)
	for pool.Idle() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Idle() != 0 {
		t.Fatal("idle VMs were not evicted")
	}
}

func TestHTTPStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/json":
			if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Key") != "secret" || r.URL.Query().Get("name") != "Note" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"result":{"text":` + string(body[8:len(body)-1]) + `}}`))
		case "/form":
			form, _ := url.ParseQuery(string(body))
			_, _ = w.Write([]byte(form.Get("content") + "|" + r.Header.Get("Content-Type")))
		case "/bytes":
			_, _ = w.Write(append(body, '!'))
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("nope"))
		}
	}))
	defer srv.Close()

	res := run(t, Pipeline{Steps: []PipelineStep{{Type: StepHTTP, Enabled: true, URL: srv.URL + "/json", BodyType: "json",
		Body: `{"text":"{{input}}"}`, ResponseMode: "json", ResponsePath: "result.text",
		Headers: []KeyValue{{"X-Key", "secret"}}, Query: []KeyValue{{"name", "{{item.name}}"}}}}}, Document{Text: "line \"1\"\nline 2"})
	if res.Text != "line \"1\"\nline 2" {
		t.Fatalf("json: got %q", res.Text)
	}
	res = run(t, Pipeline{Steps: []PipelineStep{{Type: StepHTTP, Enabled: true, URL: srv.URL + "/form", BodyType: "form",
		Form: []KeyValue{{"content", "{{input}} & more"}}}}}, Document{Text: "a b"})
	if res.Text != "a b & more|application/x-www-form-urlencoded;charset=UTF-8" {
		t.Fatalf("form: got %q", res.Text)
	}
	res = run(t, Pipeline{Steps: []PipelineStep{{Type: StepHTTP, Enabled: true, Method: "PUT", URL: srv.URL + "/bytes", BodyType: "input", ResponseMode: "binary"}}},
		Document{Binary: true, Data: []byte{1, 2}})
	if !res.Binary || string(res.Data) != "\x01\x02!" {
		t.Fatalf("binary: got %+v", res)
	}
	res = run(t, Pipeline{Steps: []PipelineStep{{Type: StepHTTP, Enabled: true, URL: srv.URL + "/bytes", BodyType: "input", ResponseMode: "ignore"}}}, Document{Text: "keep"})
	if res.Text != "keep" {
		t.Fatalf("ignore: got %q", res.Text)
	}
	_, err := Run(context.Background(), Pipeline{Enabled: true, Steps: []PipelineStep{{Type: StepHTTP, Enabled: true, Method: "GET", URL: srv.URL + "/missing", BodyType: "none", ResponseMode: "text"}}}.Script(), Env{}, Document{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "418") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
	_, err = Run(context.Background(), `async function process() { const r = await fetch("`+srv.URL+`/missing"); return r.status + " " + r.headers.get("content-type") + " " + await r.text(); }`, Env{}, Document{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerate(t *testing.T) {
	code := Generate([]PipelineStep{{ID: "1", Type: StepCollapseWhitespace, Enabled: true}, {ID: "2", Type: StepReplace, Enabled: true, Pattern: "<a>", Replacement: "b"}})
	want := "// Generated from post-processing steps.\nasync function process(context, input) {\n" +
		"  input = await steps.collapseWhitespace(context, input, {});\n" +
		"  input = await steps.replace(context, input, {\"pattern\":\"<a>\",\"replacement\":\"b\"});\n" +
		"  return input;\n}\n"
	if code != want {
		t.Fatalf("got\n%s", code)
	}
	if (Pipeline{Mode: ModeCode, Code: "x"}).Script() != "" {
		t.Fatal("disabled config must have no script")
	}
}

func TestInitializationRespectsDeadlineAndCancellation(t *testing.T) {
	old := DocumentTimeout
	DocumentTimeout = 30 * time.Millisecond
	defer func() { DocumentTimeout = old }()
	_, err := Run(t.Context(), `while (true) {} function process(c, input) { return input; }`, Env{}, Document{})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("top-level loop escaped deadline: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = Run(ctx, `console.log("started"); while (true) {} function process(c, input) { return input; }`,
		Env{Log: func(string, string) { cancel() }}, Document{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("top-level loop ignored cancellation: %v", err)
	}
	_, err = Run(ctx, `throw new Error("must not execute");`, Env{}, Document{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled invocation initialized code: %v", err)
	}
}

func TestDocumentDeadlineCancelsFetch(t *testing.T) {
	old := DocumentTimeout
	DocumentTimeout = 100 * time.Millisecond
	defer func() { DocumentTimeout = old }()
	for _, location := range []string{"initialization", "process", "response body"} {
		t.Run(location, func(t *testing.T) {
			cancelled := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if location == "response body" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
					close(cancelled)
				case <-time.After(2 * time.Second):
				}
			}))
			defer srv.Close()
			request := `fetch("` + srv.URL + `", { timeout: 10000 })`
			script := `async function process() { return (await ` + request + `).text(); }`
			if location == "initialization" {
				script = request + `; function process(c, input) { return input; }`
			}
			_, err := Run(t.Context(), script, Env{}, Document{})
			if err == nil || !strings.Contains(err.Error(), "exceeded") {
				t.Fatalf("document deadline not reported: %v", err)
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("document deadline did not cancel the HTTP request")
			}
		})
	}
}

func TestFetchRedirectProtectsHeaders(t *testing.T) {
	var sourceURL string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Authorization", "Cookie", "X-Api-Key", "CF-Access-Client-Secret"} {
			if r.Header.Get(name) != "" {
				t.Errorf("redirect leaked %s", name)
			}
		}
		http.Redirect(w, r, sourceURL+"/return", http.StatusFound)
	}))
	defer foreign.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/same", http.StatusFound)
		case "/same":
			if r.Header.Get("X-Api-Key") != "api-secret" || r.Header.Get("Authorization") != "Bearer auth-secret" {
				t.Error("same-origin redirect lost credentials")
			}
			http.Redirect(w, r, foreign.URL, http.StatusFound)
		case "/return":
			if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("return hop restored credentials")
			}
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer source.Close()
	sourceURL = source.URL
	for _, client := range []*http.Client{nil, {Transport: http.DefaultTransport}} {
		res, err := Run(t.Context(), `async function process() {
  return (await fetch("`+sourceURL+`/start", { headers: {
    "X-Api-Key": "api-secret", "CF-Access-Client-Secret": "cf-secret",
    "Authorization": "Bearer auth-secret", "Cookie": "session=secret"
  } })).text();
}`, Env{HTTP: client}, Document{})
		if err != nil || res.Text != "ok" {
			t.Fatalf("redirect result: %+v %v", res, err)
		}
	}
}

func TestTrimTreatsCharactersLiterally(t *testing.T) {
	for _, characters := range []string{"a-z", "z-a", "-[]^\\", ".-*"} {
		res := run(t, Pipeline{Steps: []PipelineStep{{Type: StepTrim, Enabled: true, Characters: characters}}},
			Document{Text: characters + "hello" + characters})
		if res.Text != "hello" {
			t.Fatalf("trim %q returned %q", characters, res.Text)
		}
	}
}

func TestFetchRejectsHTTPSDowngrade(t *testing.T) {
	contacted := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted <- struct{}{}
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	_, err := Run(t.Context(), `async function process() { return (await fetch("`+source.URL+`")).text(); }`, Env{HTTP: source.Client()}, Document{})
	if err == nil || !strings.Contains(err.Error(), "HTTPS downgrade refused") {
		t.Fatalf("HTTPS downgrade was not refused: %v", err)
	}
	select {
	case <-contacted:
		t.Fatal("contacted insecure redirect target")
	default:
	}
}
