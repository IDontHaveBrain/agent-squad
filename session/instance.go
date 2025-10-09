package session

import (
	"agent-squad/log"
	"agent-squad/session/git"
	"agent-squad/session/tmux"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atotto/clipboard"
	"github.com/fsnotify/fsnotify"
)

type Status int

const (
	// Running is the status when the instance is running and claude is working.
	Running Status = iota
	// Ready is if the claude instance is ready to be interacted with (waiting for user input).
	Ready
	// Loading is if the instance is loading (if we are starting it up or something).
	Loading
	// Paused is if the instance is paused (worktree removed but branch preserved).
	Paused
)

const (
	diffRefreshInterval = 5 * time.Second
)

// Instance is a running instance of claude code.
type Instance struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Branch is the branch of the instance.
	Branch string
	// Status is the status of the instance.
	Status Status
	// Program is the program to run in the instance.
	Program string
	// Height is the height of the instance.
	Height int
	// Width is the width of the instance.
	Width int
	// CreatedAt is the time the instance was created.
	CreatedAt time.Time
	// UpdatedAt is the time the instance was last updated.
	UpdatedAt time.Time
	// AutoYes is true if the instance should automatically press enter when prompted.
	AutoYes bool
	// Prompt is the initial prompt to pass to the instance on startup
	Prompt string

	// DiffStats stores the current git diff statistics
	diffStats *git.DiffStats

	diffDirty     atomic.Bool
	diffMu        sync.Mutex
	previewDirty  atomic.Bool
	lastDiffCheck atomic.Int64

	diffWatcher         *fsnotify.Watcher
	diffWatcherDisabled bool
	diffWatchCtx        context.Context
	diffWatchCancel     context.CancelFunc
	diffWatchWg         sync.WaitGroup

	// The below fields are initialized upon calling Start().

	started bool
	// tmuxSession is the tmux session for the instance.
	tmuxSession *tmux.TmuxSession
	// gitWorktree is the git worktree for the instance.
	gitWorktree *git.GitWorktree
}

// ToInstanceData converts an Instance to its serializable form
func (i *Instance) ToInstanceData() InstanceData {
	data := InstanceData{
		Title:     i.Title,
		Path:      i.Path,
		Branch:    i.Branch,
		Status:    i.Status,
		Height:    i.Height,
		Width:     i.Width,
		CreatedAt: i.CreatedAt,
		UpdatedAt: time.Now(),
		Program:   i.Program,
		AutoYes:   i.AutoYes,
	}

	// Only include worktree data if gitWorktree is initialized
	if i.gitWorktree != nil {
		data.Worktree = GitWorktreeData{
			RepoPath:      i.gitWorktree.GetRepoPath(),
			WorktreePath:  i.gitWorktree.GetWorktreePath(),
			SessionName:   i.Title,
			BranchName:    i.gitWorktree.GetBranchName(),
			BaseCommitSHA: i.gitWorktree.GetBaseCommitSHA(),
		}
	}

	// Only include diff stats if they exist
	if i.diffStats != nil {
		data.DiffStats = DiffStatsData{
			Added:   i.diffStats.Added,
			Removed: i.diffStats.Removed,
			Content: i.diffStats.Content,
		}
	}

	return data
}

// FromInstanceData creates a new Instance from serialized data
func FromInstanceData(data InstanceData) (*Instance, error) {
	instance := &Instance{
		Title:     data.Title,
		Path:      data.Path,
		Branch:    data.Branch,
		Status:    data.Status,
		Height:    data.Height,
		Width:     data.Width,
		CreatedAt: data.CreatedAt,
		UpdatedAt: data.UpdatedAt,
		Program:   data.Program,
		gitWorktree: git.NewGitWorktreeFromStorage(
			data.Worktree.RepoPath,
			data.Worktree.WorktreePath,
			data.Worktree.SessionName,
			data.Worktree.BranchName,
			data.Worktree.BaseCommitSHA,
		),
		diffStats: &git.DiffStats{
			Added:   data.DiffStats.Added,
			Removed: data.DiffStats.Removed,
			Content: data.DiffStats.Content,
		},
	}
	instance.previewDirty.Store(true)
	instance.diffDirty.Store(true)
	instance.lastDiffCheck.Store(0)

	if instance.Paused() {
		instance.started = true
		instance.tmuxSession = tmux.NewTmuxSession(instance.Title, instance.Program)
		// Sync branch from gitWorktree for paused instances
		instance.GetBranch()
	} else {
		if err := instance.Start(false); err != nil {
			return nil, err
		}
	}

	return instance, nil
}

