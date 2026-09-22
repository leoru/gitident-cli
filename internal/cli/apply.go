package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/inspect"
	"github.com/leoru/gitident-cli/internal/materialize"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/render"
)

const applyUsage = `usage: gitident apply [dir...] [--all] [--force] [--dry-run]

Copy the profile a repository gets (its pin, else rules and repos lists) into
the repository's own .git/config: user.name, user.email, the signing settings,
core.sshCommand and extra keys. Git reads .git/config after the global config,
so the copy also works where the includeIf rules are not evaluated: dev
containers that only mount the repository, or clients without hasconfig
support. dir defaults to the current directory.

gitident records what it wrote in a [gitident] section of .git/config, and
"gitident sync" keeps every copy current when profiles.yaml changes (and
removes it when no profile applies any more). Values changed by hand, and
local settings gitident did not write, are never overwritten without --force.

Set ` + "`materialize: true`" + ` in profiles.yaml, at the top level or per profile,
to have sync do this for every matching repository automatically.

flags:
  --all      every repository under the default roots that a profile applies to
  --force    overwrite values changed by hand or set by something else
  --dry-run  show what would change, write nothing
`

const unapplyUsage = `usage: gitident unapply [dir...] [--all] [--force] [--dry-run]

Remove the values "gitident apply" copied into a repository's .git/config; the
repository goes back to getting its identity from the includeIf rules. If the
repository's profile has ` + "`materialize: true`" + `, the next sync copies them
back. dir defaults to the current directory.

flags:
  --all      every repository gitident has copied a profile into
  --force    also remove values that were changed by hand since
  --dry-run  show what would change, write nothing
`

type matMode int

const (
	modeApply  matMode = iota // write the target profile
	modeRemove                // remove the copy
	modeSync                  // write where wanted or already present, remove where no profile applies
)

// matOutcome is the result of materializing one repository.
type matOutcome struct {
	repo    string // display path
	local   string // the config file
	profile string // profile written or kept ("" for none)
	skip    string // why nothing was attempted
	res     materialize.Result
	err     error
}

// holds reports whether the repository still has a materialized copy afterwards.
func (o matOutcome) holds(dryRun bool) bool {
	was := o.res.Record != nil && o.res.Record.Materialized()
	switch o.res.Action {
	case materialize.Written:
		return true
	case materialize.Removed:
		return dryRun
	default:
		return was
	}
}

func (a *App) cmdApply(args []string) error {
	return a.applyCommand("apply", applyUsage, modeApply, args)
}
func (a *App) cmdUnapply(args []string) error {
	return a.applyCommand("unapply", unapplyUsage, modeRemove, args)
}

