package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-squad/session/git"
	"agent-squad/session/tmux"
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

func TestInstanceGetBranch(t *testing.T) {
	repo := setupInstanceTestRepo(t)
	head := strings.TrimSpace(runGitInstanceTest(t, repo, "rev-parse", "HEAD"))

	worktree := git.NewGitWorktreeFromStorage(repo, repo, "test-session", "test-branch", head)

	inst := &Instance{
		Title:       "test-instance",
		Branch:      "old-branch",
		started:     true,
		Status:      Running,
		gitWorktree: worktree,
	}

	branch := inst.GetBranch()
	if branch != "test-branch" {
		t.Fatalf("expected branch 'test-branch', got '%s'", branch)
	}

	if inst.Branch != "test-branch" {
		t.Fatalf("expected instance.Branch to be synced to 'test-branch', got '%s'", inst.Branch)
	}
}

func TestInstanceGetBranchWithoutWorktree(t *testing.T) {
	inst := &Instance{
		Title:       "test-instance",
		Branch:      "fallback-branch",
		started:     false,
		Status:      Ready,
		gitWorktree: nil,
	}

	branch := inst.GetBranch()
	if branch != "fallback-branch" {
		t.Fatalf("expected fallback branch 'fallback-branch', got '%s'", branch)
	}
}

func TestEnsureTmuxSessionCreatesNewSessionWhenMissing(t *testing.T) {
	repo := setupInstanceTestRepo(t)
	worktreeDir := t.TempDir()

	worktree := git.NewGitWorktreeFromStorage(repo, worktreeDir, "test-session", "test-branch", "")

	exec := &fakeExecutor{captureReturnValue: "Do you trust the files in this folder?"}
	pty := &fakePtyFactory{exec: exec}

	inst := &Instance{
		Title:       "test-session",
		Program:     "claude",
		started:     true,
		Status:      Ready,
		gitWorktree: worktree,
		tmuxSession: tmux.NewTmuxSessionWithDeps("test-session", "claude", pty, exec),
	}

	if err := inst.ensureTmuxSession(); err != nil {
		t.Fatalf("ensureTmuxSession returned error: %v", err)
	}

	if !exec.hasSession {
		t.Fatalf("expected ensureTmuxSession to start a tmux session")
	}

	if len(pty.startCalls) == 0 || !strings.Contains(pty.startCalls[0], "new-session") {
		t.Fatalf("expected tmux new-session command to run, got %v", pty.startCalls)
	}

	if inst.Status != Running {
		t.Fatalf("expected instance status Running, got %v", inst.Status)
	}
}

func TestEnsureTmuxSessionFailsWhenStartErrors(t *testing.T) {
	repo := setupInstanceTestRepo(t)
	worktreeDir := t.TempDir()

	worktree := git.NewGitWorktreeFromStorage(repo, worktreeDir, "another-session", "test-branch", "")

	exec := &fakeExecutor{failNewSession: true, captureReturnValue: "Do you trust the files in this folder?"}
	pty := &fakePtyFactory{exec: exec}

	inst := &Instance{
		Title:       "another-session",
		Program:     "claude",
		started:     true,
		Status:      Ready,
		gitWorktree: worktree,
		tmuxSession: tmux.NewTmuxSessionWithDeps("another-session", "claude", pty, exec),
	}

	err := inst.ensureTmuxSession()
	if err == nil {
		t.Fatalf("expected ensureTmuxSession to fail when tmux start fails")
	}
	if !strings.Contains(err.Error(), "failed to start") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fakeExecutor struct {
	hasSession         bool
	failNewSession     bool
	commands           []string
	captureResponded   bool
	captureReturnValue string
}

func (f *fakeExecutor) Run(cmd *exec.Cmd) error {
	f.commands = append(f.commands, strings.Join(cmd.Args, " "))

	if len(cmd.Args) < 2 {
		return nil
	}

	switch cmd.Args[1] {
	case "has-session":
		if f.hasSession {
			return nil
		}
		return fmt.Errorf("no session")
	case "kill-session":
		f.hasSession = false
		return nil
	case "set-option":
		return nil
	default:
		return nil
	}
}

func (f *fakeExecutor) Output(cmd *exec.Cmd) ([]byte, error) {
	f.commands = append(f.commands, strings.Join(cmd.Args, " "))
	if len(cmd.Args) >= 2 && cmd.Args[1] == "capture-pane" {
		if !f.captureResponded && f.captureReturnValue != "" {
			f.captureResponded = true
			return []byte(f.captureReturnValue), nil
		}
		return []byte(""), nil
	}
	return []byte(""), nil
}

type fakePtyFactory struct {
	exec       *fakeExecutor
	startCalls []string
}

func (f *fakePtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	f.startCalls = append(f.startCalls, strings.Join(cmd.Args, " "))

	if len(cmd.Args) >= 2 && cmd.Args[1] == "new-session" {
		if f.exec.failNewSession {
			return nil, fmt.Errorf("forced new-session failure")
		}
		f.exec.hasSession = true
	}

	if len(cmd.Args) >= 2 && cmd.Args[1] == "attach-session" && !f.exec.hasSession {
		return nil, fmt.Errorf("session missing")
	}

	file, err := os.CreateTemp("", "fake-pty")
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *fakePtyFactory) Close() {}
