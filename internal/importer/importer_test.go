package importer

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolated(m)) }

// Fixture builds a home directory with a hand-written gitconfig and repo tree.
func Fixture(t *testing.T) string {
	home := testutil.Home(t)
	testutil.InitRepo(t, filepath.Join(home, "work", "api"))
	side := testutil.InitRepo(t, filepath.Join(home, "work", "side"))
	testutil.Git(t, side, "config", "user.email", "kirill@personal.dev")
	for _, n := range []string{"a", "b", "c"} {
		r := testutil.InitRepo(t, filepath.Join(home, "code", n))
		testutil.Git(t, r, "config", "user.name", "Kirill Freelance")
		testutil.Git(t, r, "config", "user.email", "k@freelance.io")
	}
	// Already correct via its rule: must not become a repos entry.
	redundant := testutil.InitRepo(t, filepath.Join(home, "work", "redundant"))
	testutil.Git(t, redundant, "config", "user.email", "kirill@acme.com")
	// A repo pinned to some fragment file.
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig.d", "gitident", "client.gitconfig"), "[user]\n\tname = Kirill\n\temail = kirill@client.example\n")
	pinned := testutil.InitRepo(t, filepath.Join(home, "code", "pinned"))
	testutil.Git(t, pinned, "config", "include.path", "~/.gitconfig.d/gitident/client.gitconfig")
	testutil.InitRepo(t, filepath.Join(home, "oss", "lib"))
	testutil.InitRepo(t, filepath.Join(home, "tmp", "uses-global"))

	// Written after the repos: git 2.45 crashes in `git init` with an
	// includeIf "onbranch:" in the global config.
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), `[user]
	name = Kirill Kunst
	email = kirill@personal.dev
[core]
	editor = vim
[includeIf "gitdir:~/work/"]
	path = ~/.gitconfig-work
[includeIf "hasconfig:remote.*.url:git@github.com:acme/**"]
	path = ~/.gitconfig-work
[includeIf "gitdir:~/oss/lib/.git"]
	path = .gitconfig.d/oss.inc
[includeIf "hasconfig:remote.*.url:https://github.com/me/notes"]
	path = ~/.gitconfig.d/oss.inc
[includeIf "onbranch:release"]
	path = ~/.gitconfig-work
[includeIf "gitdir:~/missing/"]
	path = ~/nope.gitconfig
`)
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig-work"), `[user]
	email = kirill@acme.com
	signingkey = ~/.ssh/id_acme.pub
[gpg]
	format = ssh
[commit]
	gpgsign = true
[core]
	sshCommand = ssh -i ~/.ssh/id_acme -o IdentitiesOnly=yes
[pull]
	rebase = true
`)
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig.d", "oss.inc"), "[user]\n\tname = Kirill K\n\temail = k@oss.dev\n")

	gk := filepath.Join(home, ".gitkraken", "profiles")
	testutil.WriteFile(t, filepath.Join(gk, "p1", "profile"), `{"profileName":"Work Laptop","userName":"Kirill Kunst","userEmail":"kirill@acme.com","settings":{"email":"ignored@nested.example"}}`)
	testutil.WriteFile(t, filepath.Join(gk, "p2", "profile"), `{not json`)
	// Same name and email as the global identity, nothing else: folds into it
	// and lends it its name.
	testutil.WriteFile(t, filepath.Join(gk, "p3", "profile"), `{"profileName":"Home","userName":"Kirill Kunst","userEmail":"kirill@personal.dev"}`)
	return home
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/importer -update`)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestImportGolden(t *testing.T) {
	home := Fixture(t)
	res, err := Import(Options{
		GlobalFiles:  []string{filepath.Join(home, ".config", "git", "config"), filepath.Join(home, ".gitconfig")},
		Roots:        []string{filepath.Join(home, "work"), filepath.Join(home, "code"), filepath.Join(home, "oss"), filepath.Join(home, "tmp")},
		GitKrakenDir: filepath.Join(home, ".gitkraken"),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.YAML()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "import.yaml", out)

	// The output is a valid config.
	cfg, err := config.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if rep := cfg.Validate(); !rep.OK() {
		t.Errorf("imported config invalid: %v", rep.Errors)
	}

	wantWarnings := []string{
		`includeIf "onbranch:release" is not supported`,
		"points to missing file ~/nope.gitconfig",
		"GitKraken profile ~/.gitkraken/profiles/p2/profile: invalid JSON",
		`imported as profile "home"`,
	}
	joined := strings.Join(res.Warnings, "\n")
	for _, w := range wantWarnings {
		if !strings.Contains(joined, w) {
			t.Errorf("missing warning %q in:\n%s", w, joined)
		}
	}
	hints := strings.Join(res.Hints, "\n")
	for _, h := range []string{"~/tmp/uses-global", `3 repos of profile "freelance" are in ~/code`} {
		if !strings.Contains(hints, h) {
			t.Errorf("missing hint %q in:\n%s", h, hints)
		}
	}
}

func TestImportIsDeterministic(t *testing.T) {
	home := Fixture(t)
	opts := Options{GlobalFiles: []string{filepath.Join(home, ".gitconfig")}, Roots: []string{home}}
	first, _ := Import(opts)
	a, _ := first.YAML()
	for i := 0; i < 3; i++ {
		again, _ := Import(opts)
		b, _ := again.YAML()
		if !bytes.Equal(a, b) {
			t.Fatalf("import output differs between runs:\n%s\n---\n%s", a, b)
		}
	}
}

func TestGlobalOrderMatters(t *testing.T) {
	home := testutil.Home(t)
	// [user] after the includeIf overrides the fragment's name, as in git.
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), "[includeIf \"gitdir:~/w/\"]\n\tpath = ~/w.inc\n[user]\n\tname = Late Name\n")
	testutil.WriteFile(t, filepath.Join(home, "w.inc"), "[user]\n\tname = Early Name\n\temail = e@corp.example\n")
	res, err := Import(Options{GlobalFiles: []string{filepath.Join(home, ".gitconfig")}})
	if err != nil {
		t.Fatal(err)
	}
	if p := res.Config.Profiles["w"]; p == nil || p.Name != "Late Name" || p.Email != "e@corp.example" {
		t.Errorf("profile w = %+v (order %v)", p, res.Order)
	}
}