func (a *App) applyCommand(name, usage string, mode matMode, args []string) error {
	fs := a.newFlags(name, usage)
	all := fs.Bool("all", false, "")
	force := fs.Bool("force", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	dirs, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if *all && len(dirs) > 0 {
		return usageError("--all takes no directories")
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	opts := materialize.Options{Force: *force, DryRun: *dryRun}

	var targets []*inspect.Result
	failed := 0
	if *all {
		repos, _, err := discover(cfg, nil)
		if err != nil {
			return err
		}
		if mode == modeRemove {
			reg, err := materialize.LoadRegistry()
			if err != nil {
				return err
			}
			repos = append(repos, registryRepos(reg)...)
		}
		repos = dedupeRepos(repos)
		for i, r := range inspectAll(cfg, repos) {
			if r.err != nil {
				a.warn("%s: %v", paths.Contract(repos[i]), r.err)
				failed++
				continue
			}
			if mode == modeRemove && r.res.Materialized == "" {
				continue
			}
			if mode == modeApply && !hasProfile(cfg, r.res) {
				continue
			}
			targets = append(targets, r.res)
		}
	} else {
		if len(dirs) == 0 {
			dirs = []string{"."}
		}
		for _, d := range dirs {
			r, err := inspect.Inspect(cfg, d)
			if err != nil {
				return err
			}
			if r.Repo == "" || r.CommonDir == "" {
				return fmt.Errorf("%s is not inside a git repository", paths.Contract(r.Dir))
			}
			targets = append(targets, r)
		}
	}

	outs := materializeAll(cfg, targets, mode, opts)
	written, unchanged := 0, 0
	for _, o := range outs {
		switch {
		case o.err != nil || o.skip != "" || o.res.Action == materialize.Edited || o.res.Action == materialize.Conflict:
			failed++
		case o.res.Action == materialize.Unchanged:
			unchanged++
			if !*all {
				if mode == modeRemove {
					a.printf("%s has no copied profile\n", o.repo)
				} else {
					a.printf("%s already has profile %q in %s\n", o.repo, o.profile, paths.Contract(o.local))
				}
			}
			continue
		default:
			written++
		}
		a.reportOutcome(o, opts.DryRun)
	}
	if err := a.updateRegistry(outs, opts.DryRun); err != nil {
		return err
	}
	if *all {
		verb := "changed"
		if opts.DryRun {
			verb = "would change"
		}
		a.printf("%s %s, %d unchanged", verb, plural(written, "repository"), unchanged)
		if failed > 0 {
			a.printf(", %d skipped", failed)
		}
		a.printf("\n")
	}
	if failed > 0 {
		return errProblems
	}
	return nil
}

func hasProfile(cfg *config.Config, r *inspect.Result) bool {
	_, ok := cfg.Profiles[r.TargetProfile()]
	return ok
}

// materializeAll applies mode to every repository, in parallel.
func materializeAll(cfg *config.Config, targets []*inspect.Result, mode matMode, opts materialize.Options) []matOutcome {
	// Worktrees share one config file; write it once.
	seen := map[string]bool{}
	unique := targets[:0:0]
	for _, r := range targets {
		if k := paths.Real(r.LocalConfig()); !seen[k] {
			seen[k] = true
			unique = append(unique, r)
		}
	}
	targets = unique
	outs := make([]matOutcome, len(targets))
	forEach(len(targets), func(i int) { outs[i] = materializeOne(cfg, targets[i], mode, opts) })
	sort.SliceStable(outs, func(i, j int) bool { return outs[i].repo < outs[j].repo })
	return outs
}

func materializeOne(cfg *config.Config, r *inspect.Result, mode matMode, opts materialize.Options) matOutcome {
	o := matOutcome{repo: paths.Contract(r.Repo), local: r.LocalConfig()}
	if r.Repo == "" {
		o.repo = paths.Contract(r.CommonDir)
	}
	target := r.TargetProfile()
	p := cfg.Profiles[target]
	switch {
	case mode == modeRemove,
		mode == modeSync && p == nil && r.Materialized != "":
		o.res, o.err = materialize.Remove(o.local, opts)
	case p == nil && mode == modeApply:
		o.skip = "no profile applies here — add a rule or repos entry, or pin it with `gitident use <profile>` first"
		if target != "" {
			o.skip = fmt.Sprintf("pinned to profile %q, which does not exist", target)
		}
	case p == nil,
		mode == modeSync && !cfg.Materializes(target) && r.Materialized == "":
		// sync: nothing to do
	default:
		o.profile = target
		o.res, o.err = materialize.Apply(o.local, target, render.Settings(p), opts)
	}
	return o
}

func (a *App) reportOutcome(o matOutcome, dryRun bool) {
	would := ""
	if dryRun {
		would = "would "
	}
	where := paths.Contract(o.local)
	switch {
	case o.err != nil:
		fmt.Fprintf(a.Stderr, "error: %s: %v\n", o.repo, o.err)
	case o.skip != "":
		a.warn("%s: %s", o.repo, o.skip)
	case o.res.Action == materialize.Written && o.res.Previous == "":
		a.printf("%scopy profile %q into %s\n", would, o.profile, where)
	case o.res.Action == materialize.Written && o.res.Previous != o.profile:
		a.printf("%sswitch %s from profile %q to %q\n", would, where, o.res.Previous, o.profile)
	case o.res.Action == materialize.Written:
		a.printf("%supdate profile %q in %s\n", would, o.profile, where)
	case o.res.Action == materialize.Removed:
		a.printf("%sremove profile %q from %s\n", would, o.res.Previous, where)
	case o.res.Action == materialize.Edited:
		detail := ""
		if len(o.res.Details) > 0 {
			detail = " (" + strings.Join(o.res.Details, "; ") + ")"
		}
		a.warn("%s: values gitident copied into %s were changed by hand%s; left alone — `gitident apply --force` restores the profile, `gitident unapply --force` removes them",
			o.repo, where, detail)
	case o.res.Action == materialize.Conflict:
		a.warn("%s: %s already sets %s; left alone — remove those settings, or rerun with --force to overwrite them",
			o.repo, where, strings.Join(o.res.Details, ", "))
	}
}

// updateRegistry records which config files hold a copy after outs.
func (a *App) updateRegistry(outs []matOutcome, dryRun bool) error {
	if dryRun {
		return nil
	}
	reg, err := materialize.LoadRegistry()
	if err != nil {
		return err
	}
	touched := map[string]bool{}
	var keep []string
	for _, o := range outs {
		if o.local == "" {
			continue
		}
		touched[paths.Real(o.local)] = true
		if o.holds(false) {
			keep = append(keep, o.local)
		}
	}
	for _, f := range reg {
		if !touched[paths.Real(f)] {
			if _, err := os.Stat(f); err == nil {
				keep = append(keep, f)
			}
		}
	}
	return materialize.SaveRegistry(keep)
}

// syncMaterialized keeps materialized copies current: registered repositories
// always, and every matching repository of profiles with materialize on.
func (a *App) syncMaterialized(cfg *config.Config, dryRun bool) (changed, held int, err error) {
	reg, err := materialize.LoadRegistry()
	if err != nil {
		return 0, 0, err
	}
	if !cfg.MaterializesAny() && len(reg) == 0 {
		return 0, 0, nil
	}
	repos := registryRepos(reg)
	for _, f := range reg {
		if _, err := os.Stat(f); err != nil {
			changed++
			if dryRun {
				a.printf("would forget %s (no longer exists)\n", paths.Contract(f))
			} else {
				a.printf("forgot %s (no longer exists)\n", paths.Contract(f))
			}
		}
	}
	if cfg.MaterializesAny() {
		found, _, err := discover(cfg, nil)
		if err != nil {
			return 0, 0, err
		}
		repos = append(repos, found...)
	}
	repos = dedupeRepos(repos)
	var targets []*inspect.Result
	for i, r := range inspectAll(cfg, repos) {
		if r.err != nil {
			a.warn("%s: %v", paths.Contract(repos[i]), r.err)
			continue
		}
		if r.res.CommonDir != "" {
			targets = append(targets, r.res)
		}
	}
	outs := materializeAll(cfg, targets, modeSync, materialize.Options{DryRun: dryRun})
	for _, o := range outs {
		if o.holds(dryRun) {
			held++
		}
		if o.err == nil && o.skip == "" && o.res.Action == materialize.Unchanged {
			continue
		}
		if o.res.Action == materialize.Written || o.res.Action == materialize.Removed {
			changed++
		}
		a.reportOutcome(o, dryRun)
	}
	return changed, held, a.updateRegistry(outs, dryRun)
}

// registryRepos turns registered config files into repository directories.
func registryRepos(files []string) []string {
	var out []string
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		dir := filepath.Dir(f)
		if filepath.Base(dir) == ".git" {
			dir = filepath.Dir(dir)
		}
		out = append(out, dir)
	}
	return out
}

func dedupeRepos(repos []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range repos {
		k := paths.Real(r)
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// forEach runs fn for 0..n-1 on a bounded worker pool.
func forEach(n int, fn func(i int)) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}
