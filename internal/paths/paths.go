// Package paths holds every well-known name and location gitident uses, plus
// helpers for tilde expansion, symlink resolution and remote-URL normalisation.
package paths

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// AppName is the binary name and the directory name used for config and fragments.
	AppName = "gitident"
	// ConfigEnv overrides the location of profiles.yaml.
	ConfigEnv = "GITIDENT_CONFIG"
	// ConfigFileName is the base name of the profiles file.
	ConfigFileName = "profiles.yaml"
	// FragmentDirTilde is where rendered per-profile gitconfig fragments live, in the
	// portable ~ form that is written into gitconfig files.
	FragmentDirTilde = "~/.gitconfig.d/" + AppName
	// FragmentExt is the file extension of rendered fragments.
	FragmentExt = ".gitconfig"
)

// Home returns the user's home directory ($HOME on Unix).
func Home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

// ConfigPath returns the profiles.yaml location:
// $GITIDENT_CONFIG, else $XDG_CONFIG_HOME/gitident/profiles.yaml, else ~/.config/gitident/profiles.yaml.
func ConfigPath() string {
	if p := os.Getenv(ConfigEnv); p != "" {
		return Expand(p)
	}
	return filepath.Join(xdgConfigHome(), AppName, ConfigFileName)
}

func xdgConfigHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return x
	}
	return filepath.Join(Home(), ".config")
}

// StrictFragment is the name of the generated fragment that sets
// user.useConfigOnly. It starts with "_", which profile names cannot, so it
// never collides with a profile's fragment.
const StrictFragment = "_strict"

// CloneFragment is the generated fragment that points init.templateDir at
// TemplateDirTilde (clone_hook).
const CloneFragment = "_clone"

// TemplateDirTilde is gitident's git template directory; its hooks are copied
// into every repository created by git clone / git init.
const TemplateDirTilde = FragmentDirTilde + "/template"

// HookName is the hook gitident installs in the template directory.
const HookName = "post-checkout"

// FragmentDir returns the absolute fragment directory.
func FragmentDir() string { return Expand(FragmentDirTilde) }

// FragmentPath returns the absolute path of a profile's fragment.
func FragmentPath(profile string) string {
	return filepath.Join(FragmentDir(), profile+FragmentExt)
}

// FragmentPathTilde returns a profile's fragment path in ~ form, as written into gitconfig.
func FragmentPathTilde(profile string) string {
	return FragmentDirTilde + "/" + profile + FragmentExt
}

// ProfileFromFragment reports which profile a fragment path belongs to, if the path
// (absolute or ~ form) points into the fragment directory.
func ProfileFromFragment(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	p = Expand(p)
	if !strings.HasSuffix(p, FragmentExt) {
		return "", false
	}
	if !SamePath(filepath.Dir(p), FragmentDir()) {
		return "", false
	}
	return strings.TrimSuffix(filepath.Base(p), FragmentExt), true
}

// GlobalGitConfig returns the global gitconfig file that gitident edits.
// It honours $GIT_CONFIG_GLOBAL; otherwise uses ~/.gitconfig, falling back to
// $XDG_CONFIG_HOME/git/config when that exists and ~/.gitconfig does not.
func GlobalGitConfig() string {
	if p := os.Getenv("GIT_CONFIG_GLOBAL"); p != "" {
		return Expand(p)
	}
	home := filepath.Join(Home(), ".gitconfig")
	if _, err := os.Stat(home); err == nil {
		return home
	}
	xdg := filepath.Join(xdgConfigHome(), "git", "config")
	if _, err := os.Stat(xdg); err == nil {
		return xdg
	}
	return home
}

// Expand replaces a leading "~" or "~/" with the home directory and cleans the path.
// Paths of the form "~user" are returned unchanged.
func Expand(p string) string {
	switch {
	case p == "~":
		return Home()
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(Home(), p[2:])
	}
	return p
}

// Contract turns an absolute path under the home directory into its ~ form.
func Contract(p string) string {
	home := filepath.Clean(Home())
	if home == "/" || home == "" {
		return p
	}
	c := filepath.Clean(p)
	if c == home {
		return "~"
	}
	if strings.HasPrefix(c, home+string(filepath.Separator)) {
		return "~/" + filepath.ToSlash(c[len(home)+1:])
	}
	// The home directory itself may be reached through a symlink (macOS /var → /private/var).
	if rh := Real(home); rh != home {
		if c == rh {
			return "~"
		}
		if strings.HasPrefix(c, rh+string(filepath.Separator)) {
			return "~/" + filepath.ToSlash(c[len(rh)+1:])
		}
	}
	return p
}

// Abs expands ~ and makes the path absolute and clean.
func Abs(p string) string {
	p = Expand(p)
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return filepath.Clean(p)
}

// Real resolves symlinks in p. Components that do not exist are kept as-is after
// resolving the longest existing prefix, so the result is usable for comparisons
// even for paths that have not been created yet.
func Real(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(Real(parent), filepath.Base(p))
}

// SamePath reports whether two paths refer to the same location after ~ expansion,
// cleaning and symlink resolution.
func SamePath(a, b string) bool {
	a, b = Abs(a), Abs(b)
	return a == b || Real(a) == Real(b)
}

// Within reports whether p is dir itself or lies below it (after symlink resolution).
func Within(p, dir string) bool {
	within := func(p, dir string) bool {
		return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
	}
	p, dir = Abs(p), Abs(dir)
	return within(p, dir) || within(Real(p), Real(dir))
}

var scpLike = regexp.MustCompile(`^[^/@\s:]+@[^/\s:]+:`)

// IsURL reports whether s looks like a remote URL: "scheme://..." or scp-like "user@host:path".
func IsURL(s string) bool {
	return strings.Contains(s, "://") || scpLike.MatchString(s)
}

// IsPath reports whether s looks like a filesystem path ("/", "~" or "." prefix).
func IsPath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.HasPrefix(s, ".")
}

// NormalizeURL returns a canonical form of a remote URL for comparisons:
// trailing "/" and ".git" stripped, host lower-cased.
func NormalizeURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		scheme := strings.ToLower(u[:i])
		rest := u[i+3:]
		hostEnd := strings.IndexByte(rest, '/')
		if hostEnd < 0 {
			hostEnd = len(rest)
		}
		host := rest[:hostEnd]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[:at+1] + strings.ToLower(host[at+1:])
		} else {
			host = strings.ToLower(host)
		}
		return scheme + "://" + host + rest[hostEnd:]
	}
	if loc := scpLike.FindStringIndex(u); loc != nil {
		prefix := u[:loc[1]] // user@host:
		at := strings.IndexByte(prefix, '@')
		return prefix[:at+1] + strings.ToLower(prefix[at+1:]) + u[loc[1]:]
	}
	return u
}
