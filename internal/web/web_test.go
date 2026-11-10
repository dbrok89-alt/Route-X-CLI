package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchHTMLAndTruncation(t *testing.T) {
	t.Setenv("ROUTE_X_ALLOW_PRIVATE_FETCH", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body><h1>Hello</h1><p>World &amp; friends</p></body></html>"))
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(strings.Repeat("x", MaxBody+100)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	text, err := Fetch(srv.URL + "/page")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "World & friends") {
		t.Fatalf("fetch html = %q", text)
	}
	if strings.Contains(text, "<h1>") {
		t.Fatalf("tags not stripped: %q", text)
	}
	text, err = Fetch(srv.URL + "/big")
	if err != nil || !strings.HasSuffix(text, "[truncated: response exceeded 200 KiB]") {
		t.Fatalf("big fetch: err=%v truncated=%v", err, strings.HasSuffix(text, "[truncated: response exceeded 200 KiB]"))
	}
	if _, err := Fetch(srv.URL + "/missing"); err == nil {
		t.Fatal("fetch must fail on 404")
	}
}

func TestFetchRefusesPrivateHosts(t *testing.T) {
	// No escape hatch: loopback must be refused.
	t.Setenv("ROUTE_X_ALLOW_PRIVATE_FETCH", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if _, err := Fetch(srv.URL + "/"); err == nil {
		t.Fatal("fetch must refuse loopback hosts (SSRF guard)")
	}
	if _, err := Fetch("ftp://example.com/x"); err == nil {
		t.Fatal("fetch must refuse non-http schemes")
	}
}

func TestSearchDDG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a class="result__a" href="https://example.com/a">First <b>Result</b></a>
<a class="result__snippet" href="#">snippet one</a>
<a class="result__a" href="https://example.com/b">Second</a>
<a class="result__snippet" href="#">snippet two</a>`))
	}))
	defer srv.Close()
	s := &Searcher{DDGBaseURL: srv.URL}
	results, err := s.Search(context.Background(), "test query")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Title != "First Result" || results[0].URL != "https://example.com/a" || results[0].Snippet != "snippet one" {
		t.Fatalf("ddg results = %+v", results)
	}
}

func TestSearchExa(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "exa-test-key" {
			t.Errorf("missing exa api key header")
		}
		_, _ = w.Write([]byte(`{"results": [{"title": "T", "url": "https://x.dev", "text": "some text here"}]}`))
	}))
	defer srv.Close()
	s := &Searcher{ExaAPIKey: "exa-test-key", ExaBaseURL: srv.URL}
	results, err := s.Search(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "T" || results[0].Snippet != "some text here" {
		t.Fatalf("exa results = %+v", results)
	}
	if _, err := (&Searcher{}).Search(context.Background(), ""); err == nil {
		t.Fatal("search must reject an empty query")
	}
}