// Options for creating a new instance
type InstanceOptions struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Program is the program to run in the instance (e.g. "claude", "aider --model ollama_chat/gemma3:1b")
	Program string
	// If AutoYes is true, then
	AutoYes bool
}

func NewInstance(opts InstanceOptions) (*Instance, error) {
	t := time.Now()

	// Convert path to absolute
	absPath, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	inst := &Instance{
		Title:     opts.Title,
		Status:    Ready,
		Path:      absPath,
		Program:   opts.Program,
		Height:    0,
		Width:     0,
		CreatedAt: t,
		UpdatedAt: t,
		AutoYes:   false,
	}
	inst.previewDirty.Store(true)
	inst.diffDirty.Store(true)
	inst.lastDiffCheck.Store(0)
	return inst, nil
}

func (i *Instance) RepoName() (string, error) {
	if !i.started {
		return "", fmt.Errorf("cannot get repo name for instance that has not been started")
	}
	return i.gitWorktree.GetRepoName(), nil
}

func (i *Instance) SetStatus(status Status) {
	i.Status = status
}

// firstTimeSetup is true if this is a new instance. Otherwise, it's one loaded from storage.
func (i *Instance) Start(firstTimeSetup bool) error {
	if i.Title == "" {
		return fmt.Errorf("instance title cannot be empty")
	}

	var tmuxSession *tmux.TmuxSession
	if i.tmuxSession != nil {
		// Use existing tmux session (useful for testing)
		tmuxSession = i.tmuxSession
	} else {
		// Create new tmux session
		tmuxSession = tmux.NewTmuxSession(i.Title, i.Program)
	}
	i.tmuxSession = tmuxSession

	if firstTimeSetup {
		gitWorktree, branchName, err := git.NewGitWorktree(i.Path, i.Title)
		if err != nil {
			return fmt.Errorf("failed to create git worktree: %w", err)
		}
		i.gitWorktree = gitWorktree
		i.Branch = branchName
	}

	// Setup error handler to cleanup resources on any error
	var setupErr error
	defer func() {
		if setupErr != nil {
			if cleanupErr := i.Kill(); cleanupErr != nil {
				setupErr = fmt.Errorf("%v (cleanup error: %v)", setupErr, cleanupErr)
			}
		} else {
			i.started = true
		}
	}()

	if !firstTimeSetup {
		// Reuse existing session
		if err := tmuxSession.Restore(); err != nil {
			setupErr = fmt.Errorf("failed to restore existing session: %w", err)
			return setupErr
		}
		// Sync branch from gitWorktree when loading from storage
		i.GetBranch()
	} else {
		// Setup git worktree first
		if err := i.gitWorktree.Setup(); err != nil {
			setupErr = fmt.Errorf("failed to setup git worktree: %w", err)
			return setupErr
		}

		// Create new session
		if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
			}
			setupErr = fmt.Errorf("failed to start new session: %w", err)
			return setupErr
		}
	}

	if err := i.startDiffWatcher(); err != nil {
		setupErr = fmt.Errorf("failed to initialize diff watcher: %w", err)
		return setupErr
	}

	i.MarkPreviewDirty()
	i.MarkDiffDirty()
	i.lastDiffCheck.Store(0)
	i.SetStatus(Running)

	return nil
}

// Kill terminates the instance and cleans up all resources
func (i *Instance) Kill() error {
	if !i.started {
		// If instance was never started, just return success
		return nil
	}

	var errs []error

	if err := i.stopDiffWatcher(); err != nil {
		errs = append(errs, fmt.Errorf("failed to stop diff watcher: %w", err))
	}

	// Always try to cleanup both resources, even if one fails
	// Clean up tmux session first since it's using the git worktree
	if i.tmuxSession != nil {
		if err := i.tmuxSession.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close tmux session: %w", err))
		}
	}

	// Then clean up git worktree
	if i.gitWorktree != nil {
		if err := i.gitWorktree.Cleanup(); err != nil {
			errs = append(errs, fmt.Errorf("failed to cleanup git worktree: %w", err))
		}
	}

	return i.combineErrors(errs)
}

// combineErrors combines multiple errors into a single error
func (i *Instance) combineErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple cleanup errors occurred:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return fmt.Errorf("%s", errMsg)
}

func (i *Instance) Preview() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	content, err := i.tmuxSession.CapturePaneContent()
	if err != nil {
		return "", err
	}
	i.previewDirty.Store(false)
	return content, nil
}

func (i *Instance) HasUpdated() (updated bool, hasPrompt bool) {
	if !i.started {
		return false, false
	}
	updated, hasPrompt = i.tmuxSession.HasUpdated()
	if updated || hasPrompt {
		i.MarkPreviewDirty()
		i.MarkDiffDirty()
	}
	return updated, hasPrompt
}

