package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkGitWorktreeDiff_NoChange(b *testing.B) {
	repo := setupTempRepoBench(b)
	head := strings.TrimSpace(runGitBench(b, repo, "rev-parse", "HEAD"))
	wt := &GitWorktree{
		repoPath:      repo,
		worktreePath:  repo,
		branchName:    "main",
		baseCommitSHA: head,
	}

	target := filepath.Join(repo, "bench.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		b.Fatalf("write initial: %v", err)
	}
	runGitBench(b, repo, "add", "bench.txt")
	runGitBench(b, repo, "commit", "-m", "add bench file")

	if err := os.WriteFile(target, []byte("original\nmodified\n"), 0o644); err != nil {
		b.Fatalf("write diff seed: %v", err)
	}
	if stats := wt.Diff(false); stats.Error != nil {
		b.Fatalf("Diff warmup: %v", stats.Error)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if stats := wt.Diff(false); stats.Error != nil {
			b.Fatalf("Diff iteration: %v", stats.Error)
		}
	}
}

func BenchmarkGitWorktreeDiff_WithChanges(b *testing.B) {
	repo := setupTempRepoBench(b)
	head := strings.TrimSpace(runGitBench(b, repo, "rev-parse", "HEAD"))
	wt := &GitWorktree{
		repoPath:      repo,
		worktreePath:  repo,
		branchName:    "main",
		baseCommitSHA: head,
	}

	target := filepath.Join(repo, "bench.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		b.Fatalf("write initial: %v", err)
	}
	runGitBench(b, repo, "add", "bench.txt")
	runGitBench(b, repo, "commit", "-m", "add bench file")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := os.WriteFile(target, []byte("original\nmodified iteration\n"+string(rune('a'+(i%26)))+"\n"), 0o644); err != nil {
			b.Fatalf("write iteration: %v", err)
		}
		if stats := wt.Diff(false); stats.Error != nil {
			b.Fatalf("Diff iteration: %v", stats.Error)
		}
	}
}

func setupTempRepoBench(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	runGitBench(tb, dir, "init", "--initial-branch=main")
	runGitBench(tb, dir, "config", "user.email", "bench@example.com")
	runGitBench(tb, dir, "config", "user.name", "Bench User")

	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello world\n"), 0o644); err != nil {
		tb.Fatalf("write initial file: %v", err)
	}

	runGitBench(tb, dir, "add", ".")
	runGitBench(tb, dir, "commit", "-m", "initial commit")

	return dir
}

func runGitBench(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
