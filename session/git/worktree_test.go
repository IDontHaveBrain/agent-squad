package git

import (
	"agent-squad/config"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHomeConfig(t *testing.T, branchPrefix string) string {
	t.Helper()

	tempHome := t.TempDir()

	t.Setenv("HOME", tempHome)

	configDir := filepath.Join(tempHome, ".agent-squad")
	require.NoError(t, os.MkdirAll(configDir, 0o755))

	configContent := `{
		"default_program": "test",
		"auto_yes": true,
		"daemon_poll_interval": 1500,
		"branch_prefix": "` + branchPrefix + `"
	}`

	configPath := filepath.Join(configDir, config.ConfigFileName)
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644))

	return tempHome
}

func initGitRepo(t *testing.T, baseDir string) string {
	t.Helper()

	repoPath := filepath.Join(baseDir, "repo")
	_, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)

	return repoPath
}

func TestNewGitWorktreeBranchNameHandling(t *testing.T) {
	t.Run("falls back to prefix when sanitized name is empty", func(t *testing.T) {
		tempHome := setupTestHomeConfig(t, "tester/")
		repoPath := initGitRepo(t, tempHome)

		worktree, branchName, err := NewGitWorktree(repoPath, "/🔥")
		require.NoError(t, err)
		require.NotNil(t, worktree)

		assert.Equal(t, "tester", branchName)
		assert.Equal(t, branchName, worktree.GetBranchName())
	})

	t.Run("preserves nested names when sanitized name remains valid", func(t *testing.T) {
		tempHome := setupTestHomeConfig(t, "tester/")
		repoPath := initGitRepo(t, tempHome)

		worktree, branchName, err := NewGitWorktree(repoPath, "feature/subtask")
		require.NoError(t, err)
		require.NotNil(t, worktree)

		assert.Equal(t, "feature/subtask", branchName)
		assert.Equal(t, branchName, worktree.GetBranchName())
	})
}
