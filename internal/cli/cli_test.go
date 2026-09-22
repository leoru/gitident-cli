package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolated(m)) }

type result struct {
	stdout, stderr string
	code           int
}

func run(t *testing.T, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	a := &App{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""), Version: "test"}
	code := a.Run(args)
	return result{out.String(), errb.String(), code}
}

func mustRun(t *testing.T, args ...string) result {
	t.Helper()
	r := run(t, args...)
	if r.code != 0 {
		t.Fatalf("gitident %v exited %d\nstdout:\n%s\nstderr:\n%s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

// commitAuthor makes a commit in dir and returns "name <email>", or the error output.
func commitAuthor(t *testing.T, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", "commit", "--allow-empty", "-q", "-m", "test")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return string(out), err
	}
	return strings.TrimSpace(testutil.Git(t, dir, "log", "-1", "--format=%an <%ae>")), nil
}

const e2eConfig = `# my identities
version: 1
profiles:
  personal:
    name: Pat Personal
    email: pat@home.org
  work:
    name: Pat Work
    email: pat@company.com
    extra:
      pull.rebase: "true"
  oss:
    name: Pat OSS
    email: pat@oss.dev
    repos:
      - ~/misc/legacy                       # a path
      - https://github.com/me/notes         # a URL
rules:
  - profile: personal
    dirs: ["~/code"]
  - profile: work        # later rule wins
    dirs: ["~/work"]
    remotes: ["git@github.com:company/**"]
`

func setupE2E(t *testing.T) string {
	home := testutil.Home(t)
	testutil.WriteFile(t, paths.ConfigPath(), e2eConfig)
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), "[core]\n\teditor = vim\n")
	testutil.InitRepo(t, filepath.Join(home, "code", "blog"))
	testutil.InitRepo(t, filepath.Join(home, "code", "company-tool"), "git@github.com:company/tool.git")
	testutil.InitRepo(t, filepath.Join(home, "work", "api"))
	testutil.InitRepo(t, filepath.Join(home, "work", "legacy-in-work"))
	testutil.InitRepo(t, filepath.Join(home, "misc", "legacy"))
	testutil.InitRepo(t, filepath.Join(home, "code", "notes"), "https://github.com/me/notes.git")
	testutil.InitRepo(t, filepath.Join(home, "tmp", "unmatched"))
	return home
}

