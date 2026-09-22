// Package inspect determines the effective identity of a repository and which
// gitident profile it corresponds to.
package inspect

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/match"
	"github.com/leoru/gitident-cli/internal/paths"
)

// IdentityKeys are the effective settings `which` reports, in display order.
var IdentityKeys = []string{"user.name", "user.email", "user.signingkey", "commit.gpgsign", "gpg.format", "core.sshCommand"}

// How the effective profile was recognised.
const (
	ViaFragment = "fragment" // user.email comes from a gitident fragment
	ViaEmail    = "email"    // user.email matches a profile but was set elsewhere
	// ViaMaterialized: user.email is a copy gitident wrote into .git/config.
	ViaMaterialized = "materialized"
)

// Result is everything known about one directory's identity.
type Result struct {
	Dir       string
	Repo      string // working tree root; "" outside a repository
	GitDir    string
	CommonDir string
	Remotes   []gitx.Remote

	Values  map[string]string // effective values of IdentityKeys ("" = unset)
	Origins map[string]string // origin file of each set IdentityKey

	Profile   string // effective profile ("" if not recognised)
	Via       string // ViaFragment or ViaEmail
	Ambiguous []string
	Pinned    string // profile pinned via include.path in .git/config
	// Materialized is the profile copied into .git/config by `gitident apply`.
	Materialized string
	Strict       bool // user.useConfigOnly is in effect
	Expected     match.Expectation
	HasProfile   bool // cfg was available
}

// Email is a shortcut for the effective user.email.
func (r *Result) Email() string { return r.Values["user.email"] }

// OutsideGitident reports whether user.email was set somewhere other than a fragment.
func (r *Result) OutsideGitident() bool {
	return r.Email() != "" && r.Via != ViaFragment && r.Via != ViaMaterialized
}

// LocalConfig returns the repository's shared config file ("" outside a repository).
func (r *Result) LocalConfig() string {
	if r.CommonDir == "" {
		return ""
	}
	return filepath.Join(r.CommonDir, "config")
}

// TargetProfile returns the profile gitident means this repository to have: its pin,
// else what rules and repos lists say.
func (r *Result) TargetProfile() string {
	if r.Pinned != "" {
		return r.Pinned
	}
	return r.Expected.Profile
}

// Inspect examines dir. cfg may be nil, in which case profile matching by email
// and the expected profile are skipped.
func Inspect(cfg *config.Config, dir string) (*Result, error) {
	dir = paths.Abs(dir)
	if st, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, errors.New(dir + ": not a directory")
	}
	r := &Result{Dir: dir, Values: map[string]string{}, Origins: map[string]string{}}

	if out, err := gitx.Run(dir, "rev-parse", "--absolute-git-dir", "--git-common-dir", "--show-toplevel"); err == nil {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) >= 2 {
			r.GitDir = lines[0]
			r.CommonDir = lines[1]
			if !filepath.IsAbs(r.CommonDir) {
				r.CommonDir = filepath.Join(dir, r.CommonDir)
			}
			r.CommonDir = paths.Real(r.CommonDir)
		}
		if len(lines) >= 3 {
			r.Repo = lines[2]
		}
	}

	out, err := gitx.Run(dir, "config", "--list", "--show-origin", "-z")
	if err != nil {
		var ge *gitx.Error
		if !errors.As(err, &ge) || ge.ExitCode != 1 || strings.TrimSpace(ge.Stderr) != "" {
			return nil, err
		}
	}
	wanted := map[string]string{}
	for _, k := range IdentityKeys {
		wanted[strings.ToLower(k)] = k
	}
	localConfig := r.LocalConfig()
	for _, e := range gitx.ParseList(out, true) {
		origin := e.Origin
		if origin != "" && !filepath.IsAbs(origin) && !strings.Contains(origin, ":") {
			// git prints repo-local files (".git/config") relative to the top of
			// the working tree, not to the directory it was run from.
			base := dir
			if r.Repo != "" {
				base = r.Repo
			}
			origin = filepath.Join(base, origin)
		}
		key := strings.ToLower(e.Key)
		switch {
		case wanted[key] != "":
			r.Values[wanted[key]] = e.Value
			r.Origins[wanted[key]] = origin
		case key == "user.useconfigonly":
			r.Strict = isTrue(e.Value)
		case key == "include.path" && localConfig != "" && paths.SamePath(origin, localConfig):
			if p, ok := paths.ProfileFromFragment(resolveInclude(e.Value, origin)); ok {
				r.Pinned = p
			}
		case key == "gitident.profile" && localConfig != "" && paths.SamePath(origin, localConfig):
			r.Materialized = e.Value
		case strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".url"):
			name := e.Key[len("remote.") : len(e.Key)-len(".url")]
			r.Remotes = append(r.Remotes, gitx.Remote{Name: name, URL: e.Value})
		}
	}

	if origin := r.Origins["user.email"]; origin != "" {
		if p, ok := paths.ProfileFromFragment(origin); ok {
			r.Profile, r.Via = p, ViaFragment
		} else if r.Materialized != "" && paths.SamePath(origin, localConfig) {
			// A copy by `gitident apply` — unless user.email was edited since.
			if p := profileOf(cfg, r.Materialized); cfg == nil || (p != nil && strings.EqualFold(p.Email, r.Email())) {
				r.Profile, r.Via = r.Materialized, ViaMaterialized
			}
		}
	}
	if cfg != nil {
		r.HasProfile = true
		if r.Profile == "" && r.Email() != "" {
			r.Profile, r.Ambiguous = ByEmail(cfg, r.Email(), r.Values["user.name"])
			if r.Profile != "" {
				r.Via = ViaEmail
			}
		}
		if r.GitDir != "" {
			r.Expected = match.Expect(cfg, r.Target())
		}
	}
	return r, nil
}

// Target returns the match target for this repository.
func (r *Result) Target() match.Target {
	t := match.Target{GitDirs: []string{r.GitDir}}
	if r.Repo != "" {
		// The unresolved path as reached by the user (e.g. through a symlinked dir).
		for _, base := range []string{r.Dir, r.Repo} {
			if g := filepath.Join(base, ".git"); g != r.GitDir {
				if st, err := os.Stat(g); err == nil && st.IsDir() {
					t.GitDirs = append(t.GitDirs, g)
				}
			}
		}
	}
	for _, rm := range r.Remotes {
		t.RemoteURLs = append(t.RemoteURLs, rm.URL)
	}
	return t
}

// ByEmail finds the profile with the given email (case-insensitive). When several
// profiles share it, the one whose name also matches wins; the rest are returned
// as ambiguous alternatives.
func ByEmail(cfg *config.Config, email, name string) (string, []string) {
	var candidates []string
	for _, n := range cfg.ProfileNames() {
		if strings.EqualFold(cfg.Profiles[n].Email, email) {
			candidates = append(candidates, n)
		}
	}
	if len(candidates) <= 1 {
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		return "", nil
	}
	for i, n := range candidates {
		if cfg.Profiles[n].Name == name {
			rest := append(append([]string{}, candidates[:i]...), candidates[i+1:]...)
			return n, rest
		}
	}
	return candidates[0], candidates[1:]
}

func profileOf(cfg *config.Config, name string) *config.Profile {
	if cfg == nil {
		return nil
	}
	return cfg.Profiles[name]
}

// resolveInclude resolves an include.path value relative to the file containing it.
func resolveInclude(value, origin string) string {
	v := paths.Expand(value)
	if !filepath.IsAbs(v) && origin != "" {
		v = filepath.Join(filepath.Dir(origin), v)
	}
	return v
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1", "":
		return true
	}
	return false
}
