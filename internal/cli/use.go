package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/inspect"
	"github.com/leoru/gitident-cli/internal/paths"
)

const useUsage = `usage: gitident use <profile> [dir] [--save]

Pin the repository containing dir (default: the current directory) to a
profile by adding include.path = <fragment> to its .git/config. The fragment is
included rather than copied, so later edits to the profile still apply. Any
previous gitident pin is replaced. In a linked worktree the pin goes into the
shared repository config.

flags:
  --save   also add the repository to the profile's ` + "`repos`" + ` in profiles.yaml
           (removing it from other profiles; comments are preserved) and sync
`

const unuseUsage = `usage: gitident unuse [dir]

Remove the gitident pin from the repository containing dir (default: the
current directory). The repository falls back to what rules and repos lists say.
`

func (a *App) cmdUse(args []string) error {
	fs := a.newFlags("use", useUsage)
	save := fs.Bool("save", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usageError("expected a profile name and optionally a directory")
	}
	profile, dir := pos[0], "."
	if len(pos) == 2 {
		dir = pos[1]
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Profiles[profile]; !ok {
		return fmt.Errorf("unknown profile %q (have: %s)", profile, strings.Join(cfg.ProfileNames(), ", "))
	}
	r, err := inspect.Inspect(cfg, dir)
	if err != nil {
		return err
	}
	if r.Repo == "" || r.CommonDir == "" {
		return fmt.Errorf("%s is not inside a git repository", paths.Contract(r.Dir))
	}
	local := filepath.Join(r.CommonDir, "config")
	if _, err := removePins(local); err != nil {
		return err
	}
	if err := gitx.FileAdd(local, "include.path", paths.FragmentPathTilde(profile)); err != nil {
		return err
	}
	a.printf("pinned %s to profile %q (include.path in %s)\n", paths.Contract(r.Repo), profile, paths.Contract(local))

	if *save {
		entry, note := repoEntry(r)
		if entry == "" {
			a.warn("%s", note)
		} else {
			if note != "" {
				a.printf("%s\n", note)
			}
			newCfg, err := a.saveRepo(profile, entry)
			if err != nil {
				return err
			}
			if err := a.sync(newCfg, syncOptions{prune: true}); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(paths.FragmentPath(profile)); err != nil {
		a.warn("%s does not exist yet — run `gitident sync`", paths.Contract(paths.FragmentPath(profile)))
	}
	return nil
}

// repoEntry picks the repos entry that identifies r in profiles.yaml: the main
// working tree path, or the origin URL when the git dir is not <path>/.git
// (submodules, separate git dirs), since gitdir: rules could not match a path.
func repoEntry(r *inspect.Result) (entry, note string) {
	if filepath.Base(r.CommonDir) == ".git" {
		return paths.Contract(filepath.Dir(r.CommonDir)), ""
	}
	for _, rm := range r.Remotes {
		if rm.Name == "origin" {
			return rm.URL, fmt.Sprintf("git dir is %s, so saving the origin URL instead of the path", paths.Contract(r.CommonDir))
		}
	}
	if len(r.Remotes) > 0 {
		return r.Remotes[0].URL, fmt.Sprintf("git dir is %s, so saving the %s URL instead of the path", paths.Contract(r.CommonDir), r.Remotes[0].Name)
	}
	return "", "cannot --save: the git dir is not <repo>/.git and there is no remote URL to list instead; the pin still works"
}

// saveRepo adds entry to profiles.<profile>.repos (removing it from other
// profiles), writes profiles.yaml preserving comments, and returns the new config.
func (a *App) saveRepo(profile, entry string) (*config.Config, error) {
	path := paths.ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := config.ParseDocument(data)
	if err != nil {
		return nil, err
	}
	for _, from := range doc.RemoveRepo(entry, profile) {
		a.printf("removed %s from profile %q\n", entry, from)
	}
	added, err := doc.AddRepo(profile, entry)
	if err != nil {
		return nil, err
	}
	if !added {
		a.printf("%s is already listed in profile %q\n", entry, profile)
	}
	out, err := doc.Bytes()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Parse(out)
	if err != nil {
		return nil, err
	}
	if rep := cfg.Validate(); !rep.OK() {
		return nil, errors.New("refusing to save: " + strings.Join(rep.Errors, "; "))
	}
	if err := fsutil.WriteFileAtomic(path, out, 0o644); err != nil {
		return nil, err
	}
	if added {
		a.printf("added %s to profile %q in %s\n", entry, profile, paths.Contract(path))
	}
	return cfg, nil
}

// removePins deletes every include.path pointing at a gitident fragment from a
// repository config file and returns the profiles that were pinned.
func removePins(local string) ([]string, error) {
	vals, err := gitx.FileGetAll(local, "include.path")
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, v := range vals {
		p, ok := paths.ProfileFromFragment(v)
		if !ok {
			continue
		}
		if err := gitx.FileUnsetValue(local, "include.path", v); err != nil {
			return removed, err
		}
		removed = append(removed, p)
	}
	return removed, nil
}

func (a *App) cmdUnuse(args []string) error {
	fs := a.newFlags("unuse", unuseUsage)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError("expected at most one directory")
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	cfg, err := config.Load(paths.ConfigPath())
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}
	r, err := inspect.Inspect(cfg, dir)
	if err != nil {
		return err
	}
	if r.Repo == "" || r.CommonDir == "" {
		return fmt.Errorf("%s is not inside a git repository", paths.Contract(r.Dir))
	}
	local := filepath.Join(r.CommonDir, "config")
	removed, err := removePins(local)
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		a.printf("%s is not pinned\n", paths.Contract(r.Repo))
	} else {
		a.printf("unpinned %s (was %s)\n", paths.Contract(r.Repo), strings.Join(removed, ", "))
	}
	if cfg != nil {
		if owner, entry := listedIn(cfg, r); owner != "" {
			a.warn("%s is still listed as %s in profile %q's repos; that entry keeps applying profile %q (remove it from profiles.yaml and run `gitident sync` to change that)",
				paths.Contract(r.Repo), entry, owner, owner)
		}
		if exp := describeExpected(cfg, r); exp != "" {
			a.printf("now: %s\n", exp)
		}
	}
	return nil
}

// listedIn reports which profile's repos list names this repository.
func listedIn(cfg *config.Config, r *inspect.Result) (profile, entry string) {
	keys := map[string]bool{}
	if filepath.Base(r.CommonDir) == ".git" {
		keys[config.RepoKey(filepath.Dir(r.CommonDir))] = true
	}
	keys[config.RepoKey(r.Repo)] = true
	for _, rm := range r.Remotes {
		keys[config.RepoKey(rm.URL)] = true
	}
	for _, name := range cfg.ProfileNames() {
		for _, repo := range cfg.Profiles[name].Repos {
			if keys[config.RepoKey(repo)] {
				return name, repo
			}
		}
	}
	return "", ""
}

func describeExpected(cfg *config.Config, r *inspect.Result) string {
	if r.Expected.Profile == "" {
		if cfg.Strict() {
			return "no profile matches — commits will fail until you add a rule, a repos entry or a pin"
		}
		return "no profile matches"
	}
	return fmt.Sprintf("profile %q (%s)", r.Expected.Profile, r.Expected.Reason)
}
