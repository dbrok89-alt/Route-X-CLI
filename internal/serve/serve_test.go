package serve

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New("127.0.0.1:0", "test-token", func(prompt string) (string, error) {
		return "echo: " + prompt, nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.handler)
	t.Cleanup(ts.Close)
	return s, ts
}

func TestHealthNeedsNoToken(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestPromptRequiresToken(t *testing.T) {
	_, ts := testServer(t)
	resp, err := http.Post(ts.URL+"/prompt", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestPromptWithToken(t *testing.T) {
	_, ts := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/prompt", strings.NewReader(`{"prompt":"hello"}`))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("echo: hello")) {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestWrongTokenRejected(t *testing.T) {
	_, ts := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/prompt", strings.NewReader(`{"prompt":"hello"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestNewRefusesPublicBind(t *testing.T) {
	if _, err := New("0.0.0.0:8080", "tok", nil, false); err == nil {
		t.Fatal("public bind without --allow-public-bind should be refused")
	}
	if _, err := New("0.0.0.0:8080", "tok", nil, true); err != nil {
		t.Fatalf("explicit public bind should be allowed: %v", err)
	}
}

func TestNewRefusesEmptyToken(t *testing.T) {
	if _, err := New("127.0.0.1:8080", "", nil, false); err == nil {
		t.Fatal("empty token should be refused")
	}
}

func TestParseCloudflaredURL(t *testing.T) {
	out := "2026-11-10T10:00:00Z INF +--------------------------------------------------------------------------------------------+\n2026-11-10T10:00:00Z INF |  https://quick-fox-1234.trycloudflare.com has been started |\n"
	url, ok := ParseCloudflaredURL(out)
	if !ok || url != "https://quick-fox-1234.trycloudflare.com" {
		t.Fatalf("url = %q, ok=%v", url, ok)
	}
	if _, ok := ParseCloudflaredURL("no url here"); ok {
		t.Fatal("no URL should be found in plain text")
	}
}

func TestNgrokPublicURLTimesOutWithoutNgrok(t *testing.T) {
	// No ngrok is running in the test environment; this must
	// fail fast with a helpful error, not hang.
	if _, err := NgrokPublicURL(600 * time.Millisecond); err == nil || !strings.Contains(err.Error(), "ngrok") {
		t.Fatalf("err = %v", err)
	}
}

func TestTunnelSpecUnknownProvider(t *testing.T) {
	if _, err := (TunnelSpec{Provider: "magic", Port: 8080}).Command(); err == nil {
		t.Fatal("unknown provider should fail")
	}
}

func TestTunnelSpecNgrokMissingBinary(t *testing.T) {
	// PATH without ngrok: the driver must fail with install
	// instructions, not a bare exec error.
	t.Setenv("PATH", "/nonexistent")
	if _, err := (TunnelSpec{Provider: "ngrok", Port: 8080}).Command(); err == nil || !strings.Contains(err.Error(), "ngrok.com") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewTokenUsesEnvOverride(t *testing.T) {
	t.Setenv("ROUTE_X_SERVE_TOKEN", "fixed-token")
	got, err := NewToken()
	if err != nil || got != "fixed-token" {
		t.Fatalf("token = %q, err=%v", got, err)
	}
}

func TestNewTokenRandomIsLong(t *testing.T) {
	t.Setenv("ROUTE_X_SERVE_TOKEN", "")
	got, err := NewToken()
	if err != nil || len(got) < 32 {
		t.Fatalf("token = %q, err=%v", got, err)
	}
	if got == fmt.Sprint("x") {
		t.Fatal("token must be random")
	}
}
