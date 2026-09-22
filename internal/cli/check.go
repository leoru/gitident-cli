package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/inspect"
	"github.com/leoru/gitident-cli/internal/match"
	"github.com/leoru/gitident-cli/internal/materialize"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/render"
	"github.com/leoru/gitident-cli/internal/scan"
)

const checkUsage = `usage: gitident check [roots...] [--json] [-q]

Scan for repositories and report, for each one:
  ok <profile>     identity matches profiles.yaml
  NO IDENTITY      no user.email at all (commits fail in strict mode)
  UNKNOWN EMAIL    user.email is not in any profile
  MISMATCH         effective profile differs from what rules / repos / pin say
  STALE            the profile copy in .git/config (gitident apply) is out of
                   date, changed by hand, or missing although materialize is on
  MISSING          a path listed in ` + "`repos`" + ` does not exist or is not a repository
  NOT CLONED       a URL listed in ` + "`repos`" + ` has no clone under the roots (info)

Roots default to the dirs of all rules plus the parent directories of listed
repos. Exits 1 when any problem (anything but ok / NOT CLONED) is found.

flags:
  --json   machine-readable output
  -q       only print problems
`

// Check statuses.
const (
	statusOK        = "ok"
	statusNoIdent   = "NO IDENTITY"
	statusUnknown   = "UNKNOWN EMAIL"
	statusMismatch  = "MISMATCH"
	statusStale     = "STALE"
	statusMissing   = "MISSING"
	statusNotCloned = "NOT CLONED"
)

