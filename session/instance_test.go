package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-squad/session/git"
)

func TestInstancePreviewDirtyFlag(t *testing.T) {
	inst := &Instance{}
	if inst.IsPreviewDirty() {
		t.Fatal("preview should start clean")
	}
	inst.MarkPreviewDirty()
	if !inst.IsPreviewDirty() {
		t.Fatal("preview dirty flag should be set")
	}
	inst.previewDirty.Store(false)
	if inst.IsPreviewDirty() {
		t.Fatal("preview dirty flag should be cleared")
	}
}

func TestInstanceUpdateDiffStatsForcesOnTimer(t *testing.T) {
	repo := setupInstanceTestRepo(t)
	head := strings.TrimSpace(runGitInstanceTest(t, repo, "rev-parse", "HEAD"))

	worktree := git.NewGitWorktreeFromStorage(repo, repo, "session-test", "main", head)

	inst := &Instance{
		Title:       "timer-refresh",
		started:     true,
		Status:      Running,
		gitWorktree: worktree,
	}

	target := filepath.Join(repo, "file.txt")

	if err := os.WriteFile(target, []byte("original\nchange-one\n"), 0o644); err != nil {
		t.Fatalf("write first change: %v", err)
	}

	inst.MarkDiffDirty()
	if err := inst.UpdateDiffStats(time.Now()); err != nil {
		t.Fatalf("first UpdateDiffStats: %v", err)
	}
	firstDiff := inst.GetDiffStats().Content
	if !strings.Contains(firstDiff, "change-one") {
		t.Fatalf("expected diff to include first change, got %q", firstDiff)
	}

	if err := os.WriteFile(target, []byte("original\nchange-two\n"), 0o644); err != nil {
		t.Fatalf("write second change: %v", err)
	}

	inst.lastDiffCheck.Store(time.Now().Add(-diffRefreshInterval - time.Second).UnixNano())

	if err := inst.UpdateDiffStats(time.Now()); err != nil {
		t.Fatalf("timer UpdateDiffStats: %v", err)
	}
	secondDiff := inst.GetDiffStats().Content
	if !strings.Contains(secondDiff, "change-two") {
		t.Fatalf("expected diff to include timer-refresh change, got %q", secondDiff)
	}
	if secondDiff == firstDiff {
		t.Fatalf("expected diff to change after timer refresh")
	}
}

func setupInstanceTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitInstanceTest(t, dir, "init", "--initial-branch=main")
	runGitInstanceTest(t, dir, "config", "user.email", "test@example.com")
	runGitInstanceTest(t, dir, "config", "user.name", "Test User")

	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	runGitInstanceTest(t, dir, "add", ".")
	runGitInstanceTest(t, dir, "commit", "-m", "initial commit")

	return dir
}

func runGitInstanceTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
