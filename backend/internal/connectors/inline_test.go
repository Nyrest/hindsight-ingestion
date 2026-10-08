package connectors

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testPNG = append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, bytes.Repeat([]byte{0}, 24)...)

func TestInlineMarkdownImages(t *testing.T) {
	cases := []struct {
		name, markdown string
		count          int
	}{
		{"inline", "before ![图示](https://img/a.png) after", 1},
		{"image only", "![](https://img/a.png)", 1},
		{"list", "- first\n\n  ![图示](https://img/a.png)\n\n- last", 1},
		{"reference", "![one][pic] and ![pic][] and ![pic]\n\n[pic]: https://img/a.png", 3},
		{"code", "`![x](https://img/a.png)`\n\n```md\n![x](https://img/a.png)\n<img src='https://img/a.png'>\n```", 0},
		{"indented code", "    ![x](https://img/a.png)\n", 0},
		{"escape", "\\![x](https://img/a.png)", 0},
		{"html", "before <img src='https://img/a.png' alt='图示'> after", 1},
		{"html block", "<div>\n<img src='https://img/a.png' alt='one'>\n<img src='https://img/b.png'>\n</div>\n", 2},
		{"html multiline", "before <img\n src='https://img/a.png'\n alt='one'> after", 1},
		{"html code", "<pre><code><img src='https://img/a.png'></code></pre>\n", 0},
		{"image after html code", "<pre>\n<img src='https://img/a.png'>\n</pre>\n\n![real](https://img/b.png)", 1},
		{"inline html code", "before <code><img src='https://img/a.png'>![x](https://img/b.png)</code> after", 0},
		{"container code", "```html\n<columns>\n```\n\n\t![example](https://img/a.png)", 0},
		{"tab indented container code", "\t<columns>\n\t![example](https://img/a.png)\n", 0},
		{"space indented container code", "    <columns>\n    ![example](https://img/a.png)\n", 0},
		{"notion container", "<columns>\n\t<column>\n\t\t![one](https://img/a.png)\n\t</column>\n</columns>", 1},
		{"notion container paragraphs", "<columns>\n\n\t<column>\n\n\t\t![one](https://img/a.png)\n\n\t</column>\n</columns>", 1},
		{"code after notion container", "<columns>\n\n\t<column>\n\n\t\t![one](https://img/a.png)\n\n\t</column>\n</columns>\n\n\t![example](https://img/b.png)", 1},
		{"code inside notion container", "<callout>\n\n\t\t<columns>\n\t\t![example](https://img/a.png)\n\n\t![real](https://img/b.png)\n</callout>", 1},
		{"nested caption", "![outer ![inner](https://img/b.png)](https://img/a.png)", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			content, err := InlineImages(context.Background(), tc.markdown, 1000, func(context.Context, string) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(testPNG))}, nil
			})
			if err != nil || len(content.Warnings) != 0 {
				t.Fatalf("parse: %+v %v", content, err)
			}
			images := 0
			var text strings.Builder
			for _, block := range content.Blocks {
				if block.Type == "image" {
					images++
					if block.Source.Data != base64.StdEncoding.EncodeToString(testPNG) {
						t.Fatal("bytes changed")
					}
				} else {
					text.WriteString(block.Text)
				}
			}
			if images != tc.count {
				t.Fatalf("got %d images, want %d: %+v", images, tc.count, content)
			}
			if tc.count == 0 && (calls != 0 || content.Text != tc.markdown) {
				t.Fatal("code or plain text changed")
			}
			if tc.name == "inline" && text.String() != "before \n图示\n after" {
				t.Fatalf("text order: %q", text.String())
			}
		})
	}
}

func TestInlineMarkdownImageDestinations(t *testing.T) {
	cases := []struct{ name, markdown, url string }{
		{"inline", `![diagram](assets/a\(b\).png)`, "assets/a(b).png"},
		{"reference", "![diagram][pic]\n\n[pic]: " + `assets/a\(b\).png`, "assets/a(b).png"},
		{"punctuation and entity", `![diagram](https://img/a\!b.png?x=1&amp;y=2)`, "https://img/a!b.png?x=1&y=2"},
		{"percent encoding", `![diagram](https://img/a%28b%29.png)`, "https://img/a%28b%29.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var urls []string
			content, err := InlineImages(t.Context(), tc.markdown, 1000, func(_ context.Context, rawURL string) (*http.Response, error) {
				urls = append(urls, rawURL)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(testPNG))}, nil
			})
			if err != nil || len(content.Warnings) != 0 || len(urls) != 1 || urls[0] != tc.url {
				t.Fatalf("image destination: got=%q want=%q warnings=%v err=%v", urls, tc.url, content.Warnings, err)
			}
		})
	}
}