func TestEndToEnd(t *testing.T) {
	home := setupE2E(t)

	// sync writes fragments and the block, and is idempotent.
	r := mustRun(t, "sync")
	if !strings.Contains(r.stdout, "synced 3 profiles, 2 rules, 2 repos") {
		t.Errorf("sync output:\n%s", r.stdout)
	}
	gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if !strings.HasPrefix(string(gc), "[core]\n\teditor = vim\n\n# >>> gitident managed block") {
		t.Errorf("global config not preserved:\n%s", gc)
	}
	if r := mustRun(t, "sync"); !strings.HasPrefix(r.stdout, "up to date") {
		t.Errorf("second sync should be a no-op:\n%s", r.stdout)
	}

	// Real git resolves identities as the precedence model says.
	cases := map[string]string{
		"code/blog":         "Pat Personal <pat@home.org>",
		"code/company-tool": "Pat Work <pat@company.com>", // remote rule after dir rule
		"work/api":          "Pat Work <pat@company.com>",
		"misc/legacy":       "Pat OSS <pat@oss.dev>", // repos path
		"code/notes":        "Pat OSS <pat@oss.dev>", // repos URL beats dir rule
	}
	for rel, want := range cases {
		got, err := commitAuthor(t, filepath.Join(home, rel))
		if err != nil || got != want {
			t.Errorf("%s: commit author = %q (%v), want %q", rel, got, err, want)
		}
	}
	if out, err := commitAuthor(t, filepath.Join(home, "tmp", "unmatched")); err == nil {
		t.Errorf("commit in unmatched repo should fail in strict mode, got %q", out)
	}

	// which
	r = mustRun(t, "which", filepath.Join(home, "code", "notes"))
	for _, want := range []string{"profile    oss  (via ~/.gitconfig.d/gitident/oss.gitconfig)", "expected   oss  (repos: https://github.com/me/notes)"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("which output lacks %q:\n%s", want, r.stdout)
		}
	}
	r = mustRun(t, "which", filepath.Join(home, "tmp", "unmatched"))
	if !strings.Contains(r.stdout, "NONE — commits will fail") {
		t.Errorf("which unmatched:\n%s", r.stdout)
	}
	r = mustRun(t, "which", "--json", filepath.Join(home, "work", "api"))
	var wj whichJSON
	if err := json.Unmarshal([]byte(r.stdout), &wj); err != nil || wj.Profile != "work" || wj.Via != "fragment" || wj.Values["user.email"] != "pat@company.com" || !wj.CanCommit {
		t.Errorf("which --json = %+v (%v)\n%s", wj, err, r.stdout)
	}

	// Pin, unpin.
	unmatched := filepath.Join(home, "tmp", "unmatched")
	mustRun(t, "use", "personal", unmatched)
	if got, _ := commitAuthor(t, unmatched); got != "Pat Personal <pat@home.org>" {
		t.Errorf("pinned commit author = %q", got)
	}
	r = mustRun(t, "which", unmatched)
	if !strings.Contains(r.stdout, "pinned     personal") {
		t.Errorf("which pinned:\n%s", r.stdout)
	}
	mustRun(t, "use", "work", unmatched) // replaces the previous pin
	if vals := testutil.Git(t, unmatched, "config", "--local", "--get-all", "include.path"); strings.Count(vals, "\n") != 1 || !strings.Contains(vals, "work.gitconfig") {
		t.Errorf("pins after re-pin: %q", vals)
	}
	r = mustRun(t, "unuse", unmatched)
	if !strings.Contains(r.stdout, "unpinned") || !strings.Contains(r.stdout, "no profile matches") {
		t.Errorf("unuse output:\n%s", r.stdout)
	}
	if out, err := commitAuthor(t, unmatched); err == nil {
		t.Errorf("after unuse the commit should fail again, got %q", out)
	}
}

func TestCheckStatuses(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	// UNKNOWN EMAIL: local identity not in any profile.
	rogue := testutil.InitRepo(t, filepath.Join(home, "code", "rogue"))
	testutil.Git(t, rogue, "config", "user.email", "rogue@elsewhere.net")
	// MISMATCH: local identity of another profile.
	mixed := testutil.InitRepo(t, filepath.Join(home, "work", "mixed"))
	testutil.Git(t, mixed, "config", "user.email", "pat@home.org")
	// ok, set outside gitident: local identity equal to the expected profile.
	same := testutil.InitRepo(t, filepath.Join(home, "work", "same"))
	testutil.Git(t, same, "config", "user.email", "pat@company.com")
	// MISSING and NOT CLONED entries.
	cfg := strings.Replace(e2eConfig, "      - https://github.com/me/notes         # a URL\n",
		"      - https://github.com/me/notes         # a URL\n      - ~/gone/repo\n      - git@github.com:me/never-cloned.git\n", 1)
	testutil.WriteFile(t, paths.ConfigPath(), cfg)
	mustRun(t, "sync")

	r := run(t, "check", "--json", filepath.Join(home, "code"), filepath.Join(home, "work"), filepath.Join(home, "tmp"))
	if r.code != 1 {
		t.Errorf("check exit = %d, want 1\n%s%s", r.code, r.stdout, r.stderr)
	}
	var items []checkItem
	if err := json.Unmarshal([]byte(r.stdout), &items); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	got := map[string]checkItem{}
	for _, it := range items {
		got[it.Path] = it
	}
	want := map[string]string{
		"~/code/blog":                        statusOK,
		"~/code/company-tool":                statusOK,
		"~/code/notes":                       statusOK,
		"~/code/rogue":                       statusUnknown,
		"~/work/api":                         statusOK,
		"~/work/mixed":                       statusMismatch,
		"~/work/same":                        statusOK,
		"~/tmp/unmatched":                    statusNoIdent,
		"~/misc/legacy":                      statusOK,
		"~/gone/repo":                        statusMissing,
		"git@github.com:me/never-cloned.git": statusNotCloned,
	}
	for path, status := range want {
		if got[path].Status != status {
			t.Errorf("%s: status %q (%s), want %q", path, got[path].Status, got[path].Detail, status)
		}
	}
	if !strings.Contains(got["~/work/same"].Detail, "set outside gitident") {
		t.Errorf("~/work/same detail = %q", got["~/work/same"].Detail)
	}
	if got["~/code/notes"].Profile != "oss" {
		t.Errorf("notes profile = %q", got["~/code/notes"].Profile)
	}

	// Text output and default roots.
	r = run(t, "check", "-q")
	if r.code != 1 || strings.Contains(r.stdout, "~/code/blog") || !strings.Contains(r.stdout, "MISMATCH") || !strings.Contains(r.stdout, "hint:") {
		t.Errorf("check -q:\n%s", r.stdout)
	}
}

