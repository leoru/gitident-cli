// Package importer reverses the gitident pipeline: it reads existing git
// configuration (global config, includeIf fragments, per-repo local config and
// optionally GitKraken profiles) and proposes a profiles.yaml.
package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/inspect"
	"github.com/leoru/gitident-cli/internal/match"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/render"
	"github.com/leoru/gitident-cli/internal/scan"
)

// Options configure an import.
type Options struct {
	// GlobalFiles are the global gitconfig files to read, in git's reading order.
	GlobalFiles []string
	// Roots are scanned for repositories with a local identity. When empty, the
	// directories of imported gitdir rules are scanned.
	Roots  []string
	Ignore []string
	// GitKrakenDir is the GitKraken data directory (~/.gitkraken); "" skips it.
	GitKrakenDir string
	// Prompt, when set, is asked for every profile name (interactive mode).
	Prompt func(question, suggestion string) string
	// FoldThreshold is how many repos of one profile in one directory trigger a
	// `dirs` suggestion (default 3).
	FoldThreshold int
}

// Result is a proposed configuration plus diagnostics.
type Result struct {
	Config   *config.Config
	Order    []string            // profile names in discovery order
	Sources  map[string][]string // profile → where it came from
	Warnings []string
	Hints    []string
	Scanned  []string // roots that were scanned
}

// identity is the tuple that defines a profile.
type identity struct {
	Name, Email, SigningKey, GPGFormat, GPGSign, SSHCommand string
}

func (id identity) key() string {
	return strings.Join([]string{id.Name, strings.ToLower(id.Email), id.SigningKey, id.GPGFormat, id.GPGSign, id.SSHCommand}, "\x00")
}

// set records a git config entry if it is an identity key; it reports whether it was.
func (id *identity) set(key, value string) bool {
	switch strings.ToLower(key) {
	case "user.name":
		id.Name = value
	case "user.email":
		id.Email = value
	case "user.signingkey":
		id.SigningKey = value
	case "gpg.format":
		id.GPGFormat = value
	case "commit.gpgsign":
		id.GPGSign = normBool(value)
	case "core.sshcommand":
		id.SSHCommand = value
	default:
		return false
	}
	return true
}

func normBool(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1", "":
		return "true"
	case "false", "no", "off", "0":
		return "false"
	}
	return v
}

// Name-hint priorities: lower wins.
const (
	hintFragment = iota
	hintGitKraken
	hintGlobal
)

type hint struct {
	priority int
	name     string
}

type cluster struct {
	id      identity
	hints   []hint
	sources []string
	extra   map[string]string
	name    string
}

type importer struct {
	opts     Options
	res      *Result
	clusters []*cluster
	byKey    map[string]*cluster
	global   identity
	events   []event // identity settings and conditional includes, in git's reading order
	frags    []fragment
	conds    []condInclude
	globalC  *cluster // cluster of the unconditional global identity
}

// event is one step in reading the global config: an identity key being set,
// or (frag >= 0) a conditional include whose fragment applies at this position.
type event struct {
	key, value string
	frag       int
}

// fragment is a parsed includeIf target.
type fragment struct {
	cond, path, from string
	entries          []gitx.Entry // identity entries, in order
	extra            map[string]string
}

type condInclude struct {
	cond    string
	cluster *cluster
}

type pendingRepo struct {
	path    string
	cluster *cluster
	target  match.Target
}

