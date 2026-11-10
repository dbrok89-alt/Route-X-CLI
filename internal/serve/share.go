package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Tunnel drivers: each spawns a tunnel binary that forwards a
// public URL to the local server. ngrok needs an account
// (authtoken); cloudflared quick tunnels and localhost.run do
// not, but their URLs rotate. A Go CLI cannot implement ngrok's
// proprietary protocol with stdlib only, so spawning the installed
// binary is the honest approach — and when the binary or the
// account is missing, share fails with setup instructions instead
// of pretending a URL exists.

// PublicURL returns the public URL for a running ngrok tunnel by
// polling its local API (http://127.0.0.1:4040/api/tunnels).
// It polls for up to timeout and returns an error when ngrok is
// not running or has no tunnel yet.
func NgrokPublicURL(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:4040/api/tunnels")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var parsed struct {
				Tunnels []struct {
					PublicURL string `json:"public_url"`
					Proto     string `json:"proto"`
				} `json:"tunnels"`
			}
			if err := json.Unmarshal(body, &parsed); err == nil {
				for _, t := range parsed.Tunnels {
					if t.Proto == "https" && t.PublicURL != "" {
						return t.PublicURL, nil
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("no ngrok tunnel found on http://127.0.0.1:4040 — is ngrok running and authenticated? (ngrok needs NGROK_AUTHTOKEN)")
}

var cloudflareURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
var localhostRunURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.lhr\.life|https://[a-z0-9-]+\.localhost\.run`)

// ParseCloudflaredURL extracts the trycloudflare.com URL from
// cloudflared's log output.
func ParseCloudflaredURL(output string) (string, bool) {
	return cloudflareURLRe.FindString(output), cloudflareURLRe.MatchString(output)
}

// ParseLocalhostRunURL extracts the public URL from localhost.run
// ssh output.
func ParseLocalhostRunURL(output string) (string, bool) {
	return localhostRunURLRe.FindString(output), localhostRunURLRe.MatchString(output)
}

// TunnelSpec describes one tunnel invocation.
type TunnelSpec struct {
	Provider string // ngrok | cloudflared | localhost.run
	Port     int
}

// Command returns the exec.Cmd that starts the tunnel. The caller
// owns the process (wait for the URL, kill on shutdown).
func (t TunnelSpec) Command() (*exec.Cmd, error) {
	switch t.Provider {
	case "ngrok":
		if _, err := exec.LookPath("ngrok"); err != nil {
			return nil, fmt.Errorf("ngrok binary not found: install it from https://ngrok.com/download and set NGROK_AUTHTOKEN (ngrok needs an account)")
		}
		return exec.Command("ngrok", "http", fmt.Sprint(t.Port)), nil
	case "cloudflared":
		if _, err := exec.LookPath("cloudflared"); err != nil {
			return nil, fmt.Errorf("cloudflared binary not found: install it from https://developers.cloudflare.com/cloudflare-one/connections/connect/downloads/")
		}
		return exec.Command("cloudflared", "tunnel", "--url", fmt.Sprintf("http://localhost:%d", t.Port)), nil
	case "localhost.run":
		if _, err := exec.LookPath("ssh"); err != nil {
			return nil, fmt.Errorf("ssh binary not found")
		}
		return exec.Command("ssh", "-R", fmt.Sprintf("80:localhost:%d", t.Port), "nokey@localhost.run"), nil
	}
	return nil, fmt.Errorf("unknown tunnel provider %q (ngrok | cloudflared | localhost.run)", t.Provider)
}

// TunnelInstructions returns the manual command for a provider,
// shown when the user prefers to run the tunnel themselves.
func TunnelInstructions(provider string, port int) string {
	switch provider {
	case "ngrok":
		return fmt.Sprintf("ngrok http %d   # needs NGROK_AUTHTOKEN; URL appears at http://127.0.0.1:4040", port)
	case "cloudflared":
		return fmt.Sprintf("cloudflared tunnel --url http://localhost:%d   # no account needed", port)
	case "localhost.run":
		return fmt.Sprintf("ssh -R 80:localhost:%d nokey@localhost.run   # no account needed", port)
	}
	return ""
}

// LooksLikeTunnelOutput reports whether a line of tunnel output
// contains a public URL (used to stop waiting for one).
func LooksLikeTunnelOutput(line string) bool {
	return strings.Contains(line, "trycloudflare.com") || strings.Contains(line, ".ngrok-free.app") || strings.Contains(line, "localhost.run") || strings.Contains(line, ".lhr.life")
}