func TestSyncDryRunAndInit(t *testing.T) {
	home := testutil.Home(t)
	mustRun(t, "init")
	if r := run(t, "init"); r.code == 0 || !strings.Contains(r.stderr, "already exists") {
		t.Errorf("second init should refuse: %+v", r)
	}
	mustRun(t, "init", "--force")
	r := mustRun(t, "sync", "--dry-run")
	if !strings.Contains(r.stdout, "would write managed block") || !strings.Contains(r.stdout, "nothing written") {
		t.Errorf("dry run output:\n%s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); !os.IsNotExist(err) {
		t.Error("dry run wrote ~/.gitconfig")
	}
	if _, err := os.Stat(paths.FragmentDir()); !os.IsNotExist(err) {
		t.Error("dry run wrote fragments")
	}
}

func TestInitPrefillsGlobalIdentityAndWarns(t *testing.T) {
	home := testutil.Home(t)
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = Old Name\n\temail = old@example.org\n")
	mustRun(t, "init")
	data, _ := os.ReadFile(paths.ConfigPath())
	if !strings.Contains(string(data), "name: Old Name") || !strings.Contains(string(data), "email: old@example.org") {
		t.Errorf("init did not prefill:\n%s", data)
	}
	r := mustRun(t, "sync")
	if !strings.Contains(r.stderr, "sets user.email = old@example.org outside the managed block") {
		t.Errorf("expected global identity warning, stderr:\n%s", r.stderr)
	}
}

func TestPruneAndUninstall(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	cfg := strings.Replace(e2eConfig, "  oss:\n    name: Pat OSS\n    email: pat@oss.dev\n    repos:\n      - ~/misc/legacy                       # a path\n      - https://github.com/me/notes         # a URL\n", "", 1)
	testutil.WriteFile(t, paths.ConfigPath(), cfg)
	// A foreign file in the fragment dir is never pruned.
	testutil.WriteFile(t, filepath.Join(paths.FragmentDir(), "mine.gitconfig"), "[user]\n")
	r := mustRun(t, "sync", "--no-prune")
	if strings.Contains(r.stdout, "removed") {
		t.Errorf("--no-prune removed files:\n%s", r.stdout)
	}
	r = mustRun(t, "sync")
	if !strings.Contains(r.stdout, "removed ~/.gitconfig.d/gitident/oss.gitconfig") {
		t.Errorf("prune output:\n%s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(paths.FragmentDir(), "mine.gitconfig")); err != nil {
		t.Error("foreign file was pruned")
	}

	mustRun(t, "uninstall")
	gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if string(gc) != "[core]\n\teditor = vim\n" {
		t.Errorf("uninstall left:\n%q", gc)
	}
	if _, err := os.Stat(paths.FragmentPath("work")); !os.IsNotExist(err) {
		t.Error("fragment not removed")
	}
}

func TestUseSavePreservesComments(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	repo := filepath.Join(home, "work", "legacy-in-work")
	r := mustRun(t, "use", "oss", repo, "--save")
	if !strings.Contains(r.stdout, "added ~/work/legacy-in-work to profile \"oss\"") {
		t.Errorf("use --save output:\n%s", r.stdout)
	}
	data, _ := os.ReadFile(paths.ConfigPath())
	for _, want := range []string{"# my identities", "# a path", "# later rule wins", "- ~/work/legacy-in-work"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("profiles.yaml lost %q:\n%s", want, data)
		}
	}
	// The repos entry now applies even without the pin.
	mustRun(t, "unuse", repo)
	if got, _ := commitAuthor(t, repo); got != "Pat OSS <pat@oss.dev>" {
		t.Errorf("after unuse, repos entry should apply; author = %q", got)
	}
	r = mustRun(t, "unuse", repo)
	if !strings.Contains(r.stdout, "is not pinned") {
		t.Errorf("second unuse:\n%s", r.stdout)
	}
}

func TestWorktreePin(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	main := filepath.Join(home, "tmp", "unmatched")
	testutil.Git(t, main, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(home, "tmp", "wt")
	testutil.Git(t, main, "worktree", "add", "-q", wt)
	mustRun(t, "use", "work", wt)
	if got, _ := commitAuthor(t, main); got != "Pat Work <pat@company.com>" {
		t.Errorf("pin via worktree should apply to the main repo; author = %q", got)
	}
	if got, _ := commitAuthor(t, wt); got != "Pat Work <pat@company.com>" {
		t.Errorf("worktree author = %q", got)
	}
}

func TestValidationErrorsBlockSync(t *testing.T) {
	home := testutil.Home(t)
	testutil.WriteFile(t, paths.ConfigPath(), "version: 1\nprofiles:\n  a:\n    name: A\nrules:\n  - profile: b\n    dirs: [~/x]\n")
	r := run(t, "sync")
	if r.code != 1 || !strings.Contains(r.stderr, "`email` is required") || !strings.Contains(r.stderr, `unknown profile "b"`) {
		t.Errorf("sync with invalid config: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); !os.IsNotExist(err) {
		t.Error("invalid config must not write anything")
	}
}

func TestCorruptBlockRefused(t *testing.T) {
	home := setupE2E(t)
	orig := "[core]\n\teditor = vim\n# >>> gitident managed block — edit profiles.yaml and run `gitident sync` >>>\n[user]\n"
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), orig)
	r := run(t, "sync")
	if r.code == 0 || !strings.Contains(r.stderr, "damaged") {
		t.Errorf("expected refusal: %+v", r)
	}
	if gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig")); string(gc) != orig {
		t.Error("corrupt file was modified")
	}
}

