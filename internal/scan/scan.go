// Package scan discovers git repositories below a set of root directories.
package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leoru/gitident-cli/internal/match"
	"github.com/leoru/gitident-cli/internal/paths"
)

// DefaultIgnore lists directory names that never contain repositories worth checking.
var DefaultIgnore = []string{"node_modules", ".build", "DerivedData", "Pods", "Carthage"}

// Options control a scan.
type Options struct {
	// Ignore holds extra patterns: a bare name or glob matches a directory's base
	// name; a pattern containing "/" (or starting with "~") matches its full path.
	Ignore []string
}

// Find walks roots and returns the working-tree roots of all repositories found,
// sorted and de-duplicated. A directory containing ".git" (a git directory with a
// HEAD, or a file for linked worktrees and submodules) is a repository and is not
// descended into.
// Hidden directories are skipped (except the roots themselves) and symlinks are
// not followed. Missing roots are ignored.
func Find(roots []string, opts Options) ([]string, error) {
	seen := map[string]bool{}
	var repos []string
	for _, root := range roots {
		root = paths.Abs(root)
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			continue
		}
		// Walk the resolved root so a symlinked root is followed, but report paths
		// under the root as given.
		real := paths.Real(root)
		err = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == real {
					return err
				}
				return filepath.SkipDir // unreadable directory
			}
			if !d.IsDir() {
				return nil
			}
			shown := root
			if p != real {
				shown = filepath.Join(root, strings.TrimPrefix(p, real+string(filepath.Separator)))
			}
			if p != real && ignored(shown, d.Name(), opts.Ignore) {
				return filepath.SkipDir
			}
			if isRepo(p) {
				key := paths.Real(p)
				if !seen[key] {
					seen[key] = true
					repos = append(repos, shown)
				}
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(repos)
	return repos, nil
}

// isRepo reports whether dir has a ".git" file (worktree, submodule) or a
// ".git" directory that looks like a real git dir; an empty or broken ".git"
// directory is not a repository to git either.
func isRepo(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	if !fi.IsDir() {
		return true
	}
	_, err = os.Stat(filepath.Join(dir, ".git", "HEAD"))
	return err == nil
}

func ignored(path, name string, extra []string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, n := range DefaultIgnore {
		if name == n {
			return true
		}
	}
	for _, pat := range extra {
		if strings.Contains(pat, "/") || strings.HasPrefix(pat, "~") {
			pat = strings.TrimRight(paths.Expand(pat), "/")
			if match.Glob(pat, path) || match.Glob(pat, paths.Real(path)) {
				return true
			}
			continue
		}
		if match.Glob(pat, name) {
			return true
		}
	}
	return false
}
