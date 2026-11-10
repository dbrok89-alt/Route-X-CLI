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

// EditFile replaces one occurrence of oldText in a file with
// newText, showing a diff and following the same approval rules
// as WriteFile. It prefers exact matching; when the exact text
// is not found it retries with each line's surrounding whitespace
// trimmed (models often get indentation slightly wrong). When
// oldText occurs more than once, or cannot be found at all, it
// refuses instead of guessing — a wrong edit is worse than none.
func EditFile(path, oldText, newText, mode string, approve ApproveFunc) (diff string, written bool, err error) {
	old, err := ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if oldText == "" {
		return "", false, fmt.Errorf("old_text must not be empty")
	}
	if strings.Count(old, oldText) > 1 {
		return "", false, fmt.Errorf("old_text occurs %d times in %s — be more specific", strings.Count(old, oldText), path)
	}
	replacement, found := replaceOnce(old, oldText, newText)
	if !found {
		// Fallback: match a single line with its surrounding
		// whitespace ignored (models often get indentation
		// slightly wrong).
		replacement, found = replaceOnceTrimmed(old, oldText, newText)
	}
	if !found {
		return "", false, fmt.Errorf("old_text not found in %s", path)
	}
	diff = Diff(old, replacement)
	if mode == "plan" {
		return diff, false, nil
	}
	if mode == "default" {
		if !approve(fmt.Sprintf("Edit %s ?\n%s", path, diff)) {
			return diff, false, nil
		}
	}
	if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
		return diff, false, err
	}
	return diff, true, nil
}

// replaceOnce replaces the first occurrence of old in s with
// newText.
func replaceOnce(s, old, newText string) (string, bool) {
	i := strings.Index(s, old)
	if i < 0 {
		return s, false
	}
	return s[:i] + newText + s[i+len(old):], true
}

// replaceOnceTrimmed retries the match line by line, comparing
// trimmed lines, and replaces the matching line with newText,
// keeping the original line's leading whitespace. Only a
// single-line oldText can be matched this way.
func replaceOnceTrimmed(s, old, newText string) (string, bool) {
	if strings.Contains(old, "\n") {
		return s, false
	}
	want := strings.TrimSpace(old)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == want {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + strings.TrimSpace(newText)
			return strings.Join(lines, "\n"), true
		}
	}
	return s, false
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

