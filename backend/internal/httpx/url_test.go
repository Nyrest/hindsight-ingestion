package httpx

import "testing"

func TestNormalizeURLPreservesStableIdentity(t *testing.T) {
	tests := map[string]string{
		"https://EXAMPLE.com:443/docs/?q=a#intro": "https://example.com/docs/?q=a",
		"http://EXAMPLE.com:80":                   "http://example.com/",
		"https://[2001:DB8::1]:443/a":             "https://[2001:db8::1]/a",
		"https://example.com/a%2Fb?x=1&x=2":       "https://example.com/a%2Fb?x=1&x=2",
	}
	for input, want := range tests {
		got, err := NormalizeURL(input)
		if err != nil || got != want {
			t.Fatalf("normalize %s = %s %v", input, got, err)
		}
	}
	for _, invalid := range []string{"file:///tmp/x", "https://name:password@example.com/", "about:blank"} {
		if _, err := NormalizeURL(invalid); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}