// Import runs the import.
func Import(opts Options) (*Result, error) {
	if opts.FoldThreshold == 0 {
		opts.FoldThreshold = 3
	}
	im := &importer{
		opts:  opts,
		res:   &Result{Sources: map[string][]string{}},
		byKey: map[string]*cluster{},
	}

	// 1. Global config and the fragments it includes.
	var globalSources []string
	for _, f := range opts.GlobalFiles {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		if err := im.readGlobal(f, 0); err != nil {
			return nil, err
		}
		globalSources = append(globalSources, paths.Contract(f))
	}
	im.clusterFragments()
	if im.global.Email != "" {
		im.globalC = im.add(im.global, hint{hintGlobal, "default"}, "global identity in "+strings.Join(globalSources, ", "))
	}

	// 2. GitKraken.
	if opts.GitKrakenDir != "" {
		im.readGitKraken(opts.GitKrakenDir)
	}

	// 3. Repositories.
	roots := opts.Roots
	if len(roots) == 0 {
		roots = im.condDirs()
	}
	im.res.Scanned = roots
	pending, globalUsers, err := im.scanRepos(roots)
	if err != nil {
		return nil, err
	}

	// 4. Names, then the config.
	im.assignNames()
	if im.globalC != nil {
		im.warnf("global identity %s <%s> imported as profile %q: after `gitident sync` with strict_identity it no longer applies to repositories that match no rule — add `dirs`/`repos` for them (see hints)",
			im.global.Name, im.global.Email, im.globalC.name)
	}
	cfg := im.buildConfig()

	// Local identities already produced by the imported rules need no repos entry.
	for _, p := range pending {
		exp := match.Expect(cfg, p.target)
		if exp.Profile == p.cluster.name {
			continue
		}
		entry := paths.Contract(p.path)
		prof := cfg.Profiles[p.cluster.name]
		if !containsRepo(prof.Repos, entry) {
			prof.Repos = append(prof.Repos, entry)
		}
	}
	im.res.Config = cfg
	im.hints(cfg, globalUsers)
	return im.res, nil
}

func (im *importer) warnf(format string, args ...any) {
	im.res.Warnings = append(im.res.Warnings, fmt.Sprintf(format, args...))
}

// add finds or creates the cluster for id.
func (im *importer) add(id identity, h hint, source string) *cluster {
	c, ok := im.byKey[id.key()]
	if !ok {
		c = &cluster{id: id, extra: map[string]string{}}
		im.byKey[id.key()] = c
		im.clusters = append(im.clusters, c)
	}
	if h.name != "" {
		c.hints = append(c.hints, h)
	}
	if source != "" && !contains(c.sources, source) {
		c.sources = append(c.sources, source)
	}
	return c
}

// readGlobal reads one global config file, following unconditional includes and
// collecting conditional ones.
func (im *importer) readGlobal(file string, depth int) error {
	if depth > 5 {
		im.warnf("%s: includes nested too deeply, stopping", paths.Contract(file))
		return nil
	}
	entries, err := gitx.ListFile(file)
	if err != nil {
		return fmt.Errorf("reading %s: %w", paths.Contract(file), err)
	}
	for _, e := range entries {
		key := strings.ToLower(e.Key)
		switch {
		case im.global.set(e.Key, e.Value):
			im.events = append(im.events, event{key: e.Key, value: e.Value, frag: -1})
		case key == "include.path":
			p := resolveIncludePath(e.Value, file)
			if _, err := os.Stat(p); err != nil {
				continue // git ignores missing includes too
			}
			if err := im.readGlobal(p, depth+1); err != nil {
				return err
			}
		case strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path"):
			cond := e.Key[len("includeif.") : len(e.Key)-len(".path")]
			im.readConditional(cond, resolveIncludePath(e.Value, file), file)
		}
	}
	return nil
}

// readConditional parses an includeIf fragment and records its position.
func (im *importer) readConditional(cond, path, from string) {
	entries, err := gitx.ListFile(path)
	if err != nil {
		if _, statErr := os.Stat(path); statErr != nil {
			im.warnf("includeIf %q in %s points to missing file %s; skipped", cond, paths.Contract(from), paths.Contract(path))
		} else {
			im.warnf("cannot read %s: %v; skipped", paths.Contract(path), err)
		}
		return
	}
	f := fragment{cond: cond, path: path, from: from, extra: map[string]string{}}
	for _, e := range entries {
		key := strings.ToLower(e.Key)
		var probe identity
		if probe.set(e.Key, e.Value) {
			f.entries = append(f.entries, e)
			continue
		}
		if strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") {
			im.warnf("%s: nested include %s = %s is not imported", paths.Contract(path), e.Key, e.Value)
			continue
		}
		f.extra[e.Key] = e.Value
	}
	if len(f.entries) == 0 {
		im.warnf("includeIf %q → %s sets no identity; skipped (its settings are not imported)", cond, paths.Contract(path))
		return
	}
	im.events = append(im.events, event{frag: len(im.frags)})
	im.frags = append(im.frags, f)
}

