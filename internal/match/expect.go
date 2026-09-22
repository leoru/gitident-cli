package match

import (
	"fmt"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/paths"
)

// Target describes a repository the way git's includeIf conditions see it.
type Target struct {
	// GitDirs are the repository's git directory as discovered and resolved
	// (git tries both); duplicates are fine.
	GitDirs []string
	// RemoteURLs are all remote.*.url values visible to the repository.
	RemoteURLs []string
}

// Expectation is the profile a repository should get from profiles.yaml.
type Expectation struct {
	Profile string // "" when nothing matches
	Reason  string // human-readable description of the winning entry
}

// Expect evaluates the managed block's includeIf entries in order, exactly as git
// would, and returns the last match.
func Expect(cfg *config.Config, t Target) Expectation {
	var e Expectation
	for i, r := range cfg.Rules {
		for _, d := range r.Dirs {
			if gitdirMatches(strings.TrimRight(d, "/")+"/**", t.GitDirs) {
				e = Expectation{r.Profile, fmt.Sprintf("rule %d: dirs %s", i+1, d)}
			}
		}
		for _, u := range r.Remotes {
			if anyGlob(u, t.RemoteURLs) {
				e = Expectation{r.Profile, fmt.Sprintf("rule %d: remote %s", i+1, u)}
			}
		}
	}
	for _, name := range cfg.ProfileNames() {
		for _, repo := range cfg.Profiles[name].Repos {
			if paths.IsURL(repo) {
				u := strings.TrimSuffix(strings.TrimRight(repo, "/"), ".git")
				if anyGlob(u, t.RemoteURLs) || anyGlob(u+".git", t.RemoteURLs) {
					e = Expectation{name, "repos: " + repo}
				}
				continue
			}
			if gitdirMatches(strings.TrimRight(repo, "/")+"/.git", t.GitDirs) {
				e = Expectation{name, "repos: " + repo}
			}
		}
	}
	return e
}

// gitdirMatches applies a gitdir: pattern (in ~ or absolute form) to each git dir,
// also trying the pattern's symlink-resolved form as the rendered block does.
func gitdirMatches(pattern string, gitDirs []string) bool {
	pats := []string{paths.Expand(pattern)}
	if !HasGlob(strings.TrimSuffix(pattern, "/**")) {
		base := strings.TrimSuffix(pats[0], "/**")
		if real := paths.Real(base); real != base {
			pats = append(pats, real+strings.TrimPrefix(pats[0], base))
		}
	}
	for _, p := range pats {
		for _, g := range gitDirs {
			if Glob(p, g) {
				return true
			}
		}
	}
	return false
}

func anyGlob(pattern string, values []string) bool {
	for _, v := range values {
		if Glob(pattern, v) {
			return true
		}
	}
	return false
}