func (i *Instance) MarkPreviewDirty() {
	i.previewDirty.Store(true)
}

func (i *Instance) IsPreviewDirty() bool {
	return i.previewDirty.Load()
}

func (i *Instance) MarkDiffDirty() {
	i.diffDirty.Store(true)
	if i.gitWorktree != nil {
		i.gitWorktree.InvalidateDiffCache()
	}
}

// TapEnter sends an enter key press to the tmux session if AutoYes is enabled.
func (i *Instance) TapEnter() {
	if !i.started || !i.AutoYes {
		return
	}
	if err := i.tmuxSession.TapEnter(); err != nil {
		log.ErrorLog.Printf("error tapping enter: %v", err)
	}
}

func (i *Instance) ensureTmuxSession() error {
	if !i.started {
		return fmt.Errorf("cannot ensure tmux session for instance that has not been started")
	}
	if i.Status == Paused {
		return fmt.Errorf("cannot ensure tmux session for paused instance")
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if i.tmuxSession.DoesSessionExist() {
		return nil
	}

	worktreePath := ""
	if i.gitWorktree != nil {
		worktreePath = i.gitWorktree.GetWorktreePath()
	}
	if worktreePath == "" {
		return fmt.Errorf("worktree path not set; resume the instance before attaching")
	}

	if _, err := os.Stat(worktreePath); err != nil {
		return fmt.Errorf("worktree path %s unavailable: %w", worktreePath, err)
	}

	if log.InfoLog != nil {
		log.InfoLog.Printf("tmux session missing for %s; starting a fresh session in %s", i.Title, worktreePath)
	}
	if err := i.tmuxSession.Start(worktreePath); err != nil {
		return fmt.Errorf("failed to start new tmux session: %w", err)
	}

	i.MarkPreviewDirty()
	i.MarkDiffDirty()
	i.lastDiffCheck.Store(0)
	i.SetStatus(Running)
	i.UpdatedAt = time.Now()

	return nil
}

func (i *Instance) Attach() (chan struct{}, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot attach instance that has not been started")
	}
	if err := i.ensureTmuxSession(); err != nil {
		return nil, err
	}

	ch, err := i.tmuxSession.Attach()
	if err == nil {
		return ch, nil
	}

	// Attempt one restore in case the PTY was stale
	if log.WarningLog != nil {
		log.WarningLog.Printf("failed to attach to tmux session %s: %v; attempting restore", i.Title, err)
	}
	if restoreErr := i.tmuxSession.Restore(); restoreErr != nil {
		return nil, fmt.Errorf("failed to attach and restore tmux session: %w", err)
	}
	return i.tmuxSession.Attach()
}

func (i *Instance) SetPreviewSize(width, height int) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot set preview size for instance that has not been started or " +
			"is paused")
	}
	return i.tmuxSession.SetDetachedSize(width, height)
}

// GetGitWorktree returns the git worktree for the instance
func (i *Instance) GetGitWorktree() (*git.GitWorktree, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot get git worktree for instance that has not been started")
	}
	return i.gitWorktree, nil
}

// GetBranch returns the current branch name, syncing from gitWorktree if available
func (i *Instance) GetBranch() string {
	if i.gitWorktree != nil {
		i.Branch = i.gitWorktree.GetBranchName()
	}
	return i.Branch
}

func (i *Instance) Started() bool {
	return i.started
}

// SetTitle sets the title of the instance. Returns an error if the instance has started.
// We cant change the title once it's been used for a tmux session etc.
func (i *Instance) SetTitle(title string) error {
	if i.started {
		return fmt.Errorf("cannot change title of a started instance")
	}
	i.Title = title
	return nil
}

func (i *Instance) Paused() bool {
	return i.Status == Paused
}

// TmuxAlive returns true if the tmux session is alive. This is a sanity check before attaching.
func (i *Instance) TmuxAlive() bool {
	return i.tmuxSession.DoesSessionExist()
}

