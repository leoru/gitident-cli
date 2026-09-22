package match

import (
	"testing"

	"github.com/leoru/gitident-cli/internal/config"
)

func TestExpect(t *testing.T) {
	t.Setenv("HOME", "/home/k")
	cfg := &config.Config{
		Version: 1,
		Profiles: map[string]*config.Profile{
			"personal": {Name: "P", Email: "p@x", Repos: []string{"~/work/side"}},
			"work":     {Name: "W", Email: "w@x", Repos: []string{"https://github.com/me/work-notes"}},
			"oss":      {Name: "O", Email: "o@x"},
		},
		Rules: []config.Rule{
			{Profile: "personal", Dirs: []string{"~/code"}},
			{Profile: "work", Dirs: []string{"~/work/"}, Remotes: []string{"git@github.com:company/**"}},
			{Profile: "oss", Remotes: []string{"https://github.com/oss/**"}},
		},
	}
	cases := []struct {
		name   string
		target Target
		want   string
	}{
		{"dir rule", Target{GitDirs: []string{"/home/k/code/x/.git"}}, "personal"},
		{"dir rule nested", Target{GitDirs: []string{"/home/k/code/a/b/.git"}}, "personal"},
		{"no match", Target{GitDirs: []string{"/home/k/tmp/x/.git"}}, ""},
		{"prefix is not a match", Target{GitDirs: []string{"/home/k/codex/x/.git"}}, ""},
		{"later rule wins", Target{GitDirs: []string{"/home/k/code/x/.git"}, RemoteURLs: []string{"git@github.com:company/x.git"}}, "work"},
		{"later remote rule over dir", Target{GitDirs: []string{"/home/k/work/y/.git"}, RemoteURLs: []string{"https://github.com/oss/lib"}}, "oss"},
		{"repos path beats rules", Target{GitDirs: []string{"/home/k/work/side/.git"}}, "personal"},
		{"repos path is exact", Target{GitDirs: []string{"/home/k/work/side/nested/.git"}}, "work"},
		{"repos url without .git", Target{RemoteURLs: []string{"https://github.com/me/work-notes"}}, "work"},
		{"repos url with .git", Target{GitDirs: []string{"/home/k/code/n/.git"}, RemoteURLs: []string{"https://github.com/me/work-notes.git"}}, "work"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Expect(cfg, c.target); got.Profile != c.want {
				t.Errorf("Expect = %+v, want %q", got, c.want)
			}
		})
	}
}