func TestUsageErrors(t *testing.T) {
	testutil.Home(t)
	if r := run(t); r.code != 2 {
		t.Errorf("no args exit = %d", r.code)
	}
	if r := run(t, "bogus"); r.code != 2 || !strings.Contains(r.stderr, "unknown command") {
		t.Errorf("unknown command: %+v", r)
	}
	if r := run(t, "sync", "--nope"); r.code != 2 {
		t.Errorf("bad flag exit = %d", r.code)
	}
	if r := run(t, "help", "use"); r.code != 0 || !strings.Contains(r.stdout, "--save") {
		t.Errorf("help use: %+v", r)
	}
	if r := run(t, "use", "--help"); r.code != 0 {
		t.Errorf("use --help exit = %d", r.code)
	}
	if r := run(t, "version"); r.code != 0 || r.stdout != "gitident test\n" {
		t.Errorf("version: %+v", r)
	}
	for _, sh := range []string{"bash", "zsh", "fish"} {
		if r := run(t, "completion", sh); r.code != 0 || !strings.Contains(r.stdout, "__profiles") {
			t.Errorf("completion %s: %+v", sh, r.code)
		}
	}
}

func TestParseFlagsInterleaved(t *testing.T) {
	a := &App{Stderr: &bytes.Buffer{}}
	fs := a.newFlags("x", "")
	save := fs.Bool("save", false, "")
	pos, err := parseFlags(fs, []string{"work", "--save", "dir", "--", "--literal"})
	if err != nil || !*save || strings.Join(pos, ",") != "work,dir,--literal" {
		t.Errorf("pos=%q save=%v err=%v", pos, *save, err)
	}
}

