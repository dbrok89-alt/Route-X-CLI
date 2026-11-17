package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type scoredMatch struct {
	match Match
	score int
}

// CodeSearch scans code files under root for query, assigning higher score to symbol declarations,
// sorting by highest score, and returning up to limit matches.
// It is keyword-ranked, not semantic search: no embedding model is
// involved, so results are exact substring matches only. Guards mirror
// tools.Search: skipDirs are skipped and files over 1 MiB are not read
// (so a compiled binary in the workspace never produces junk matches).
func CodeSearch(root, query string, limit int) ([]Match, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("codesearch needs a non-empty query")
	}
	var scored []scoredMatch
	lowerQuery := strings.ToLower(query)
	symbolKeywords := []string{"func ", "type ", "struct ", "interface ", "def ", "class ", "const ", "var "}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
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
		for lineNum, line := range lines {
			lowerLine := strings.ToLower(line)
			if !strings.Contains(lowerLine, lowerQuery) {
				continue
			}

			trimmed := strings.TrimSpace(line)
			score := 1
			for _, kw := range symbolKeywords {
				if strings.HasPrefix(trimmed, kw) || strings.HasPrefix(line, kw) {
					score += 10
					break
				}
			}

			scored = append(scored, scoredMatch{
				match: Match{
					File: path,
					Line: lineNum + 1,
					Text: trimmed,
				},
				score: score,
			})
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	results := make([]Match, 0, len(scored))
	for _, sm := range scored {
		results = append(results, sm.match)
	}

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}
