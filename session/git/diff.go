package git

import (
	"strings"
	"time"
)

// DiffStats holds statistics about the changes in a diff
type DiffStats struct {
	// Content is the full diff content
	Content string
	// Added is the number of added lines
	Added int
	// Removed is the number of removed lines
	Removed int
	// Error holds any error that occurred during diff computation
	// This allows propagating setup errors (like missing base commit) without breaking the flow
	Error error
}

func (d *DiffStats) IsEmpty() bool {
	return d.Added == 0 && d.Removed == 0 && d.Content == ""
}

// Diff returns the git diff between the worktree and the base branch along with statistics.
// If force is true, cached results are bypassed even when the status signature matches.
func (g *GitWorktree) Diff(force bool) *DiffStats {
	stats := &DiffStats{}

	g.diffMu.Lock()
	defer g.diffMu.Unlock()

	statusOutput, err := g.runGitCommand(g.worktreePath, "status", "--porcelain")
	if err != nil {
		stats.Error = err
		return stats
	}

	now := time.Now()
	g.lastDiffCheckedAt = now

	statusSignature := statusOutput

	if !force && g.lastDiff != nil && statusSignature == g.lastStatusSnapshot {
		return cloneDiffStats(g.lastDiff)
	}

	if strings.Contains(statusOutput, "?? ") {
		if _, err := g.runGitCommand(g.worktreePath, "add", "-N", "."); err != nil {
			stats.Error = err
			return stats
		}

		statusOutput, err = g.runGitCommand(g.worktreePath, "status", "--porcelain")
		if err != nil {
			stats.Error = err
			return stats
		}
		statusSignature = statusOutput
	}

	content, err := g.runGitCommand(g.worktreePath, "--no-pager", "diff", g.GetBaseCommitSHA())
	if err != nil {
		stats.Error = err
		return stats
	}

	added, removed := countDiffStats(content)
	stats.Added = added
	stats.Removed = removed
	stats.Content = content

	g.lastStatusSnapshot = statusSignature
	g.lastDiff = cloneDiffStats(stats)

	return stats
}

func cloneDiffStats(src *DiffStats) *DiffStats {
	if src == nil {
		return nil
	}
	copy := *src
	return &copy
}

func countDiffStats(content string) (int, int) {
	var added, removed int
	if content == "" {
		return 0, 0
	}

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		if line[0] == '+' {
			if strings.HasPrefix(line, "+++") {
				continue
			}
			added++
		} else if line[0] == '-' {
			if strings.HasPrefix(line, "---") {
				continue
			}
			removed++
		}
	}
	return added, removed
}