func TestInlineContainerCodeIsolation(t *testing.T) {
	cases := []string{
		"\t<columns>\n\t![example](https://img/example.png)\n\n![real](https://img/real.png)",
		"<columns>\n\n\t<column>\n\n\t\t![real](https://img/real.png)\n\n\t</column>\n</columns>\n\n\t![example](https://img/example.png)",
		"<callout>\n\n\t\t<columns>\n\t\t![example](https://img/example.png)\n\n\t![real](https://img/real.png)\n</callout>",
		"<callout>\n\n    <columns>\n    ![example](https://img/example.png)\n\n\t![real](https://img/real.png)\n</callout>",
	}
	for _, markdown := range cases {
		t.Run(markdown, func(t *testing.T) {
			calls := 0
			content, err := InlineImages(t.Context(), markdown, 1000, func(_ context.Context, rawURL string) (*http.Response, error) {
				calls++
				if rawURL != "https://img/real.png" {
					t.Errorf("downloaded code example: %s", rawURL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(testPNG))}, nil
			})
			var text strings.Builder
			for _, block := range content.Blocks {
				text.WriteString(block.Text)
			}
			if err != nil || len(content.Warnings) != 0 || calls != 1 || content.Text != markdown || !strings.Contains(text.String(), "![example](https://img/example.png)") {
				t.Fatalf("code isolation: calls=%d content=%+v err=%v", calls, content, err)
			}
		})
	}
}

func TestInlineImageFormatsAndFallback(t *testing.T) {
	cases := []struct {
		mediaType string
		data      []byte
	}{
		{"image/png", testPNG},
		{"image/jpeg", append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0}, 24)...)},
		{"image/gif", []byte("GIF89a....................")},
		{"image/webp", []byte("RIFF....WEBPVP8 ..........")},
		{"image/svg+xml", []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>")},
	}
	for _, tc := range cases {
		t.Run(tc.mediaType, func(t *testing.T) {
			md := "before ![caption](https://img/a) after"
			content, err := InlineImages(t.Context(), md, 1000, func(context.Context, string) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(tc.data))}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.mediaType == "image/svg+xml" {
				if content.Text != md || len(content.Blocks) != 0 || len(content.Warnings) != 1 {
					t.Fatal("unsupported format was not preserved")
				}
				return
			}
			if len(content.Blocks) != 4 || content.Blocks[2].Source.MediaType != tc.mediaType || content.Blocks[2].Source.Data != base64.StdEncoding.EncodeToString(tc.data) {
				t.Fatalf("format/bytes changed: %+v", content)
			}
		})
	}
}

func TestInlineImagesLimitsAndWarnings(t *testing.T) {
	content, err := InlineImages(t.Context(), "before ![ok](https://img/a) between ![bad](https://img/b) after", int64(len(testPNG)+1), func(context.Context, string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(testPNG))}, nil
	})
	if err != nil || len(content.Warnings) != 1 || len(content.Blocks) != 4 || !strings.Contains(content.Blocks[3].Text, "![bad]") {
		t.Fatalf("total limit/context: %+v %v", content, err)
	}
	for _, limit := range []int64{int64(len(testPNG)), int64(len(testPNG) - 1)} {
		content, err := InlineImages(context.Background(), "![x](https://img/x)", limit, func(context.Context, string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(testPNG))}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if limit == int64(len(testPNG)) && len(content.Blocks) == 0 {
			t.Fatal("exact size rejected")
		}
		if limit < int64(len(testPNG)) && (len(content.Blocks) != 0 || len(content.Warnings) != 1) {
			t.Fatal("oversize not warned")
		}
	}
	calls := 0
	md := strings.Repeat("![x](https://img/x)\n", 51)
	content, err = InlineImages(context.Background(), md, 10000, func(context.Context, string) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(testPNG))}, nil
	})
	if err != nil || calls != 1 || len(content.Warnings) != 1 {
		t.Fatalf("cache/count: calls=%d warnings=%v err=%v", calls, content.Warnings, err)
	}
	last := content.Blocks[len(content.Blocks)-1].Text
	if !strings.Contains(last, "![x]") {
		t.Fatal("failed image text lost")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InlineImages(cancelled, md, 1000, nil); err != context.Canceled {
		t.Fatalf("cancel swallowed: %v", err)
	}
}

func TestImageRedirectDoesNotForwardCredentials(t *testing.T) {
	var auth, custom string
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, custom = r.Header.Get("Authorization"), r.Header.Get("X-Secret")
		w.Write(testPNG)
	}))
	defer external.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token secret" || r.Header.Get("X-Secret") != "custom" {
			t.Error("source authentication missing")
		}
		http.Redirect(w, r, external.URL, http.StatusFound)
	}))
	defer origin.Close()
	cred := Credential{Secrets: map[string]string{"token": "secret"}, Headers: map[string]string{"X-Secret": "custom"}}
	resp, err := FetchImage(context.Background(), origin.URL, &cred)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if auth != "" || custom != "" {
		t.Fatal("credentials leaked across redirect")
	}
}