type checkItem struct {
	Status   string `json:"status"`
	Path     string `json:"path"`
	Profile  string `json:"profile,omitempty"`
	Expected string `json:"expected,omitempty"`
	Email    string `json:"email,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Problem  bool   `json:"problem"`
}

func (a *App) cmdCheck(args []string) error {
	fs := a.newFlags("check", checkUsage)
	asJSON := fs.Bool("json", false, "")
	quiet := fs.Bool("q", false, "")
	roots, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	items, err := runCheck(cfg, roots)
	if err != nil {
		return err
	}

	problems := 0
	for _, it := range items {
		if it.Problem {
			problems++
		}
	}
	if *asJSON {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		if items == nil {
			items = []checkItem{}
		}
		if err := enc.Encode(items); err != nil {
			return err
		}
	} else {
		a.printCheck(items, *quiet)
	}
	if problems > 0 {
		return errProblems
	}
	return nil
}

// runCheck scans roots (or the default roots) and classifies every repository.
func runCheck(cfg *config.Config, roots []string) ([]checkItem, error) {
	repos, items, err := discover(cfg, roots)
	if err != nil {
		return nil, err
	}

	results := inspectAll(cfg, repos)
	remotes := map[string]bool{}
	for i, r := range results {
		if r.err != nil {
			items = append(items, checkItem{Status: "ERROR", Path: paths.Contract(repos[i]), Detail: r.err.Error(), Problem: true})
			continue
		}
		for _, rm := range r.res.Remotes {
			remotes[paths.NormalizeURL(rm.URL)] = true
		}
		it := classify(cfg, r.res)
		// A stale copy explains an unknown or mismatched email better than
		// those statuses do.
		if problem := copyProblem(cfg, r.res); problem != "" && (it.Status == statusOK || r.res.Materialized != "") {
			it.Status, it.Problem, it.Detail = statusStale, true, problem
		}
		it.Path = paths.Contract(repos[i])
		items = append(items, it)
	}

	for _, name := range cfg.ProfileNames() {
		for _, repo := range cfg.Profiles[name].Repos {
			if paths.IsURL(repo) && !remotes[paths.NormalizeURL(repo)] {
				items = append(items, checkItem{Status: statusNotCloned, Path: repo, Profile: name,
					Detail: fmt.Sprintf("listed in profile %q; no clone under the scanned roots", name)})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items, nil
}

// discover returns the repositories under roots (DefaultRoots when empty) plus
// every listed repo path, and MISSING items for listed paths that are not
// repositories.
func discover(cfg *config.Config, roots []string) ([]string, []checkItem, error) {
	if len(roots) == 0 {
		roots = DefaultRoots(cfg)
	}
	repos, err := scan.Find(roots, scan.Options{Ignore: cfg.ScanIgnore()})
	if err != nil {
		return nil, nil, err
	}
	var missing []checkItem
	seen := map[string]bool{}
	for _, r := range repos {
		seen[paths.Real(r)] = true
	}
	// Listed repo paths are always included, even outside the roots.
	for _, name := range cfg.ProfileNames() {
		for _, repo := range cfg.Profiles[name].Repos {
			if paths.IsURL(repo) {
				continue
			}
			abs := paths.Abs(repo)
			if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
				missing = append(missing, checkItem{Status: statusMissing, Path: paths.Contract(abs), Profile: name,
					Detail: fmt.Sprintf("listed in profile %q but not a git repository", name), Problem: true})
				continue
			}
			if !seen[paths.Real(abs)] {
				seen[paths.Real(abs)] = true
				repos = append(repos, abs)
			}
		}
	}
	return repos, missing, nil
}

type inspected struct {
	res *inspect.Result
	err error
}

func inspectAll(cfg *config.Config, repos []string) []inspected {
	out := make([]inspected, len(repos))
	forEach(len(repos), func(i int) {
		res, err := inspect.Inspect(cfg, repos[i])
		out[i] = inspected{res, err}
	})
	return out
}

// classify decides the status of one repository.
func classify(cfg *config.Config, r *inspect.Result) checkItem {
	it := checkItem{Email: r.Email()}
	expected, reason := r.Expected.Profile, r.Expected.Reason
	if r.Pinned != "" {
		expected, reason = r.Pinned, "pinned in .git/config"
		if _, ok := cfg.Profiles[r.Pinned]; !ok {
			it.Status, it.Problem = statusMismatch, true
			it.Detail = fmt.Sprintf("pinned to profile %q, which no longer exists — run `gitident unuse` or `gitident use <profile>` there", r.Pinned)
			return it
		}
	}
	it.Expected = expected
	profile := r.Profile
	// Several profiles may share an email; if the expected one does, it is fine.
	if r.Via == inspect.ViaEmail && expected != "" && strings.EqualFold(cfg.Profiles[expected].Email, r.Email()) {
		profile = expected
	}
	it.Profile = profile

	switch {
	case r.Email() == "":
		it.Status, it.Problem = statusNoIdent, true
		if expected != "" {
			it.Detail = fmt.Sprintf("profiles.yaml says %q (%s) but it is not applied — run `gitident sync`", expected, reason)
		} else {
			it.Detail = "no rule, repos entry or pin matches"
		}
	case profile == "":
		it.Status, it.Problem = statusUnknown, true
		it.Detail = fmt.Sprintf("%s (set in %s)", r.Email(), paths.Contract(r.Origins["user.email"]))
	case expected != "" && profile != expected:
		it.Status, it.Problem = statusMismatch, true
		it.Detail = fmt.Sprintf("effective %q (from %s), expected %q (%s)", profile, paths.Contract(r.Origins["user.email"]), expected, reason)
	default:
		it.Status = statusOK
		var notes []string
		if r.Pinned != "" {
			notes = append(notes, "pinned")
		}
		if r.Via == inspect.ViaMaterialized {
			notes = append(notes, "copied into .git/config")
		}
		if r.OutsideGitident() {
			notes = append(notes, "set outside gitident: "+paths.Contract(r.Origins["user.email"]))
		}
		if expected == "" {
			notes = append(notes, "no rule matches")
		}
		if len(notes) > 0 {
			it.Detail = "(" + strings.Join(notes, "; ") + ")"
		}
	}
	return it
}

func (a *App) printCheck(items []checkItem, quiet bool) {
	counts := map[string]int{}
	width := 0
	for _, it := range items {
		if len(it.Path) > width {
			width = len(it.Path)
		}
	}
	if width > 60 {
		width = 60
	}
	for _, it := range items {
		counts[it.Status]++
		if quiet && !it.Problem {
			continue
		}
		detail := it.Detail
		if it.Status == statusOK {
			detail = strings.TrimSpace(it.Profile + " " + it.Detail)
		}
		a.printf("%-13s %-*s  %s\n", it.Status, width, it.Path, detail)
	}
	if len(items) == 0 {
		a.printf("no repositories found (pass roots explicitly, or add `dirs` rules)\n")
		return
	}
	var parts []string
	for _, s := range []string{statusOK, statusNoIdent, statusUnknown, statusMismatch, statusStale, statusMissing, "ERROR", statusNotCloned} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], strings.ToLower(s)))
		}
	}
	a.printf("\n%s: %s\n", plural(len(items), "entry"), strings.Join(parts, ", "))
	if counts[statusNoIdent]+counts[statusUnknown] > 0 {
		a.printf("hint: add the repo to a profile's `repos` (or a `dirs`/`remotes` rule) and run `gitident sync`, or run `gitident use <profile>` inside it\n")
	}
	if counts[statusMismatch] > 0 {
		a.printf("hint: run `gitident sync`; if it persists, look for user.* set in the file shown (often .git/config) and remove it\n")
	}
	if counts[statusStale] > 0 {
		a.printf("hint: run `gitident sync`; copies changed by hand need `gitident apply --force` (restore) or `gitident unapply --force` (remove)\n")
	}
	if counts[statusMissing] > 0 {
		a.printf("hint: remove stale `repos` entries from profiles.yaml, or clone the repositories there\n")
	}
}

// copyProblem describes what is wrong with a repository's materialized copy
// ("" when it is current, or when there is none and none is wanted).
func copyProblem(cfg *config.Config, r *inspect.Result) string {
	if r.Materialized == "" {
		if t := r.TargetProfile(); cfg.Profiles[t] != nil && cfg.Materializes(t) {
			return fmt.Sprintf("profile %q has materialize on but .git/config has no copy yet — run `gitident sync`", t)
		}
		return ""
	}
	rec, err := materialize.Read(r.LocalConfig())
	if err != nil {
		return err.Error()
	}
	p := cfg.Profiles[rec.Profile]
	switch {
	case rec.Edited():
		return fmt.Sprintf("values copied from profile %q were changed by hand in .git/config", rec.Profile)
	case p == nil:
		return fmt.Sprintf(".git/config holds a copy of profile %q, which no longer exists — run `gitident sync`", rec.Profile)
	case !rec.UpToDate(rec.Profile, render.Settings(p)):
		return fmt.Sprintf("the copy of profile %q in .git/config is out of date — run `gitident sync`", rec.Profile)
	}
	return ""
}

// DefaultRoots returns the directories check and import scan by default: every
// rule dir (up to its first glob component) and the parent of every listed repo path.
func DefaultRoots(cfg *config.Config) []string {
	var roots []string
	seen := map[string]bool{}
	add := func(p string) {
		p = paths.Abs(p)
		if !seen[p] {
			seen[p] = true
			roots = append(roots, p)
		}
	}
	for _, r := range cfg.Rules {
		for _, d := range r.Dirs {
			add(globBase(d))
		}
	}
	for _, name := range cfg.ProfileNames() {
		for _, repo := range cfg.Profiles[name].Repos {
			if !paths.IsURL(repo) {
				add(filepath.Dir(paths.Abs(repo)))
			}
		}
	}
	return roots
}

// globBase returns the longest leading path of p without glob characters.
func globBase(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if match.HasGlob(part) {
			if i == 0 {
				return "/"
			}
			return strings.Join(parts[:i], "/")
		}
	}
	return p
}
