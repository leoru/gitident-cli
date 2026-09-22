package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandContract(t *testing.T) {
	t.Setenv("HOME", "/home/k")
	cases := []struct{ in, expanded string }{
		{"~", "/home/k"},
		{"~/work", "/home/k/work"},
		{"~/work/", "/home/k/work"},
		{"/abs/path", "/abs/path"},
		{"./rel", "./rel"},
		{"~other/x", "~other/x"},
	}
	for _, c := range cases {
		if got := Expand(c.in); got != c.expanded {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.expanded)
		}
	}
	if got := Contract("/home/k/work/api"); got != "~/work/api" {
		t.Errorf("Contract = %q", got)
	}
	if got := Contract("/home/k"); got != "~" {
		t.Errorf("Contract(home) = %q", got)
	}
	if got := Contract("/home/kx/work"); got != "/home/kx/work" {
		t.Errorf("Contract must not match partial prefix, got %q", got)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"git@GitHub.com:Company/Infra.git":    "git@github.com:Company/Infra",
		"https://GitLab.Company.com/a/b.git/": "https://gitlab.company.com/a/b",
		"ssh://git@Host.COM:22/x/y.git":       "ssh://git@host.com:22/x/y",
		"https://github.com/owner/repo":       "https://github.com/owner/repo",
		"HTTPS://user@Example.org/Path/Repo":  "https://user@example.org/Path/Repo",
		"/local/path/repo.git":                "/local/path/repo",
	}
	for in, want := range cases {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	urls := []string{"git@github.com:a/b.git", "https://x.org/a", "ssh://git@h/x", "file:///tmp/x"}
	for _, u := range urls {
		if !IsURL(u) {
			t.Errorf("IsURL(%q) = false", u)
		}
	}
	for _, p := range []string{"/abs", "~/x", "./rel", "../up"} {
		if !IsPath(p) || IsURL(p) {
			t.Errorf("%q should be a path only", p)
		}
	}
	for _, s := range []string{"relative/dir", "github.com/a/b"} {
		if IsPath(s) || IsURL(s) {
			t.Errorf("%q should be neither", s)
		}
	}
}

func TestSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !SamePath(link, real) {
		t.Error("link and target should be the same path")
	}
	if !Within(filepath.Join(link, "repo", "missing"), real) {
		t.Error("non-existent child of link should be within target")
	}
	if Within(filepath.Join(dir, "realx"), real) {
		t.Error("sibling with shared prefix must not be within")
	}
	// On macOS t.TempDir() lives under /var, a symlink to /private/var.
	if got := Real(filepath.Join(link, "does", "not", "exist")); got != filepath.Join(Real(real), "does", "not", "exist") {
		t.Errorf("Real of missing path = %q", got)
	}
}

func TestProfileFromFragment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if p, ok := ProfileFromFragment(FragmentPathTilde("work")); !ok || p != "work" {
		t.Errorf("tilde form: %q %v", p, ok)
	}
	if p, ok := ProfileFromFragment(FragmentPath("personal")); !ok || p != "personal" {
		t.Errorf("abs form: %q %v", p, ok)
	}
	if _, ok := ProfileFromFragment("~/.gitconfig"); ok {
		t.Error("non-fragment path matched")
	}
}

func TestConfigPathAndGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(ConfigEnv, "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	if got, want := ConfigPath(), filepath.Join(home, ".config", "gitident", "profiles.yaml"); got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
	t.Setenv(ConfigEnv, "~/custom.yaml")
	if got := ConfigPath(); got != filepath.Join(home, "custom.yaml") {
		t.Errorf("ConfigPath override = %q", got)
	}
	if got := GlobalGitConfig(); got != filepath.Join(home, ".gitconfig") {
		t.Errorf("GlobalGitConfig default = %q", got)
	}
	xdg := filepath.Join(home, ".config", "git", "config")
	_ = os.MkdirAll(filepath.Dir(xdg), 0o755)
	_ = os.WriteFile(xdg, nil, 0o644)
	if got := GlobalGitConfig(); got != xdg {
		t.Errorf("GlobalGitConfig xdg fallback = %q", got)
	}
	_ = os.WriteFile(filepath.Join(home, ".gitconfig"), nil, 0o644)
	if got := GlobalGitConfig(); got != filepath.Join(home, ".gitconfig") {
		t.Errorf("~/.gitconfig must win when it exists, got %q", got)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/elsewhere/gc")
	if got := GlobalGitConfig(); got != "/elsewhere/gc" {
		t.Errorf("GIT_CONFIG_GLOBAL = %q", got)
	}
}
