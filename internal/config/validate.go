package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/leoru/gitident-cli/internal/paths"
)

// Report holds validation results. Errors block `sync`; warnings are informational.
type Report struct {
	Errors   []string
	Warnings []string
}

// OK reports whether there are no errors.
func (r Report) OK() bool { return len(r.Errors) == 0 }

func (r *Report) errorf(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *Report) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// ValidGPGFormats are the values git accepts for gpg.format.
var ValidGPGFormats = []string{"openpgp", "ssh", "x509"}

var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// RepoKey returns the comparison key for a repos entry: the resolved absolute path
// for paths, the normalised URL for URLs, and "" for entries that are neither.
func RepoKey(entry string) string {
	switch {
	case paths.IsURL(entry):
		return "url:" + paths.NormalizeURL(entry)
	case paths.IsPath(entry):
		return "path:" + paths.Real(paths.Abs(entry))
	}
	return ""
}

// IsSSHKeyPath reports whether an SSH signing key value is a file path rather than
// a literal "key::..." / "ssh-..." public key.
func IsSSHKeyPath(v string) bool {
	return v != "" && !strings.HasPrefix(v, "key::") && !strings.HasPrefix(v, "ssh-") &&
		!strings.HasPrefix(v, "ecdsa-") && !strings.HasPrefix(v, "sk-")
}

// Validate checks the config for errors and warnings.
func (c *Config) Validate() Report {
	var r Report
	switch c.Version {
	case CurrentVersion:
	case 0:
		r.errorf("missing `version: %d`", CurrentVersion)
	default:
		r.errorf("unsupported version %d (this gitident understands version %d)", c.Version, CurrentVersion)
	}

	if len(c.Profiles) == 0 {
		r.errorf("no profiles defined")
	}

	owner := map[string]string{} // repo key → profile
	for _, name := range c.ProfileNames() {
		p := c.Profiles[name]
		if !profileNameRe.MatchString(name) {
			r.errorf("profile %q: name may only contain letters, digits, '.', '_' and '-' (it becomes a file name)", name)
		}
		if strings.TrimSpace(p.Name) == "" {
			r.errorf("profile %q: `name` is required", name)
		}
		if strings.TrimSpace(p.Email) == "" {
			r.errorf("profile %q: `email` is required", name)
		}
		if p.GPGFormat != "" && !contains(ValidGPGFormats, p.GPGFormat) {
			r.errorf("profile %q: unknown gpg_format %q (want one of %s)", name, p.GPGFormat, strings.Join(ValidGPGFormats, ", "))
		}
		for _, k := range sortedKeys(p.Extra) {
			if !validExtraKey(k) {
				r.errorf("profile %q: extra key %q must look like `section.key` or `section.subsection.key`", name, k)
			}
		}
		if p.SSHKey != "" {
			if _, err := os.Stat(paths.Expand(p.SSHKey)); err != nil {
				r.warnf("profile %q: ssh_key %s does not exist", name, p.SSHKey)
			}
		}
		if p.GPGFormat == "ssh" && IsSSHKeyPath(p.SigningKey) {
			if _, err := os.Stat(paths.Expand(p.SigningKey)); err != nil {
				r.warnf("profile %q: signing_key %s does not exist", name, p.SigningKey)
			}
		}
		if p.GPGSign != nil && *p.GPGSign && p.SigningKey == "" {
			r.warnf("profile %q: gpgsign is true but no signing_key is set (git will fall back to the committer identity)", name)
		}

		seen := map[string]bool{}
		for _, repo := range p.Repos {
			key := RepoKey(repo)
			if key == "" {
				r.errorf("profile %q: repo %q is neither a path (starting with /, ~ or .) nor a URL (scheme:// or user@host:)", name, repo)
				continue
			}
			if strings.HasPrefix(repo, ".") {
				r.errorf("profile %q: repo %q must be absolute or start with ~/ (relative paths are ambiguous in gitconfig)", name, repo)
				continue
			}
			if seen[key] {
				r.warnf("profile %q: repo %q is listed twice", name, repo)
				continue
			}
			seen[key] = true
			if other, dup := owner[key]; dup {
				r.errorf("repo %q is listed under both profile %q and profile %q", repo, other, name)
				continue
			}
			owner[key] = name
		}
	}

	for i, rule := range c.Rules {
		label := fmt.Sprintf("rule %d", i+1)
		if rule.Profile == "" {
			r.errorf("%s: `profile` is required", label)
		} else if _, ok := c.Profiles[rule.Profile]; !ok {
			r.errorf("%s: unknown profile %q", label, rule.Profile)
		}
		if len(rule.Dirs) == 0 && len(rule.Remotes) == 0 {
			r.errorf("%s: needs at least one of `dirs` or `remotes`", label)
		}
		for _, d := range rule.Dirs {
			if !strings.HasPrefix(d, "/") && !strings.HasPrefix(d, "~/") && d != "~" {
				r.errorf("%s: dir %q must be absolute or start with ~/", label, d)
			}
		}
		for _, u := range rule.Remotes {
			if strings.TrimSpace(u) == "" {
				r.errorf("%s: empty remote pattern", label)
			}
		}
	}

	// A path listed under one profile but also covered by another profile's dirs rule
	// works (repos win), but is worth pointing out.
	for _, name := range c.ProfileNames() {
		for _, repo := range c.Profiles[name].Repos {
			if !paths.IsPath(repo) || paths.IsURL(repo) {
				continue
			}
			for i, rule := range c.Rules {
				if rule.Profile == name {
					continue
				}
				for _, d := range rule.Dirs {
					if paths.Within(repo, d) {
						r.warnf("repo %s (profile %q) is inside %s from rule %d (profile %q); the repos entry wins", repo, name, d, i+1, rule.Profile)
					}
				}
			}
		}
	}
	return r
}

func validExtraKey(k string) bool {
	first := strings.IndexByte(k, '.')
	last := strings.LastIndexByte(k, '.')
	if first <= 0 || last == len(k)-1 {
		return false
	}
	return !strings.ContainsAny(k[:first], " \t\"") && !strings.ContainsAny(k[last+1:], " \t\".")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