// Pause stops the tmux session and removes the worktree, preserving the branch
func (i *Instance) Pause() error {
	if !i.started {
		return fmt.Errorf("cannot pause instance that has not been started")
	}
	if i.Status == Paused {
		return fmt.Errorf("instance is already paused")
	}

	var errs []error

	if err := i.stopDiffWatcher(); err != nil {
		errs = append(errs, fmt.Errorf("failed to stop diff watcher: %w", err))
	}

	// Check if there are any changes to commit
	if dirty, err := i.gitWorktree.IsDirty(); err != nil {
		errs = append(errs, fmt.Errorf("failed to check if worktree is dirty: %w", err))
		log.ErrorLog.Print(err)
	} else if dirty {
		// Commit changes locally (without pushing to GitHub)
		commitMsg := fmt.Sprintf("[agentsquad] update from '%s' on %s (paused)", i.Title, time.Now().Format(time.RFC822))
		if err := i.gitWorktree.CommitChanges(commitMsg); err != nil {
			errs = append(errs, fmt.Errorf("failed to commit changes: %w", err))
			log.ErrorLog.Print(err)
			// Return early if we can't commit changes to avoid corrupted state
			return i.combineErrors(errs)
		}
	}

	// Detach from tmux session instead of closing to preserve session output
	if err := i.tmuxSession.DetachSafely(); err != nil {
		errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
		log.ErrorLog.Print(err)
		// Continue with pause process even if detach fails
	}

	// Check if worktree exists before trying to remove it
	if _, err := os.Stat(i.gitWorktree.GetWorktreePath()); err == nil {
		// Remove worktree but keep branch
		if err := i.gitWorktree.Remove(); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove git worktree: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}

		// Only prune if remove was successful
		if err := i.gitWorktree.Prune(); err != nil {
			errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}
	}

	if err := i.combineErrors(errs); err != nil {
		log.ErrorLog.Print(err)
		return err
	}

	i.SetStatus(Paused)
	_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())
	return nil
}

// Resume recreates the worktree and restarts the tmux session
func (i *Instance) Resume() error {
	if !i.started {
		return fmt.Errorf("cannot resume instance that has not been started")
	}
	if i.Status != Paused {
		return fmt.Errorf("can only resume paused instances")
	}

	// Check if branch is checked out
	if checked, err := i.gitWorktree.IsBranchCheckedOut(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to check if branch is checked out: %w", err)
	} else if checked {
		return fmt.Errorf("cannot resume: branch is checked out, please switch to a different branch")
	}

	// Setup git worktree
	if err := i.gitWorktree.Setup(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to setup git worktree: %w", err)
	}

	// Check if tmux session still exists from pause, otherwise create new one
	if i.tmuxSession.DoesSessionExist() {
		// Session exists, just restore PTY connection to it
		if err := i.tmuxSession.Restore(); err != nil {
			log.ErrorLog.Print(err)
			// If restore fails, fall back to creating new session
			if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
				log.ErrorLog.Print(err)
				// Cleanup git worktree if tmux session creation fails
				if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
					err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
					log.ErrorLog.Print(err)
				}
				return fmt.Errorf("failed to start new session: %w", err)
			}
		}
	} else {
		// Create new tmux session
		if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
			log.ErrorLog.Print(err)
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
				log.ErrorLog.Print(err)
			}
			return fmt.Errorf("failed to start new session: %w", err)
		}
	}

	if err := i.startDiffWatcher(); err != nil {
		return fmt.Errorf("failed to initialize diff watcher: %w", err)
	}

	i.MarkPreviewDirty()
	i.MarkDiffDirty()
	i.lastDiffCheck.Store(0)
	i.SetStatus(Running)

	// Sync branch from gitWorktree after resume
	i.GetBranch()

	return nil
}

// UpdateDiffStats updates the git diff statistics for this instance
func (i *Instance) UpdateDiffStats(now time.Time) error {
	if !i.started {
		i.diffStats = nil
		return nil
	}

	if i.Status == Paused {
		// Keep the previous diff stats if the instance is paused
		return nil
	}

	dirty := i.diffDirty.Swap(false)

	refresh := false
	force := false

	if dirty {
		refresh = true
		force = i.diffWatcherDisabled
	} else {
		if now.IsZero() {
			refresh = true
			force = true
		} else {
			last := time.Unix(0, i.lastDiffCheck.Load())
			if last.IsZero() || now.Sub(last) >= diffRefreshInterval {
				refresh = true
				force = true
			}
		}
	}

	if !refresh {
		return nil
	}

	i.diffMu.Lock()
	defer i.diffMu.Unlock()

	stats := i.gitWorktree.Diff(force)
	if stats.Error != nil {
		if strings.Contains(stats.Error.Error(), "base commit SHA not set") {
			// Worktree is not fully set up yet, not an error
			i.diffStats = nil
			i.MarkDiffDirty()
			return nil
		}
		i.MarkDiffDirty()
		return fmt.Errorf("failed to get diff stats: %w", stats.Error)
	}

	i.diffStats = stats
	i.lastDiffCheck.Store(now.UnixNano())
	return nil
}

// GetDiffStats returns the current git diff statistics
func (i *Instance) GetDiffStats() *git.DiffStats {
	return i.diffStats
}