// clusterFragments computes the identity each conditional include effectively
// produces — global identity entries and the fragment applied in file order, as
// git does — and clusters it.
func (im *importer) clusterFragments() {
	for i, f := range im.frags {
		var id identity
		for _, ev := range im.events {
			switch {
			case ev.frag < 0:
				id.set(ev.key, ev.value)
			case ev.frag == i:
				for _, e := range f.entries {
					id.set(e.Key, e.Value)
				}
			}
		}
		if id.Email == "" {
			im.warnf("includeIf %q → %s has no user.email (and there is no global one); skipped", f.cond, paths.Contract(f.path))
			continue
		}
		c := im.add(id, hint{hintFragment, nameFromFile(f.path)}, fmt.Sprintf("includeIf %q → %s", f.cond, paths.Contract(f.path)))
		for _, k := range sortedKeys(f.extra) {
			v := f.extra[k]
			if old, ok := c.extra[k]; ok && old != v {
				im.warnf("conflicting %s for %s: keeping %q, ignoring %q from %s", k, id.Email, old, v, paths.Contract(f.path))
				continue
			}
			c.extra[k] = v
		}
		im.conds = append(im.conds, condInclude{cond: f.cond, cluster: c})
	}
}

func resolveIncludePath(value, from string) string {
	p := paths.Expand(value)
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(from), p)
	}
	return filepath.Clean(p)
}

// nameFromFile derives a profile name from a fragment file name:
// "work.gitconfig" → "work", ".gitconfig-work" → "work", "gitconfig.personal" → "personal".
func nameFromFile(path string) string {
	base := strings.TrimPrefix(filepath.Base(path), ".")
	for _, suf := range []string{".gitconfig", ".inc", ".conf", ".config", ".cfg", ".ini"} {
		base = strings.TrimSuffix(base, suf)
	}
	for _, pre := range []string{"gitconfig-", "gitconfig_", "gitconfig.", "gitconfig"} {
		if strings.HasPrefix(base, pre) && len(base) > len(pre) {
			base = base[len(pre):]
			break
		}
	}
	return slug(base)
}

// condDirs returns the directories of imported gitdir conditions (scan roots default).
func (im *importer) condDirs() []string {
	var dirs []string
	for _, ci := range im.conds {
		if kind, v := parseCond(ci.cond); kind == condDir {
			dirs = append(dirs, v)
		}
	}
	return dirs
}

const (
	condDir = iota + 1
	condRepoPath
	condRemote
	condRepoURL
	condUnsupported
)

// parseCond classifies an includeIf condition.
func parseCond(cond string) (kind int, value string) {
	lower := strings.ToLower(cond)
	switch {
	case strings.HasPrefix(lower, "gitdir:") || strings.HasPrefix(lower, "gitdir/i:"):
		v := cond[strings.IndexByte(cond, ':')+1:]
		switch {
		case strings.HasSuffix(v, "/.git"):
			return condRepoPath, contractPattern(strings.TrimSuffix(v, "/.git"))
		case strings.HasSuffix(v, "/**"):
			return condDir, contractPattern(strings.TrimSuffix(v, "/**"))
		case strings.HasSuffix(v, "/"):
			return condDir, contractPattern(strings.TrimRight(v, "/"))
		}
		return condUnsupported, cond
	case strings.HasPrefix(lower, "hasconfig:remote.*.url:"):
		v := cond[len("hasconfig:remote.*.url:"):]
		if match.HasGlob(v) {
			return condRemote, v
		}
		return condRepoURL, v
	}
	return condUnsupported, cond
}

func contractPattern(p string) string {
	if strings.HasPrefix(p, "~") {
		return p
	}
	return paths.Contract(p)
}

