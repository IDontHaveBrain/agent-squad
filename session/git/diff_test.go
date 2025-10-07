package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitWorktreeDiffCachesStatusSnapshots(t *testing.T) {
	repo := setupTempRepo(t)

	head := runGit(t, repo, "rev-parse", "HEAD")
	head = strings.TrimSpace(head)

	wt := &GitWorktree{
		repoPath:      repo,
		worktreePath:  repo,
		branchName:    "main",
		baseCommitSHA: head,
	}

	targetFile := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(targetFile, []byte("hello world\nsecond line\n"), 0o644); err != nil {
		t.Fatalf("write pending change: %v", err)
	}

	stats := wt.Diff(false)
	if stats.Error != nil {
		t.Fatalf("Diff initial: %v", stats.Error)
	}
	if stats.IsEmpty() {
		t.Fatal("expected non-empty diff when file modified")
	}

	wt.lastDiff.Content = "cached"
	second := wt.Diff(false)
	if second.Error != nil {
		t.Fatalf("Diff cached: %v", second.Error)
	}
	if second.Content != "cached" {
		t.Fatalf("expected cached diff content, got %q", second.Content)
	}

	if err := os.WriteFile(targetFile, []byte("hello world\nsecond line\nthird line\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	wt.InvalidateDiffCache()
	afterChange := wt.Diff(false)
	if afterChange.Error != nil {
		t.Fatalf("Diff after change: %v", afterChange.Error)
	}
	if afterChange.Content == "cached" {
		t.Fatal("expected diff cache invalidation after change")
	}
	if afterChange.Added == 0 {
		t.Fatal("expected diff to report additions after change")
	}
}

func setupTempRepo(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")

	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")

	return dir
}

func runGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
