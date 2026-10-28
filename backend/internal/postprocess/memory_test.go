package postprocess

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTruncateCodePointsAndBoundary(t *testing.T) {
	for _, tc := range []struct {
		input string
		limit int
		want  string
	}{
		{"😀é中x", 3, "😀é中…"}, {"😀é中", 3, "😀é中"}, {"abc", 8, "abc"}, {"", 1, ""},
	} {
		got := run(t, Pipeline{Steps: []PipelineStep{{Type: StepTruncate, Enabled: true, MaxLength: tc.limit, Suffix: "…"}}}, Document{Text: tc.input})
		if got.Text != tc.want {
			t.Fatalf("truncate(%q,%d)=%q, want %q", tc.input, tc.limit, got.Text, tc.want)
		}
	}
}

func BenchmarkTruncateLargeInput(b *testing.B) {
	v, err := newVM("", `function process(c,i){return steps.truncate(c,i,{maxLength:80});}`)
	if err != nil {
		b.Fatal(err)
	}
	doc := Document{Text: strings.Repeat("x", 1<<20)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v.Expired() {
			v, err = newVM("", `function process(c,i){return steps.truncate(c,i,{maxLength:80});}`)
			if err != nil {
				b.Fatal(err)
			}
		}
		out, err := v.Process(context.Background(), Env{}, doc)
		if err != nil || len(out.Text) != 80 {
			b.Fatal(err, len(out.Text))
		}
	}
}

func TestPoolClearsRemovedPointersAndDiscardsExpiredVM(t *testing.T) {
	p := &Pool{MaxIdle: 2, IdleTTL: time.Hour}
	v, err := p.Acquire("a", `function process(c,i){return i;}`)
	if err != nil {
		t.Fatal(err)
	}
	p.Release(v)
	same, err := p.Acquire("a", "")
	if err != nil || same != v {
		t.Fatal("VM was not reused", err)
	}
	if p.idle[:cap(p.idle)][0] != nil {
		t.Fatal("removed VM is retained by backing storage")
	}
	v.documents = MaxVMDocuments
	p.Release(v)
	if p.Idle() != 0 {
		t.Fatal("expired VM was retained")
	}
	other, err := p.Acquire("a", `function process(c,i){return i;}`)
	if err != nil || other == v {
		t.Fatal("expired VM was reused", err)
	}
	p.timer.Stop()
}

func TestDocumentLimitsAndRuntimeRotation(t *testing.T) {
	v, err := newVM("", `let n=0;function process(c,i){return String(++n);}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Process(t.Context(), Env{}, Document{Text: strings.Repeat("x", MaxDocumentBytes+1)}); err == nil {
		t.Fatal("oversized input accepted")
	}
	for i := 0; i < MaxVMDocuments; i++ {
		if _, err := v.Process(t.Context(), Env{}, Document{Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if !v.Expired() {
		t.Fatal("VM did not expire under continuous reuse")
	}
	if _, err := v.Process(t.Context(), Env{}, Document{Text: "x"}); err == nil {
		t.Fatal("expired VM accepted more input")
	}
	for _, script := range []string{
		`function process(){return "x".repeat(16777217);}`,
		`function process(){return new Uint8Array(16777217);}`,
	} {
		if _, err := Run(t.Context(), script, Env{}, Document{}); err == nil {
			t.Fatal("oversized output accepted")
		}
	}
}