func TestParseCond(t *testing.T) {
	t.Setenv("HOME", "/home/k")
	cases := []struct {
		cond  string
		kind  int
		value string
	}{
		{"gitdir:~/work/", condDir, "~/work"},
		{"gitdir/i:~/Work/", condDir, "~/Work"},
		{"gitdir:/home/k/code/**", condDir, "~/code"},
		{"gitdir:/srv/x/.git", condRepoPath, "/srv/x"},
		{"hasconfig:remote.*.url:git@github.com:acme/**", condRemote, "git@github.com:acme/**"},
		{"hasconfig:remote.*.url:https://github.com/me/x", condRepoURL, "https://github.com/me/x"},
		{"onbranch:main", condUnsupported, "onbranch:main"},
		{"gitdir:~/bare", condUnsupported, "gitdir:~/bare"},
	}
	for _, c := range cases {
		kind, v := parseCond(c.cond)
		if kind != c.kind || v != c.value {
			t.Errorf("parseCond(%q) = %d %q, want %d %q", c.cond, kind, v, c.kind, c.value)
		}
	}
}

func TestNaming(t *testing.T) {
	names := map[string]string{
		"kirill@welltory.com":                "welltory",
		"k@mail.company.co":                  "company",
		"john.doe@gmail.com":                 "john.doe",
		"1234+octo@users.noreply.github.com": "octo",
		"weird":                              "weird",
	}
	for email, want := range names {
		if got := emailName(email); got != want {
			t.Errorf("emailName(%q) = %q, want %q", email, got, want)
		}
	}
	files := map[string]string{
		"~/.gitconfig-work":           "work",
		"~/.gitconfig.d/Personal.inc": "personal",
		"/x/gitconfig.client":         "client",
		"/x/acme corp.gitconfig":      "acme-corp",
	}
	for f, want := range files {
		if got := nameFromFile(f); got != want {
			t.Errorf("nameFromFile(%q) = %q, want %q", f, got, want)
		}
	}
	taken := map[string]bool{"work": true, "work-2": true}
	if got := unique("work", taken); got != "work-3" {
		t.Errorf("unique = %q", got)
	}
}

func TestInteractiveNames(t *testing.T) {
	home := testutil.Home(t)
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = A\n\temail = a@a.example\n")
	answers := []string{"bad name!", "mine"}
	res, err := Import(Options{
		GlobalFiles: []string{filepath.Join(home, ".gitconfig")},
		Prompt: func(q, s string) string {
			a := answers[0]
			answers = answers[1:]
			return a
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Order) != 1 || res.Order[0] != "mine" {
		t.Errorf("order = %v", res.Order)
	}
}

func TestGitKrakenParsing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "profile")
	_ = os.WriteFile(p, []byte(`{"profileName":"Home","userName":"K","userEmail":"k@home.example","gpgSign":true,"gpgSigningKey":"ABCD"}`), 0o644)
	id, name, err := parseGitKrakenProfile(p)
	if err != nil || name != "Home" || id.Email != "k@home.example" || id.GPGSign != "true" || id.SigningKey != "ABCD" {
		t.Errorf("parse = %+v %q %v", id, name, err)
	}
	_ = os.WriteFile(p, []byte(`{"profileName":"NoMail","userName":"K"}`), 0o644)
	if _, _, err := parseGitKrakenProfile(p); err == nil {
		t.Error("expected error for profile without email")
	}
}

func TestMerge(t *testing.T) {
	testutil.Home(t)
	existing := `# mine
version: 1
profiles:
  job:            # renamed by hand
    name: W
    email: w@corp.example
    repos:
      - ~/old
rules:
  - profile: job
    dirs: ["~/job"]
`
	tr := true
	res := &Result{
		Order:   []string{"corp", "hobby"},
		Sources: map[string][]string{"corp": {"x"}, "hobby": {"y"}},
		Config: &config.Config{
			Version: 1, StrictIdentity: &tr,
			Profiles: map[string]*config.Profile{
				"corp":  {Name: "W", Email: "W@corp.example", Repos: []string{"~/old/", "~/new"}, Extra: map[string]string{"pull.rebase": "true"}},
				"hobby": {Name: "H", Email: "h@hobby.example"},
			},
			Rules: []config.Rule{{Profile: "corp", Dirs: []string{"~/corp"}}, {Profile: "hobby", Dirs: []string{"~/hobby"}}},
		},
	}
	out, changes, err := Merge([]byte(existing), res)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# mine", "# renamed by hand", "- ~/new", "hobby:", "# source: y", "profile: hobby"} {
		if !strings.Contains(s, want) {
			t.Errorf("merged output lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "~/old/") || strings.Contains(s, "profile: corp") || strings.Contains(s, "corp:\n") {
		t.Errorf("merge duplicated existing data:\n%s", s)
	}
	if !strings.Contains(s, "pull.rebase: \"true\"") {
		t.Errorf("extra from the matched profile was dropped:\n%s", s)
	}
	if strings.Index(s, "profile: hobby") > strings.Index(s, "profile: job") && strings.Contains(s, "profile: job") {
		t.Errorf("imported rule must come before existing rules:\n%s", s)
	}
	if len(changes) != 4 {
		t.Errorf("changes = %q", changes)
	}
}
