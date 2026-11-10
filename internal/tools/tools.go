// Package tools implements the file and shell tools Route-X agents
// can call: read, search, write-with-diff, and run. Every write and
// every shell command goes through an approval gate that depends on
// the current execution mode, exactly like agy's diff review.
package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ApproveFunc is called before a write or shell command in
// "default" mode. It receives a human-readable description and
// returns true when the user approves it.
type ApproveFunc func(description string) bool

// AutoApprove always approves. Used in auto / accept-edits modes.
func AutoApprove(string) bool { return true }

// NeverApprove never approves. Used in plan mode.
func NeverApprove(string) bool { return false }

// ReadFile reads a file, refusing files larger than 1 MiB.
func ReadFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 1<<20 {
		return "", fmt.Errorf("file %s is too large (%d bytes, limit 1 MiB)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Match is one search hit.
type Match struct {
	File string
	Line int
	Text string
}

var skipDirs = map[string]bool{
	".git": true, ".hg": true, "node_modules": true,
	"vendor": true, ".route-x": true, "dist": true, "build": true,
}

// Search walks root looking for pattern (regex) in text files.
func Search(root, pattern string) ([]Match, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Fall back to a literal search when the regex is invalid.
		re = regexp.MustCompile(regexp.QuoteMeta(pattern))
	}
	var out []Match
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, don't abort the search
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				out = append(out, Match{File: path, Line: i + 1, Text: strings.TrimSpace(line)})
				if len(out) >= 100 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return out, err
}

// Diff produces an ordered line diff: removed lines prefixed
// with "-", added lines with "+". It is LCS-based, so order and
// duplicate lines are preserved — the old set-based diff silently
// hid reordered and duplicated lines. Files with more than 4000
// lines fall back to the set diff to keep the O(n*m) LCS bounded.
func Diff(old, new string) string {
	oldLines := splitLines(old)
	newLines := splitLines(new)
	if len(oldLines) > 4000 || len(newLines) > 4000 {
		return setDiff(oldLines, newLines)
	}
	// LCS dynamic programming table.
	n, m := len(oldLines), len(newLines)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var b strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		if oldLines[i] == newLines[j] {
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			fmt.Fprintf(&b, "- %s\n", oldLines[i])
			i++
		} else {
			fmt.Fprintf(&b, "+ %s\n", newLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		fmt.Fprintf(&b, "- %s\n", oldLines[i])
	}
	for ; j < m; j++ {
		fmt.Fprintf(&b, "+ %s\n", newLines[j])
	}
	return b.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// A trailing newline does not create an extra empty line.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func setDiff(oldLines, newLines []string) string {
	oldSet := map[string]bool{}
	for _, line := range oldLines {
		oldSet[line] = true
	}
	newSet := map[string]bool{}
	for _, line := range newLines {
		newSet[line] = true
	}
	var b strings.Builder
	for _, line := range oldLines {
		if !newSet[line] {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	for _, line := range newLines {
		if !oldSet[line] {
			fmt.Fprintf(&b, "+ %s\n", line)
		}
	}
	return b.String()
}

// WriteFile shows a diff, asks for approval when needed, then writes.
// mode "plan" never writes — it only returns the diff.
// Paths outside the current working directory are refused: a
// coding agent must never write outside the project folder by
// accident or by a crafted prompt. Absolute paths inside the
// project are allowed.
func WriteFile(path, content, mode string, approve ApproveFunc) (diff string, written bool, err error) {
	clean := filepath.Clean(path)
	wd, err := os.Getwd()
	if err != nil {
		return "", false, err
	}
	abs := clean
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(wd, abs)
	}
	abs = filepath.Clean(abs)
	if wd != string(os.PathSeparator) && abs != wd && !strings.HasPrefix(abs, wd+string(os.PathSeparator)) {
		return "", false, fmt.Errorf("refused to write outside the project: %q", path)
	}
	old, _ := ReadFile(path) // missing file => old == ""
	diff = Diff(old, content)
	if mode == "plan" {
		return diff, false, nil
	}
	if mode == "default" {
		if !approve(fmt.Sprintf("Write %s ?\n%s", path, diff)) {
			return diff, false, nil
		}
	}
	// accept-edits and auto approve writes without asking.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return diff, false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return diff, false, err
	}
	return diff, true, nil
}

var blockedCommands = []string{
	"rm -rf", "rm -fr", "mkfs", "dd if=", ":(){", "shutdown", "reboot",
	"git push --force", "git push -f", "> /dev/", "chmod 777 /", "chmod -R 777 /",
}

// RunCommand runs a shell command with a 30s timeout.
// Dangerous commands are refused outright, before any approval.
func RunCommand(command, mode string, approve ApproveFunc) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("empty command")
	}
	for _, blocked := range blockedCommands {
		if strings.Contains(command, blocked) {
			return "", fmt.Errorf("refused dangerous command containing %q", blocked)
		}
	}
	if mode == "plan" {
		return "(plan mode: command not run)\n$ " + command, nil
	}
	if mode == "default" {
		if !approve("Run shell command?\n$ " + command) {
			return "(command rejected by user)", nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("command timed out after 30s")
	}
	return string(out), err
}

// ListFiles returns up to n regular files under root, for context.
func ListFiles(root string, n int) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(out) >= n {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out
}
