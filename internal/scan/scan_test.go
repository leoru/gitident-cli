package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a"))
	mkRepo(t, filepath.Join(root, "a", "nested")) // inside a repo: not descended
	mkRepo(t, filepath.Join(root, "group", "b"))
	mkRepo(t, filepath.Join(root, "group", "deep", "c"))
	mkRepo(t, filepath.Join(root, "node_modules", "pkg"))
	mkRepo(t, filepath.Join(root, "ios", "Pods", "x"))
	mkRepo(t, filepath.Join(root, ".hidden", "d"))
	mkRepo(t, filepath.Join(root, "skipme", "e"))
	mkRepo(t, filepath.Join(root, "other", "skip-by-path"))
	// Worktree / submodule style: .git is a file.
	wt := filepath.Join(root, "wt")
	_ = os.MkdirAll(wt, 0o755)
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644)
	// An empty .git directory is not a repository; its children are still scanned.
	_ = os.MkdirAll(filepath.Join(root, "broken", ".git"), 0o755)
	mkRepo(t, filepath.Join(root, "broken", "inner"))
	// Symlinks inside the tree are not followed.
	if err := os.Symlink(filepath.Join(root, "group"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := Find([]string{root, filepath.Join(root, "missing")}, Options{Ignore: []string{"skip*", filepath.Join(root, "other", "skip-by-path")}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "a"),
		filepath.Join(root, "broken", "inner"),
		filepath.Join(root, "group", "b"),
		filepath.Join(root, "group", "deep", "c"),
		filepath.Join(root, "wt"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Find =\n%q\nwant\n%q", got, want)
	}
}

func TestFindSymlinkedRootAndDedup(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	mkRepo(t, filepath.Join(real, "r"))
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := Find([]string{link, real}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(link, "r")}; !reflect.DeepEqual(got, want) {
		t.Errorf("Find = %q, want %q", got, want)
	}
	// A root that is itself a repository is reported.
	got, _ = Find([]string{filepath.Join(real, "r")}, Options{})
	if len(got) != 1 {
		t.Errorf("root repo not found: %q", got)
	}
}

func TestFindLargeTree(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		repo := filepath.Join(root, fmt.Sprintf("org%d", i%10), fmt.Sprintf("repo%03d", i))
		mkRepo(t, repo)
		_ = os.MkdirAll(filepath.Join(repo, "src", "pkg"), 0o755)
	}
	start := time.Now()
	got, err := Find([]string{root}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 {
		t.Errorf("found %d repos", len(got))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("scan took %v", d)
	}
}
