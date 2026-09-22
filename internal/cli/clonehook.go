package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/inspect"
	"github.com/leoru/gitident-cli/internal/materialize"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/render"
)

// clonePlan is where the clone hook goes.
type clonePlan struct {
	enabled bool
	// ownTemplate: gitident's own template dir, selected via the _clone
	// fragment. False when you already set init.templateDir yourself; the
	// hook then goes into your template dir instead.
	ownTemplate bool
	hook        string // absolute path of the hook file ("" when disabled)
	foreignDir  string // your init.templateDir, if any
}

func planClone(cfg *config.Config) clonePlan {
	var p clonePlan
	if vals, _ := gitx.FileGetAll(paths.GlobalGitConfig(), "init.templateDir"); len(vals) > 0 {
		p.foreignDir = paths.Expand(vals[len(vals)-1])
	}
	if !cfg.CloneHookEnabled() {
		return p
	}
	p.enabled = true
	if p.foreignDir != "" {
		p.hook = filepath.Join(p.foreignDir, "hooks", paths.HookName)
	} else {
		p.ownTemplate = true
		p.hook = ownHookPath()
	}
	return p
}

func ownHookPath() string {
	return filepath.Join(paths.Expand(paths.TemplateDirTilde), "hooks", paths.HookName)
}

// blockFor renders the managed block for cfg.
func blockFor(cfg *config.Config) []byte {
	return render.Block(cfg, planClone(cfg).ownTemplate)
}

// isOurHook reports whether path is a hook file gitident generated.
func isOurHook(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, fragmentHeader)
}

// syncCloneHook installs, updates or removes the hook file. It returns the
// number of changes made.
func (a *App) syncCloneHook(cfg *config.Config, dryRun bool) (int, error) {
	plan := planClone(cfg)
	verb := func(v string) string {
		if dryRun {
			return "would " + v
		}
		return v
	}
	changed := 0
	// Remove hooks we no longer want (disabled, or moved between template dirs).
	candidates := []string{ownHookPath()}
	if plan.foreignDir != "" {
		candidates = append(candidates, filepath.Join(plan.foreignDir, "hooks", paths.HookName))
	}
	for _, h := range candidates {
		if h == plan.hook || !isOurHook(h) {
			continue
		}
		changed++
		a.printf("%s %s\n", verb("remove"), paths.Contract(h))
		if !dryRun {
			if err := os.Remove(h); err != nil {
				return changed, err
			}
			removeEmptyTemplateDirs()
		}
	}
	if !plan.enabled {
		return changed, nil
	}
	old, err := fsutil.ReadFileIfExists(plan.hook)
	if err != nil {
		return changed, err
	}
	switch {
	case old != nil && !bytes.Contains(old, fragmentHeader):
		a.warn("clone_hook: %s already exists and was not written by gitident; not replacing it — call `gitident __on-clone` from it (see `gitident help sync`)",
			paths.Contract(plan.hook))
	case string(old) != render.HookScript:
		changed++
		a.printf("%s %s\n", verb("write"), paths.Contract(plan.hook))
		if !dryRun {
			if err := fsutil.WriteFileAtomic(plan.hook, []byte(render.HookScript), 0o755); err != nil {
				return changed, err
			}
			if err := os.Chmod(plan.hook, 0o755); err != nil {
				return changed, err
			}
		}
	}
	for _, w := range cloneHookWarnings(plan) {
		a.warn("%s", w)
	}
	return changed, nil
}

func cloneHookWarnings(plan clonePlan) []string {
	var warns []string
	if !plan.enabled {
		return nil
	}
	if !plan.ownTemplate {
		warns = append(warns, fmt.Sprintf("clone_hook: init.templateDir is already set to %s, so the hook goes there", paths.Contract(plan.foreignDir)))
	}
	if v, ok, _ := gitx.Config("", "core.hooksPath"); ok && v != "" {
		warns = append(warns, fmt.Sprintf("clone_hook: core.hooksPath is set globally (%s), so git ignores hooks in .git/hooks and the clone hook never runs", v))
	}
	return warns
}

// removeEmptyTemplateDirs deletes gitident's template dir once it is empty.
func removeEmptyTemplateDirs() {
	dir := paths.Expand(paths.TemplateDirTilde)
	_ = os.Remove(filepath.Join(dir, "hooks"))
	_ = os.Remove(dir)
}

// cmdOnClone runs from the clone hook inside a freshly cloned repository. It
// prints one line about the identity the repository gets and never fails.
func (a *App) cmdOnClone(args []string) error {
	cfg, err := config.Load(paths.ConfigPath())
	if err != nil || !cfg.Validate().OK() {
		return nil
	}
	r, err := inspect.Inspect(cfg, ".")
	if err != nil || r.Repo == "" {
		return nil
	}
	target := r.TargetProfile()
	p := cfg.Profiles[target]
	if p == nil {
		msg := "gitident: no profile applies to this repository"
		if cfg.Strict() {
			msg += fmt.Sprintf(", so commits will fail; run `gitident use <profile>` here (profiles: %s)", strings.Join(cfg.ProfileNames(), ", "))
		}
		fmt.Fprintln(a.Stderr, msg)
		return nil
	}
	note := ""
	if cfg.Materializes(target) {
		outs := materializeAll(cfg, []*inspect.Result{r}, modeSync, materialize.Options{})
		for _, o := range outs {
			if o.err == nil && o.res.Action == materialize.Written {
				note = ", copied into .git/config"
			} else if o.res.Action != materialize.Unchanged || o.err != nil {
				a.reportOutcome(o, false)
			}
		}
		if err := a.updateRegistry(outs, false); err != nil && !errors.Is(err, os.ErrPermission) {
			a.warn("%v", err)
		}
	}
	fmt.Fprintf(a.Stderr, "gitident: profile %q (%s <%s>)%s\n", target, p.Name, p.Email, note)
	return nil
}
