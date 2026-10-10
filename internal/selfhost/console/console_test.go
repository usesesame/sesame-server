package console

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedDistHasAnIndexPage(t *testing.T) {
	data, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatalf("index.html is not embedded: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(data)), "<!doctype html>") {
		t.Fatalf("index.html is not an HTML document")
	}
}

func TestEmbeddedIndexRespectsTheContentSecurityPolicy(t *testing.T) {
	data, err := fs.ReadFile(FS(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, tag := range regexp.MustCompile(`(?is)<script[^>]*>`).FindAllString(page, -1) {
		if !regexp.MustCompile(`(?i)\ssrc=`).MatchString(tag) {
			t.Fatalf("inline script tag %q would be blocked by script-src 'self'", tag)
		}
	}
	if regexp.MustCompile(`(?is)<style[\s>]`).MatchString(page) {
		t.Fatalf("inline style element would be blocked by style-src 'self'")
	}
	if regexp.MustCompile(`(?is)\sstyle=`).MatchString(page) {
		t.Fatalf("inline style attribute would be blocked by style-src 'self'")
	}
}
