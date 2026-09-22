// Package gitx is a thin wrapper over the git binary.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Error is a failed git invocation.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.ExitCode)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

// ErrNotRepo is returned when a directory is not inside a git repository.
var ErrNotRepo = errors.New("not a git repository")

// Run executes git with args in dir ("" for the current directory) and returns stdout.
func Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), &Error{Args: args, ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
		}
		return "", fmt.Errorf("running git: %w", err)
	}
	return stdout.String(), nil
}

// isUnset reports whether err is git config's "key not found": exit 1, no stderr.
func isUnset(err error) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.ExitCode == 1 && strings.TrimSpace(ge.Stderr) == ""
}

// Config returns the effective value of key as seen from dir.
// ok is false if the key is unset.
func Config(dir, key string) (value string, ok bool, err error) {
	out, err := Run(dir, "config", "--get", key)
	if err != nil {
		if isUnset(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSuffix(out, "\n"), true, nil
}

// ConfigOrigin returns the effective value of key and the file it came from
// (e.g. "/Users/k/.gitconfig.d/gitident/work.gitconfig"). For non-file origins
// (command line, blobs) the origin is returned with its type prefix.
func ConfigOrigin(dir, key string) (value, origin string, ok bool, err error) {
	out, err := Run(dir, "config", "--show-origin", "-z", "--get", key)
	if err != nil {
		if isUnset(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	parts := strings.SplitN(out, "\x00", 3)
	if len(parts) < 2 {
		return "", "", false, fmt.Errorf("unexpected git config output %q", out)
	}
	origin = parts[0]
	if strings.HasPrefix(origin, "file:") {
		origin = strings.TrimPrefix(origin, "file:")
		if !filepath.IsAbs(origin) {
			// Relative origins are relative to the top of the working tree.
			base := dir
			if top, err := TopLevel(dir); err == nil {
				base = top
			}
			origin = filepath.Join(base, origin)
		}
	}
	return parts[1], origin, true, nil
}

// Entry is one key/value from `git config --list`.
type Entry struct {
	Origin string // file path when listed with origins
	Key    string // as printed by git: section and key lower-cased, subsection verbatim
	Value  string
}

// ParseList parses NUL-separated `git config --list -z [--show-origin]` output.
func ParseList(out string, withOrigin bool) []Entry {
	var entries []Entry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		var e Entry
		if withOrigin {
			if fields[i] == "" || i+1 >= len(fields) {
				continue
			}
			e.Origin = strings.TrimPrefix(fields[i], "file:")
			i++
		}
		kv := fields[i]
		if kv == "" {
			continue
		}
		if nl := strings.IndexByte(kv, '\n'); nl >= 0 {
			e.Key, e.Value = kv[:nl], kv[nl+1:]
		} else {
			e.Key = kv // boolean shorthand: key without "= value"
			e.Value = "true"
		}
		entries = append(entries, e)
	}
	return entries
}

// ListFile lists the entries of one config file, without following includes.
func ListFile(file string) ([]Entry, error) {
	out, err := Run("", "config", "--file", file, "--no-includes", "--list", "-z")
	if err != nil {
		return nil, err
	}
	return ParseList(out, false), nil
}

// ListLocal lists the repository-local entries (.git/config) of the repo at dir.
func ListLocal(dir string) ([]Entry, error) {
	out, err := Run(dir, "config", "--local", "--no-includes", "--list", "-z")
	if err != nil {
		if isUnset(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseList(out, false), nil
}

// FileGetAll returns all values of key in a single config file (no includes).
func FileGetAll(file, key string) ([]string, error) {
	out, err := Run("", "config", "--file", file, "--no-includes", "-z", "--get-all", key)
	if err != nil {
		if isUnset(err) {
			return nil, nil
		}
		var ge *Error
		if errors.As(err, &ge) && ge.ExitCode == 1 {
			if _, statErr := os.Stat(file); errors.Is(statErr, os.ErrNotExist) {
				return nil, nil
			}
		}
		return nil, err
	}
	var vals []string
	for _, v := range strings.Split(out, "\x00") {
		if v != "" {
			vals = append(vals, v)
		}
	}
	return vals, nil
}

// FileAdd appends key = value to a config file.
func FileAdd(file, key, value string) error {
	_, err := Run("", "config", "--file", file, "--add", key, value)
	return err
}

// FileUnsetValue removes every key entry whose value equals value exactly.
func FileUnsetValue(file, key, value string) error {
	_, err := Run("", "config", "--file", file, "--unset-all", key, "^"+regexp.QuoteMeta(value)+"$")
	if err != nil && errorCode(err) == 5 {
		return nil // nothing to unset
	}
	return err
}

func errorCode(err error) int {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.ExitCode
	}
	return -1
}

// TopLevel returns the working tree root of the repository containing dir.
func TopLevel(dir string) (string, error) {
	out, err := Run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if errorCode(err) == 128 {
			return "", fmt.Errorf("%s: %w", dir, ErrNotRepo)
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// GitDir returns the absolute git directory of the repository at dir (for linked
// worktrees this is .git/worktrees/<name> inside the main repository).
func GitDir(dir string) (string, error) {
	out, err := Run(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		if errorCode(err) == 128 {
			return "", fmt.Errorf("%s: %w", dir, ErrNotRepo)
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommonDir returns the absolute common git directory, which holds the shared
// config file for all worktrees.
func CommonDir(dir string) (string, error) {
	out, err := Run(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		if errorCode(err) == 128 {
			return "", fmt.Errorf("%s: %w", dir, ErrNotRepo)
		}
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		base := dir
		if base == "" {
			base, _ = os.Getwd()
		}
		p = filepath.Join(base, p)
	}
	// Resolve symlinks so the result is comparable with GitDir, which git reports
	// fully resolved.
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p), nil
}

// Remote is a configured remote URL.
type Remote struct {
	Name string
	URL  string
}

// Remotes returns all remote.<name>.url values visible from dir, in config order.
func Remotes(dir string) ([]Remote, error) {
	out, err := Run(dir, "config", "-z", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		if isUnset(err) {
			return nil, nil
		}
		return nil, err
	}
	var remotes []Remote
	for _, e := range ParseList(out, false) {
		name := strings.TrimSuffix(strings.TrimPrefix(e.Key, "remote."), ".url")
		remotes = append(remotes, Remote{Name: name, URL: e.Value})
	}
	return remotes, nil
}

// Version is a parsed git version.
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// AtLeast reports whether v >= major.minor.
func (v Version) AtLeast(major, minor int) bool {
	return v.Major > major || (v.Major == major && v.Minor >= minor)
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion parses `git --version` output such as "git version 2.39.3 (Apple Git-146)".
func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("cannot parse git version from %q", strings.TrimSpace(s))
	}
	v := Version{Raw: strings.TrimSpace(s)}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	return v, nil
}

// GitVersion returns the installed git version.
func GitVersion() (Version, error) {
	out, err := Run("", "--version")
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(out)
}

// HasConfigMinVersion is the first git release supporting includeIf "hasconfig:remote.*.url:".
var HasConfigMinVersion = Version{Major: 2, Minor: 36}