// scanRepos finds repositories whose identity is set locally. It also returns
// repositories that currently rely on the global identity.
func (im *importer) scanRepos(roots []string) ([]pendingRepo, []string, error) {
	if len(roots) == 0 {
		return nil, nil, nil
	}
	repos, err := scan.Find(roots, scan.Options{Ignore: im.opts.Ignore})
	if err != nil {
		return nil, nil, err
	}
	var pending []pendingRepo
	var globalUsers []string
	for _, repo := range repos {
		local, err := gitx.ListLocal(repo)
		if err != nil {
			im.warnf("%s: %v; skipped", paths.Contract(repo), err)
			continue
		}
		hasLocal := false
		var pinHint string
		for _, e := range local {
			var probe identity
			if probe.set(e.Key, e.Value) && (probe.Name != "" || probe.Email != "") {
				hasLocal = true
			}
			if strings.ToLower(e.Key) == "gitident.profile" && pinHint == "" {
				pinHint = e.Value // a copy made by `gitident apply`
			}
			if strings.ToLower(e.Key) == "include.path" {
				hasLocal = true
				if p, ok := paths.ProfileFromFragment(e.Value); ok {
					pinHint = p
				} else {
					pinHint = nameFromFile(e.Value)
				}
			}
		}
		r, err := inspect.Inspect(nil, repo)
		if err != nil {
			im.warnf("%s: %v; skipped", paths.Contract(repo), err)
			continue
		}
		if !hasLocal {
			if origin := r.Origins["user.email"]; origin != "" && im.isGlobalFile(origin) {
				globalUsers = append(globalUsers, paths.Contract(repo))
			}
			continue
		}
		var id identity
		for k, v := range r.Values {
			id.set(k, v)
		}
		if id.Email == "" {
			continue
		}
		h := hint{}
		if pinHint != "" {
			h = hint{hintFragment, pinHint}
		}
		c := im.add(id, h, "local config of "+paths.Contract(repo))
		pending = append(pending, pendingRepo{path: repo, cluster: c, target: r.Target()})
	}
	return pending, globalUsers, nil
}

func (im *importer) isGlobalFile(p string) bool {
	for _, f := range im.opts.GlobalFiles {
		if paths.SamePath(f, p) {
			return true
		}
	}
	return false
}

var genericDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "yahoo.com": true, "outlook.com": true, "hotmail.com": true,
	"live.com": true, "icloud.com": true, "me.com": true, "mac.com": true, "proton.me": true,
	"protonmail.com": true, "pm.me": true, "fastmail.com": true, "yandex.ru": true, "ya.ru": true,
	"mail.ru": true, "gmx.de": true, "gmx.net": true, "aol.com": true, "hey.com": true,
	"users.noreply.github.com": true, "users.noreply.gitlab.com": true,
}

// emailName derives a profile name from an email: the domain's first label for
// company domains (welltory.com → welltory), the local part for mail providers.
func emailName(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return slug(email)
	}
	local, domain := email[:at], strings.ToLower(email[at+1:])
	if genericDomains[domain] {
		if i := strings.IndexByte(local, '+'); i >= 0 && strings.HasSuffix(domain, "noreply.github.com") {
			local = local[i+1:]
		}
		return slug(local)
	}
	labels := strings.Split(domain, ".")
	if len(labels) >= 2 {
		return slug(labels[len(labels)-2])
	}
	return slug(domain)
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

func slug(s string) string {
	s = slugRe.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-._")
	if s == "" {
		return "profile"
	}
	return s
}

var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (im *importer) assignNames() {
	taken := map[string]bool{}
	for _, c := range im.clusters {
		suggestion := emailName(c.id.Email)
		best := -1
		for i, h := range c.hints {
			if best < 0 || h.priority < c.hints[best].priority {
				best = i
			}
		}
		if best >= 0 {
			suggestion = c.hints[best].name
		}
		name := unique(suggestion, taken)
		if im.opts.Prompt != nil {
			q := fmt.Sprintf("profile name for %s <%s>", c.id.Name, c.id.Email)
			for tries := 0; tries < 5; tries++ {
				answer := strings.TrimSpace(im.opts.Prompt(q, name))
				if answer == "" {
					break
				}
				if !profileNameRe.MatchString(answer) || taken[answer] {
					q = fmt.Sprintf("%q is invalid or taken; profile name for %s <%s>", answer, c.id.Name, c.id.Email)
					continue
				}
				name = answer
				break
			}
		}
		taken[name] = true
		c.name = name
	}
}

func unique(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		n := name + "-" + strconv.Itoa(i)
		if !taken[n] {
			return n
		}
	}
}

