// Package testutil provides a hermetic git environment for tests.
package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// RunIsolated runs the tests with HOME, XDG_CONFIG_HOME and the global git config
// pointed at a throwaway directory, so the developer's real configuration is never
// read or written. Use it from TestMain.
func RunIsolated(m *testing.M) int {
	home, err := os.MkdirTemp("", "gitident-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(home)
	Isolate(home)
	return m.Run()
}

// Isolate points all user-level configuration at home.
func Isolate(home string) {
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   "",
		"GITIDENT_CONFIG":     "",
		"GIT_AUTHOR_NAME":     "",
		"GIT_AUTHOR_EMAIL":    "",
		"GIT_COMMITTER_NAME":  "",
		"GIT_COMMITTER_EMAIL": "",
		"EMAIL":               "",
		"GIT_DIR":             "",
		"GIT_WORK_TREE":       "",
		"GNUPGHOME":           filepath.Join(home, ".gnupg"),
	} {
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
}

// Home sets up a fresh isolated home directory for a single test and returns it.
func Home(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// Git runs git in dir and fails the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// InitRepo creates a git repository at dir (creating parents) and returns dir.
func InitRepo(t *testing.T, dir string, remotes ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q")
	for i, u := range remotes {
		name := "origin"
		if i > 0 {
			name = fmt.Sprintf("remote%d", i)
		}
		Git(t, dir, "remote", "add", name, u)
	}
	return dir
}

// WriteFile writes content to path, creating parent directories.
func WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
