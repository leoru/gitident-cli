package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sample = `version: 1
strict_identity: true            # user.useConfigOnly (default true)

profiles:
  work:
    name: Kirill
    email: kirill@company.com
    signing_key: ABCDEF1234567890 # GPG key id, or path to .pub for SSH signing
    gpg_format: openpgp           # or ssh
    gpgsign: true
    ssh_key: ~/.ssh/id_work       # → core.sshCommand with IdentitiesOnly=yes
    extra:                        # free-form "section.key: value"
      pull.rebase: "true"
      push.autoSetupRemote: true
    repos:                        # explicit; always wins over rules
      - ~/misc/legacy-thing
      - git@github.com:company/infra.git

rules:                            # later rule overrides earlier
  - profile: work
    dirs: ["~/work"]
    remotes: ["git@github.com:company/**", "https://gitlab.company.com/**"]
`

func TestParseSample(t *testing.T) {
	cfg, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Profiles["work"]
	if w == nil || w.Email != "kirill@company.com" || w.GPGSign == nil || !*w.GPGSign {
		t.Fatalf("unexpected profile: %+v", w)
	}
	if got := w.Extra["push.autoSetupRemote"]; got != "true" {
		t.Errorf("non-string extra value should decode as its text, got %q", got)
	}
	if !cfg.Strict() {
		t.Error("strict should be true")
	}
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Remotes) != 2 {
		t.Errorf("rules: %+v", cfg.Rules)
	}
	// Round trip.
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, again) {
		t.Errorf("round trip mismatch:\n%s", out)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte("version: 1\nprofiles:\n  a:\n    name: A\n    emial: a@b.c\n"))
	if err == nil || !strings.Contains(err.Error(), "emial") {
		t.Errorf("expected unknown field error, got %v", err)
	}
}

func TestStrictDefault(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nprofiles: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Strict() {
		t.Error("strict must default to true")
	}
}

func TestValidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	existingKey := filepath.Join(home, "id_ok")
	if err := os.WriteFile(existingKey, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	base := func() *Config {
		return &Config{
			Version: 1,
			Profiles: map[string]*Profile{
				"work":     {Name: "W", Email: "w@company.com"},
				"personal": {Name: "P", Email: "p@home.org"},
			},
		}
	}
	cases := []struct {
		name   string
		mutate func(c *Config)
		errs   []string
		warns  []string
	}{
		{name: "valid", mutate: func(c *Config) {}},
		{name: "missing version", mutate: func(c *Config) { c.Version = 0 }, errs: []string{"missing `version"}},
		{name: "unknown version", mutate: func(c *Config) { c.Version = 7 }, errs: []string{"unsupported version 7"}},
		{name: "no profiles", mutate: func(c *Config) { c.Profiles = nil }, errs: []string{"no profiles"}},
		{name: "missing name", mutate: func(c *Config) { c.Profiles["work"].Name = "" }, errs: []string{`"work": ` + "`name` is required"}},
		{name: "missing email", mutate: func(c *Config) { c.Profiles["work"].Email = " " }, errs: []string{"`email` is required"}},
		{name: "bad profile name", mutate: func(c *Config) { c.Profiles["a/b"] = &Profile{Name: "x", Email: "y"} }, errs: []string{"may only contain"}},
		{name: "unknown gpg_format", mutate: func(c *Config) { c.Profiles["work"].GPGFormat = "pgp" }, errs: []string{`unknown gpg_format "pgp"`}},
		{name: "extra without dot", mutate: func(c *Config) { c.Profiles["work"].Extra = map[string]string{"rebase": "true"} }, errs: []string{`extra key "rebase"`}},
		{name: "extra trailing dot", mutate: func(c *Config) { c.Profiles["work"].Extra = map[string]string{"pull.": "true"} }, errs: []string{`extra key "pull."`}},
		{name: "extra with subsection ok", mutate: func(c *Config) {
			c.Profiles["work"].Extra = map[string]string{"url.git@github.com:.insteadOf": "https://github.com/"}
		}},
		{name: "rule unknown profile", mutate: func(c *Config) { c.Rules = []Rule{{Profile: "nope", Dirs: []string{"~/x"}}} }, errs: []string{`rule 1: unknown profile "nope"`}},
		{name: "rule without targets", mutate: func(c *Config) { c.Rules = []Rule{{Profile: "work"}} }, errs: []string{"rule 1: needs at least one"}},
		{name: "rule relative dir", mutate: func(c *Config) { c.Rules = []Rule{{Profile: "work", Dirs: []string{"work"}}} }, errs: []string{"must be absolute"}},
		{name: "repo neither path nor url", mutate: func(c *Config) { c.Profiles["work"].Repos = []string{"github.com/a/b"} }, errs: []string{"neither a path"}},
		{name: "repo path in two profiles", mutate: func(c *Config) {
			c.Profiles["work"].Repos = []string{"~/code/x"}
			c.Profiles["personal"].Repos = []string{home + "/code/x/"}
		}, errs: []string{"listed under both"}},
		{name: "repo url in two profiles", mutate: func(c *Config) {
			c.Profiles["work"].Repos = []string{"git@GitHub.com:a/b.git"}
			c.Profiles["personal"].Repos = []string{"git@github.com:a/b"}
		}, errs: []string{"listed under both"}},
		{name: "repo relative path", mutate: func(c *Config) { c.Profiles["work"].Repos = []string{"./x"} }, errs: []string{"must be absolute"}},
		{name: "repo listed twice in one profile", mutate: func(c *Config) { c.Profiles["work"].Repos = []string{"~/a", "~/a/"} }, warns: []string{"listed twice"}},
		{name: "repo under other profile dir rule", mutate: func(c *Config) {
			c.Profiles["personal"].Repos = []string{"~/work/side-project"}
			c.Rules = []Rule{{Profile: "work", Dirs: []string{"~/work"}}}
		}, warns: []string{"inside ~/work from rule 1"}},
		{name: "repo under own dir rule is fine", mutate: func(c *Config) {
			c.Profiles["work"].Repos = []string{"~/work/x"}
			c.Rules = []Rule{{Profile: "work", Dirs: []string{"~/work"}}}
		}},
		{name: "missing ssh key", mutate: func(c *Config) { c.Profiles["work"].SSHKey = "~/nope" }, warns: []string{"ssh_key ~/nope does not exist"}},
		{name: "existing ssh key", mutate: func(c *Config) { c.Profiles["work"].SSHKey = "~/id_ok" }},
		{name: "missing ssh signing key", mutate: func(c *Config) {
			c.Profiles["work"].GPGFormat = "ssh"
			c.Profiles["work"].SigningKey = "~/.ssh/nope.pub"
		}, warns: []string{"signing_key ~/.ssh/nope.pub does not exist"}},
		{name: "literal ssh signing key", mutate: func(c *Config) {
			c.Profiles["work"].GPGFormat = "ssh"
			c.Profiles["work"].SigningKey = "key::ssh-ed25519 AAAA"
		}},
		{name: "gpgsign without key", mutate: func(c *Config) { t := true; c.Profiles["work"].GPGSign = &t }, warns: []string{"no signing_key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(c)
			r := c.Validate()
			assertMessages(t, "error", r.Errors, tc.errs)
			assertMessages(t, "warning", r.Warnings, tc.warns)
		})
	}
}

func assertMessages(t *testing.T, kind string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%ss: got %q, want %d matching %q", kind, got, len(want), want)
		return
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("%s %d: %q does not contain %q", kind, i, got[i], want[i])
		}
	}
}

func TestDocumentEdits(t *testing.T) {
	src := `# my identities
version: 1
profiles:
  # the day job
  work:
    name: W # inline
    email: w@company.com
  personal:
    name: P
    email: p@home.org
    repos:
      - ~/code/shared
`
	doc, err := ParseDocument([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := doc.AddRepo("work", "~/code/shared"); err != nil || !changed {
		t.Fatalf("AddRepo: %v %v", changed, err)
	}
	if changed, _ := doc.AddRepo("work", "~/code/shared/"); changed {
		t.Error("equivalent path should not be added twice")
	}
	if removed := doc.RemoveRepo("~/code/shared", "work"); !reflect.DeepEqual(removed, []string{"personal"}) {
		t.Errorf("RemoveRepo = %v", removed)
	}
	if _, err := doc.AddRepo("missing", "~/x"); err == nil {
		t.Error("expected unknown profile error")
	}
	if err := doc.AddRule(Rule{Profile: "work", Dirs: []string{"~/work"}}); err != nil {
		t.Fatal(err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# my identities", "# the day job", "# inline", "- ~/code/shared", "rules:", "- profile: work"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	cfg, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles["work"].Repos) != 1 || len(cfg.Profiles["personal"].Repos) != 0 {
		t.Errorf("repos after edit: %+v / %+v", cfg.Profiles["work"].Repos, cfg.Profiles["personal"].Repos)
	}
}