func TestImportRoundTrip(t *testing.T) {
	home := testutil.Home(t)
	testutil.InitRepo(t, filepath.Join(home, "work", "api"))
	testutil.InitRepo(t, filepath.Join(home, "code", "tool"), "git@github.com:acme/tool.git")
	side := testutil.InitRepo(t, filepath.Join(home, "work", "side"))
	testutil.Git(t, side, "config", "user.email", "kirill@personal.dev")
	for _, n := range []string{"a", "b"} {
		r := testutil.InitRepo(t, filepath.Join(home, "code", n))
		testutil.Git(t, r, "config", "user.name", "Kirill Freelance")
		testutil.Git(t, r, "config", "user.email", "k@freelance.io")
	}
	testutil.InitRepo(t, filepath.Join(home, "oss", "lib"))
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), `[user]
	name = Kirill Kunst
	email = kirill@personal.dev
[includeIf "gitdir:~/work/"]
	path = ~/.gitconfig-work
[includeIf "hasconfig:remote.*.url:git@github.com:acme/**"]
	path = ~/.gitconfig-work
[includeIf "gitdir:~/oss/lib/.git"]
	path = ~/.gitconfig-oss
`)
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig-work"), "[user]\n\temail = kirill@acme.com\n[pull]\n\trebase = true\n")
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig-oss"), "[user]\n\tname = K\n\temail = k@oss.dev\n")

	// Dry run prints YAML and writes nothing.
	r := mustRun(t, "import", filepath.Join(home, "work"), filepath.Join(home, "code"), filepath.Join(home, "oss"))
	if !strings.Contains(r.stdout, "# source: includeIf") || !strings.Contains(r.stderr, "dry run") {
		t.Errorf("import dry run:\n%s\n%s", r.stdout, r.stderr)
	}
	if _, err := os.Stat(paths.ConfigPath()); !os.IsNotExist(err) {
		t.Fatal("dry run wrote profiles.yaml")
	}
	before, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))

	mustRun(t, "import", "--write", filepath.Join(home, "work"), filepath.Join(home, "code"), filepath.Join(home, "oss"))
	if after, _ := os.ReadFile(filepath.Join(home, ".gitconfig")); !bytes.Equal(before, after) {
		t.Error("import touched ~/.gitconfig")
	}
	if r := run(t, "import", "--write"); r.code == 0 || !strings.Contains(r.stderr, "already exists") {
		t.Errorf("second --write should refuse: %+v", r)
	}
	if r := mustRun(t, "import", "--merge"); !strings.Contains(r.stdout, "nothing new to merge") {
		t.Errorf("merge of identical import:\n%s", r.stdout)
	}

	mustRun(t, "sync")
	r = run(t, "check", filepath.Join(home, "work"), filepath.Join(home, "code"), filepath.Join(home, "oss"))
	if r.code != 0 || strings.Contains(r.stdout, "MISMATCH") || strings.Contains(r.stdout, "NO IDENTITY") || strings.Contains(r.stdout, "UNKNOWN") {
		t.Errorf("check after import+sync (exit %d):\n%s\n%s", r.code, r.stdout, r.stderr)
	}

	// Removing the old hand-written setup leaves identities intact.
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), "")
	mustRun(t, "sync")
	r = run(t, "check", filepath.Join(home, "work"), filepath.Join(home, "code"), filepath.Join(home, "oss"))
	if r.code != 0 {
		t.Errorf("check after removing the old config (exit %d):\n%s", r.code, r.stdout)
	}
	if got, _ := commitAuthor(t, filepath.Join(home, "code", "tool")); got != "Kirill Kunst <kirill@acme.com>" {
		t.Errorf("remote-rule author = %q", got)
	}
}

