package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolated(m)) }

func TestParseVersion(t *testing.T) {
	cases := map[string]Version{
		"git version 2.45.2\n":               {Major: 2, Minor: 45, Patch: 2},
		"git version 2.39.3 (Apple Git-146)": {Major: 2, Minor: 39, Patch: 3},
		"git version 2.36.0.windows.1":       {Major: 2, Minor: 36, Patch: 0},
		"git version 3.0":                    {Major: 3, Minor: 0},
	}
	for in, want := range cases {
		got, err := ParseVersion(in)
		if err != nil || got.Major != want.Major || got.Minor != want.Minor || got.Patch != want.Patch {
			t.Errorf("ParseVersion(%q) = %+v, %v", in, got, err)
		}
	}
	if _, err := ParseVersion("nope"); err == nil {
		t.Error("expected error")
	}
	if !(Version{Major: 2, Minor: 36}).AtLeast(2, 36) || (Version{Major: 2, Minor: 35, Patch: 9}).AtLeast(2, 36) {
		t.Error("AtLeast is wrong")
	}
}

func TestConfigAndOrigin(t *testing.T) {
	repo := testutil.InitRepo(t, filepath.Join(t.TempDir(), "r"))
	if _, ok, err := Config(repo, "user.nothing"); ok || err != nil {
		t.Fatalf("unset key: ok=%v err=%v", ok, err)
	}
	if _, _, err := Config(repo, "invalid key"); err == nil {
		t.Error("invalid key must be a real error, not 'unset'")
	}
	testutil.Git(t, repo, "config", "user.email", "a@b.c")
	v, origin, ok, err := ConfigOrigin(repo, "user.email")
	if err != nil || !ok || v != "a@b.c" {
		t.Fatalf("ConfigOrigin = %q %q %v %v", v, origin, ok, err)
	}
	if !strings.HasSuffix(origin, filepath.Join(".git", "config")) || !filepath.IsAbs(origin) {
		t.Errorf("origin = %q", origin)
	}
}

func TestRepoPlumbing(t *testing.T) {
	root := t.TempDir()
	repo := testutil.InitRepo(t, filepath.Join(root, "main"))
	testutil.Git(t, repo, "remote", "add", "origin", "git@github.com:me/x.git")
	testutil.Git(t, repo, "remote", "add", "up", "https://github.com/up/x")
	sub := filepath.Join(repo, "a", "b")
	_ = os.MkdirAll(sub, 0o755)

	top, err := TopLevel(sub)
	if err != nil || filepath.Base(top) != "main" {
		t.Errorf("TopLevel = %q %v", top, err)
	}
	remotes, err := Remotes(sub)
	if err != nil || len(remotes) != 2 || remotes[0].Name != "origin" || remotes[1].URL != "https://github.com/up/x" {
		t.Errorf("Remotes = %+v %v", remotes, err)
	}
	gd, _ := GitDir(repo)
	cd, _ := CommonDir(sub)
	if gd != cd || filepath.Base(cd) != ".git" {
		t.Errorf("GitDir %q CommonDir %q", gd, cd)
	}

	// Linked worktree: separate git dir, shared common dir.
	testutil.Git(t, repo, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(root, "wt")
	testutil.Git(t, repo, "worktree", "add", "-q", wt)
	wgd, _ := GitDir(wt)
	wcd, _ := CommonDir(wt)
	if !strings.Contains(wgd, filepath.Join(".git", "worktrees")) {
		t.Errorf("worktree GitDir = %q", wgd)
	}
	if wcd != cd {
		t.Errorf("worktree CommonDir = %q, want %q", wcd, cd)
	}

	if _, err := TopLevel(root); err == nil || !strings.Contains(err.Error(), ErrNotRepo.Error()) {
		t.Errorf("expected ErrNotRepo, got %v", err)
	}
}

func TestFileHelpers(t *testing.T) {
	f := filepath.Join(t.TempDir(), "cfg")
	if vals, err := FileGetAll(f, "include.path"); err != nil || vals != nil {
		t.Fatalf("missing file: %v %v", vals, err)
	}
	for _, v := range []string{"~/a.gitconfig", "~/b (1).gitconfig"} {
		if err := FileAdd(f, "include.path", v); err != nil {
			t.Fatal(err)
		}
	}
	if err := FileUnsetValue(f, "include.path", "~/b (1).gitconfig"); err != nil {
		t.Fatal(err)
	}
	if err := FileUnsetValue(f, "include.path", "~/never-there"); err != nil {
		t.Fatalf("unsetting a missing value should be a no-op: %v", err)
	}
	vals, _ := FileGetAll(f, "include.path")
	if len(vals) != 1 || vals[0] != "~/a.gitconfig" {
		t.Errorf("vals = %q", vals)
	}
	entries, err := ListFile(f)
	if err != nil || len(entries) != 1 || entries[0].Key != "include.path" {
		t.Errorf("ListFile = %+v %v", entries, err)
	}
}

func TestParseList(t *testing.T) {
	out := "file:/a/.gitconfig\x00user.name\nKirill K\x00file:/a/.gitconfig\x00core.bare\x00"
	got := ParseList(out, true)
	if len(got) != 2 || got[0].Origin != "/a/.gitconfig" || got[0].Value != "Kirill K" || got[1].Value != "true" {
		t.Errorf("ParseList = %+v", got)
	}
}