// Glob returns files under root matching pattern, OpenCode-style:
// segments are matched with filepath.Match ("*", "?", "[...]"),
// and a "**" segment matches any number of directories (including
// zero). Patterns that try to escape the root ("..") are refused.
// At most 100 matches are returned, sorted by path.
func Glob(root, pattern string) ([]string, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	for _, p := range parts {
		if p == ".." {
			return nil, fmt.Errorf("pattern must stay inside the workspace: %q", pattern)
		}
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if matchSegments(parts, strings.Split(filepath.ToSlash(rel), "/")) {
			out = append(out, path)
			if len(out) >= 100 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortStrings(out)
	return out, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// matchSegments matches path segments against pattern segments;
// "**" consumes zero or more segments.
func matchSegments(pattern, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if matchSegments(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pattern[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], path[1:])
}

// absInWorkspace resolves path against the working directory and
// refuses anything outside it.
func absInWorkspace(path string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(path)
	abs := clean
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(wd, abs)
	}
	abs = filepath.Clean(abs)
	if wd != string(os.PathSeparator) && abs != wd && !strings.HasPrefix(abs, wd+string(os.PathSeparator)) {
		return "", fmt.Errorf("refused path outside the project: %q", path)
	}
	return abs, nil
}

// PatchOp is one operation from an apply_patch text.
type PatchOp struct {
	Kind string // add | update | delete | rename
	Path string // target file (add/update/delete) or source (rename)
	Dest string // rename destination, "" otherwise
	Body string // new file content (add/update), "" otherwise
}

// ParsePatch parses OpenCode-style patch text:
//
//	*** Add File: path/to/new.go
//	<full content>
//	*** Update File: path/to/existing.go
//	<full new content>
//	*** Delete File: path/to/old.go
//	*** Rename File: old.go -> new.go
//
// Lines starting with "*** " begin a new operation; any other line
// belongs to the current Add/Update body. Unknown markers are an
// error — a patch the agent cannot parse must not half-apply.
func ParsePatch(text string) ([]PatchOp, error) {
	var ops []PatchOp
	var cur *PatchOp
	var body strings.Builder
	flush := func() {
		if cur != nil {
			cur.Body = body.String()
			ops = append(ops, *cur)
			body.Reset()
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "*** ") {
			flush()
			rest := strings.TrimSpace(line[4:])
			op := PatchOp{}
			switch {
			case strings.HasPrefix(rest, "Add File:"):
				op.Kind, op.Path = "add", strings.TrimSpace(rest[len("Add File:"):])
			case strings.HasPrefix(rest, "Update File:"):
				op.Kind, op.Path = "update", strings.TrimSpace(rest[len("Update File:"):])
			case strings.HasPrefix(rest, "Delete File:"):
				op.Kind, op.Path = "delete", strings.TrimSpace(rest[len("Delete File:"):])
			case strings.HasPrefix(rest, "Rename File:"):
				parts := strings.SplitN(strings.TrimSpace(rest[len("Rename File:"):]), "->", 2)
				if len(parts) != 2 {
					return nil, fmt.Errorf("bad rename marker: %q (want \"old -> new\")", line)
				}
				op.Kind = "rename"
				op.Path = strings.TrimSpace(parts[0])
				op.Dest = strings.TrimSpace(parts[1])
			default:
				return nil, fmt.Errorf("unknown patch marker: %q", line)
			}
			if op.Path == "" || (op.Kind == "rename" && op.Dest == "") {
				return nil, fmt.Errorf("patch marker without a path: %q", line)
			}
			cur = &op
			continue
		}
		if cur == nil {
			if strings.TrimSpace(line) == "" {
				continue
			}
			return nil, fmt.Errorf("patch content outside any operation: %q", line)
		}
		if cur.Kind == "delete" || cur.Kind == "rename" {
			if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("unexpected content after %s marker", cur.Kind)
			}
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()
	if len(ops) == 0 {
		return nil, fmt.Errorf("empty patch: no operations found")
	}
	return ops, nil
}

// ApplyPatch applies a parsed patch inside the workspace. It
// validates every operation before touching anything: add refuses
// an existing file, update/delete/rename require the source to
// exist, rename refuses an existing destination, and every path
// must stay inside the project. In plan mode it returns the
// combined diff without writing; in default mode it asks once for
// the whole patch. On the first failed operation it stops and
// reports which op failed — earlier ops may already be applied,
// and the diff in the error message shows what happened.
func ApplyPatch(ops []PatchOp, mode string, approve ApproveFunc) (diff string, written bool, err error) {
	// Validate everything first.
	var combined strings.Builder
	for i, op := range ops {
		if _, err := absInWorkspace(op.Path); err != nil {
			return "", false, fmt.Errorf("op %d (%s): %w", i+1, op.Kind, err)
		}
		if op.Kind == "rename" {
			if _, err := absInWorkspace(op.Dest); err != nil {
				return "", false, fmt.Errorf("op %d (rename): %w", i+1, err)
			}
		}
		switch op.Kind {
		case "add":
			if _, err := os.Stat(op.Path); err == nil {
				return "", false, fmt.Errorf("op %d (add): %s already exists — use update", i+1, op.Path)
			}
			fmt.Fprintf(&combined, "+++ %s (new)\n%s", op.Path, Diff("", op.Body))
		case "update":
			old, err := ReadFile(op.Path)
			if err != nil {
				return "", false, fmt.Errorf("op %d (update): %s: %w", i+1, op.Path, err)
			}
			fmt.Fprintf(&combined, "--- %s\n%s", op.Path, Diff(old, op.Body))
		case "delete":
			old, err := ReadFile(op.Path)
			if err != nil {
				return "", false, fmt.Errorf("op %d (delete): %s: %w", i+1, op.Path, err)
			}
			fmt.Fprintf(&combined, "--- %s (deleted)\n%s", op.Path, Diff(old, ""))
		case "rename":
			if _, err := os.Stat(op.Path); err != nil {
				return "", false, fmt.Errorf("op %d (rename): %s: %w", i+1, op.Path, err)
			}
			if _, err := os.Stat(op.Dest); err == nil {
				return "", false, fmt.Errorf("op %d (rename): destination %s already exists", i+1, op.Dest)
			}
			fmt.Fprintf(&combined, "rename %s -> %s\n", op.Path, op.Dest)
		default:
			return "", false, fmt.Errorf("op %d: unknown kind %q", i+1, op.Kind)
		}
	}
	diff = combined.String()
	if mode == "plan" {
		return diff, false, nil
	}
	if mode == "default" {
		if !approve(fmt.Sprintf("Apply patch?\n%s", diff)) {
			return diff, false, nil
		}
	}
	for i, op := range ops {
		switch op.Kind {
		case "add", "update":
			if err := os.MkdirAll(filepath.Dir(op.Path), 0o755); err != nil {
				return diff, false, fmt.Errorf("op %d (%s): %w", i+1, op.Kind, err)
			}
			if err := os.WriteFile(op.Path, []byte(op.Body), 0o644); err != nil {
				return diff, false, fmt.Errorf("op %d (%s): %w", i+1, op.Kind, err)
			}
		case "delete":
			if err := os.Remove(op.Path); err != nil {
				return diff, false, fmt.Errorf("op %d (delete): %w", i+1, err)
			}
		case "rename":
			if err := os.MkdirAll(filepath.Dir(op.Dest), 0o755); err != nil {
				return diff, false, fmt.Errorf("op %d (rename): %w", i+1, err)
			}
			if err := os.Rename(op.Path, op.Dest); err != nil {
				return diff, false, fmt.Errorf("op %d (rename): %w", i+1, err)
			}
		}
	}
	return diff, true, nil
}