func TestDoctor(t *testing.T) {
	setupE2E(t)
	r := run(t, "doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "no managed block") {
		t.Errorf("doctor before sync (exit %d):\n%s", r.code, r.stdout)
	}
	mustRun(t, "sync")
	r = run(t, "doctor")
	if r.code != 0 || !strings.Contains(r.stdout, "managed block in ~/.gitconfig is current") || !strings.Contains(r.stdout, "all checks passed") {
		t.Errorf("doctor after sync (exit %d):\n%s", r.code, r.stdout)
	}
	// Out-of-date after editing profiles.yaml.
	data, _ := os.ReadFile(paths.ConfigPath())
	testutil.WriteFile(t, paths.ConfigPath(), strings.Replace(string(data), "pat@home.org", "pat@new-home.org", 1))
	r = run(t, "doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "fragment in ~/.gitconfig.d/gitident missing or out of date") {
		t.Errorf("doctor after edit (exit %d):\n%s", r.code, r.stdout)
	}
}

func TestGlobalUserWritesStayOutsideBlock(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	// `git config --global user.*` appends to the last [user] section; the block
	// must not have one, or sync would later delete the value.
	testutil.Git(t, home, "config", "--global", "user.signingkey", "ABC123")
	gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	end := strings.Index(string(gc), "# <<< gitident managed block <<<")
	if i := strings.Index(string(gc), "signingkey = ABC123"); i < 0 || i < end {
		t.Fatalf("global write landed inside the block:\n%s", gc)
	}
	mustRun(t, "sync")
	if gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig")); !strings.Contains(string(gc), "signingkey = ABC123") {
		t.Errorf("sync lost a global setting:\n%s", gc)
	}
	// Strict mode still applies through the included fragment.
	if out, err := commitAuthor(t, filepath.Join(home, "tmp", "unmatched")); err == nil {
		t.Errorf("strict mode not in effect: %q", out)
	}
}

func TestSyncRefusesForeignLinesInBlock(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	p := filepath.Join(home, ".gitconfig")
	gc, _ := os.ReadFile(p)
	edited := strings.Replace(string(gc), "# <<< gitident", "[alias]\n\tst = status\n# <<< gitident", 1)
	testutil.WriteFile(t, p, edited)
	r := run(t, "sync")
	if r.code != 1 || !strings.Contains(r.stderr, "st = status") {
		t.Errorf("sync should refuse and list the foreign line: %+v", r)
	}
	if now, _ := os.ReadFile(p); string(now) != edited {
		t.Error("file changed despite refusal")
	}
	if r := run(t, "doctor"); r.code != 1 || !strings.Contains(r.stdout, "settings inside the managed block") {
		t.Errorf("doctor: %+v", r)
	}
}

func TestWhichFromSubdirSeesPin(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	repo := filepath.Join(home, "tmp", "unmatched")
	mustRun(t, "use", "work", repo)
	sub := filepath.Join(repo, "a", "b")
	_ = os.MkdirAll(sub, 0o755)
	r := mustRun(t, "which", sub)
	if !strings.Contains(r.stdout, "pinned     work") || strings.Contains(r.stdout, "note:") {
		t.Errorf("which from subdir:\n%s", r.stdout)
	}
}