// SendPrompt sends a prompt to the tmux session
func (i *Instance) SendPrompt(prompt string) error {
	if !i.started {
		return fmt.Errorf("instance not started")
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if err := i.tmuxSession.SendKeys(prompt); err != nil {
		return fmt.Errorf("error sending keys to tmux session: %w", err)
	}

	// Brief pause to prevent carriage return from being interpreted as newline
	time.Sleep(100 * time.Millisecond)
	if err := i.tmuxSession.TapEnter(); err != nil {
		return fmt.Errorf("error tapping enter: %w", err)
	}

	return nil
}

func (i *Instance) startDiffWatcher() error {
	if i.gitWorktree == nil {
		return fmt.Errorf("git worktree not initialized")
	}

	worktreePath := i.gitWorktree.GetWorktreePath()
	if worktreePath == "" {
		return fmt.Errorf("worktree path not set")
	}

	// Clean up any existing watcher before starting a new one
	if i.diffWatcher != nil {
		if err := i.stopDiffWatcher(); err != nil {
			log.WarningLog.Printf("failed to stop existing diff watcher for %s: %v", i.Title, err)
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		i.diffWatcherDisabled = true
		log.WarningLog.Printf("disabling diff watcher for %s: %v", i.Title, err)
		i.MarkDiffDirty()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	i.diffWatcher = watcher
	i.diffWatcherDisabled = false
	i.diffWatchCtx = ctx
	i.diffWatchCancel = cancel

	if err := i.addWatcherRecursive(worktreePath); err != nil {
		cancel()
		if closeErr := watcher.Close(); closeErr != nil {
			log.WarningLog.Printf("failed to close diff watcher for %s: %v", i.Title, closeErr)
		}
		i.diffWatcher = nil
		i.diffWatchCtx = nil
		i.diffWatchCancel = nil
		i.diffWatcherDisabled = true
		log.WarningLog.Printf("disabling diff watcher for %s: %v", i.Title, err)
		i.MarkDiffDirty()
		return nil
	}

	i.diffWatchWg.Add(1)
	go i.runDiffWatcher()

	// Trigger an initial diff computation after we start watching
	i.MarkDiffDirty()
	return nil
}

func (i *Instance) stopDiffWatcher() error {
	if i.diffWatchCancel != nil {
		i.diffWatchCancel()
	}

	var errs []error

	if i.diffWatcher != nil {
		i.diffWatchWg.Wait()
		if err := i.diffWatcher.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	i.diffWatcher = nil
	i.diffWatchCtx = nil
	i.diffWatchCancel = nil

	return i.combineErrors(errs)
}

func (i *Instance) runDiffWatcher() {
	defer i.diffWatchWg.Done()
	for {
		select {
		case <-i.diffWatchCtx.Done():
			return
		case event, ok := <-i.diffWatcher.Events:
			if !ok {
				return
			}

			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) != 0 {
				i.MarkDiffDirty()
			}

			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if err := i.addWatcherRecursive(event.Name); err != nil {
						log.WarningLog.Printf("failed to watch new directory %s for %s: %v",
							event.Name, i.Title, err)
					}
				}
			}
		case err, ok := <-i.diffWatcher.Errors:
			if !ok {
				return
			}
			log.WarningLog.Printf("diff watcher error for %s: %v", i.Title, err)
		}
	}
}

func (i *Instance) addWatcherRecursive(root string) error {
	if i.diffWatcher == nil {
		return nil
	}

	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			return nil
		}

		if i.shouldIgnoreWatch(path) {
			return filepath.SkipDir
		}

		if err := i.diffWatcher.Add(path); err != nil {
			return err
		}
		return nil
	})
}

func (i *Instance) shouldIgnoreWatch(path string) bool {
	if i.gitWorktree == nil {
		return false
	}

	root := i.gitWorktree.GetWorktreePath()
	if root == "" {
		return false
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}

	sep := string(os.PathSeparator)
	if rel == ".git" || strings.HasPrefix(rel, ".git"+sep) {
		return true
	}

	return false
}

// PreviewFullHistory captures the entire tmux pane output including full scrollback history
func (i *Instance) PreviewFullHistory() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContentWithOptions("-", "-")
}

// SetTmuxSession sets the tmux session for testing purposes
func (i *Instance) SetTmuxSession(session *tmux.TmuxSession) {
	i.tmuxSession = session
}

// SendKeys sends keys to the tmux session
func (i *Instance) SendKeys(keys string) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot send keys to instance that has not been started or is paused")
	}
	return i.tmuxSession.SendKeys(keys)
}