// buildConfig assembles profiles and rules from clusters and conditions.
func (im *importer) buildConfig() *config.Config {
	cfg := &config.Config{Version: config.CurrentVersion, Profiles: map[string]*config.Profile{}}
	strict := true
	cfg.StrictIdentity = &strict
	for _, c := range im.clusters {
		p := &config.Profile{Name: c.id.Name, Email: c.id.Email, SigningKey: c.id.SigningKey, GPGFormat: c.id.GPGFormat}
		if c.id.GPGSign != "" {
			b := c.id.GPGSign == "true"
			p.GPGSign = &b
		}
		if len(c.extra) > 0 {
			p.Extra = map[string]string{}
			for k, v := range c.extra {
				p.Extra[k] = v
			}
		}
		if c.id.SSHCommand != "" {
			if key, ok := render.SSHKeyFromCommand(c.id.SSHCommand); ok {
				p.SSHKey = key
			} else {
				if p.Extra == nil {
					p.Extra = map[string]string{}
				}
				p.Extra["core.sshCommand"] = c.id.SSHCommand
			}
		}
		cfg.Profiles[c.name] = p
		im.res.Order = append(im.res.Order, c.name)
		im.res.Sources[c.name] = c.sources
	}

	// Rules keep the includeIf order; consecutive conditions for the same profile merge.
	for _, ci := range im.conds {
		kind, v := parseCond(ci.cond)
		name := ci.cluster.name
		p := cfg.Profiles[name]
		switch kind {
		case condRepoPath, condRepoURL:
			if !containsRepo(p.Repos, v) {
				p.Repos = append(p.Repos, v)
			}
			continue
		case condUnsupported:
			im.warnf("includeIf %q is not supported by gitident (only gitdir: and hasconfig:remote.*.url:); profile %q is imported without it", ci.cond, name)
			continue
		}
		if n := len(cfg.Rules); n == 0 || cfg.Rules[n-1].Profile != name {
			cfg.Rules = append(cfg.Rules, config.Rule{Profile: name})
		}
		r := &cfg.Rules[len(cfg.Rules)-1]
		if kind == condDir && !contains(r.Dirs, v) {
			r.Dirs = append(r.Dirs, v)
		}
		if kind == condRemote && !contains(r.Remotes, v) {
			r.Remotes = append(r.Remotes, v)
		}
	}
	return cfg
}

// hints adds fold suggestions and notes about repos using the global identity.
func (im *importer) hints(cfg *config.Config, globalUsers []string) {
	byParent := map[string]map[string]int{} // profile → parent → count
	add := func(profile, repo string) {
		if byParent[profile] == nil {
			byParent[profile] = map[string]int{}
		}
		byParent[profile][filepath.Dir(repo)]++
	}
	for _, name := range im.res.Order {
		for _, repo := range cfg.Profiles[name].Repos {
			if !paths.IsURL(repo) {
				add(name, repo)
			}
		}
	}
	if len(globalUsers) > 0 {
		shown := globalUsers
		if len(shown) > 5 {
			shown = append(append([]string{}, shown[:5]...), fmt.Sprintf("… %d more", len(globalUsers)-5))
		}
		im.res.Hints = append(im.res.Hints, fmt.Sprintf("%s currently use the global identity and match no imported rule: %s",
			pluralRepos(len(globalUsers)), strings.Join(shown, ", ")))
		for _, r := range globalUsers {
			if im.globalC != nil {
				add(im.globalC.name, r)
			}
		}
	}
	for _, name := range sortedKeys(byParent) {
		for _, parent := range sortedKeys(byParent[name]) {
			n := byParent[name][parent]
			if n < im.opts.FoldThreshold || coveredByRule(cfg, parent) {
				continue
			}
			im.res.Hints = append(im.res.Hints, fmt.Sprintf("%d repos of profile %q are in %s — consider a rule `- profile: %s` with `dirs: [%q]` instead of listing them",
				n, name, parent, name, parent))
		}
	}
}

func coveredByRule(cfg *config.Config, dir string) bool {
	for _, r := range cfg.Rules {
		for _, d := range r.Dirs {
			if paths.Within(dir, d) {
				return true
			}
		}
	}
	return false
}

func pluralRepos(n int) string {
	if n == 1 {
		return "1 repo"
	}
	return fmt.Sprintf("%d repos", n)
}

func containsRepo(list []string, entry string) bool {
	key := config.RepoKey(entry)
	for _, e := range list {
		if e == entry || (key != "" && config.RepoKey(e) == key) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
