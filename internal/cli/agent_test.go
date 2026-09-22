package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/testutil"
)

func runStdin(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	a := &App{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(stdin), Version: "test"}
	code := a.Run(args)
	return result{out.String(), errb.String(), code}
}

func hookPayload(cwd, command string) string {
	b, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": cwd,
		"tool_input": map[string]string{"command": command, "description": "x"},
	})
	return string(b)
}

func TestPreflight(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")

	r := mustRun(t, "preflight", filepath.Join(home, "work", "api"))
	if !strings.Contains(r.stdout, "identity  Pat Work <pat@company.com>  (profile work)") || !strings.Contains(r.stdout, "ok: a commit here would go through") {
		t.Errorf("preflight ok:\n%s", r.stdout)
	}

	r = run(t, "preflight", "--json", filepath.Join(home, "tmp", "unmatched"))
	var rep preflightReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	if r.code != 1 || rep.OK || len(rep.Problems) != 1 || rep.Problems[0].Code != codeNoIdentity || !strings.Contains(rep.Problems[0].Fix, "gitident use <profile>") {
		t.Errorf("preflight unmatched (%d): %+v", r.code, rep)
	}

	// Identity from outside the rules is a guess.
	stray := filepath.Join(home, "tmp", "unmatched")
	testutil.Git(t, stray, "config", "user.email", "pat@home.org")
	r = run(t, "preflight", stray)
	if r.code != 1 || !strings.Contains(r.stdout, "FAIL unmatched") {
		t.Errorf("preflight unmatched email (%d):\n%s", r.code, r.stdout)
	}

	// Signing that would block exits 2.
	signing := strings.Replace(e2eConfig, "    email: pat@company.com\n", "    email: pat@company.com\n    signing_key: ~/.ssh/missing.pub\n    gpg_format: ssh\n    gpgsign: true\n", 1)
	testutil.WriteFile(t, filepath.Join(home, ".config", "gitident", "profiles.yaml"), signing)
	mustRun(t, "sync")
	r = run(t, "preflight", "--json", filepath.Join(home, "work", "api"))
	rep = preflightReport{}
	_ = json.Unmarshal([]byte(r.stdout), &rep)
	if r.code != 2 || rep.Signing == nil || rep.Signing.OK || rep.Problems[0].Code != codeSigningFailed || !strings.Contains(rep.Problems[0].Fix, "ssh-add") {
		t.Errorf("preflight signing (%d):\n%s", r.code, r.stdout)
	}
	if r := run(t, "preflight", "--no-sign", filepath.Join(home, "work", "api")); r.code != 0 {
		t.Errorf("--no-sign (%d):\n%s", r.code, r.stdout)
	}
	if r := run(t, "preflight", home); r.code != 1 || !strings.Contains(r.stdout, "not_a_repo") {
		t.Errorf("outside a repo (%d):\n%s", r.code, r.stdout)
	}
}

func TestAgentHook(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	api := filepath.Join(home, "work", "api")
	cases := []struct {
		cwd, cmd string
		code     int
		stderr   string
	}{
		{api, `git add . && git commit -m "x"`, 0, ""},
		{home, `cd work/api && git commit -m x`, 0, ""},
		{home, `cd tmp/unmatched && git commit -m x`, 2, "no_identity"},
		{home, `git -C tmp/unmatched merge main`, 2, "gitident blocked `git merge` in ~/tmp/unmatched"},
		{api, `git config user.email me@example.com`, 2, "don't set the git identity by hand (`git config user.email`)"},
		{api, `git -c user.name=Bot commit -m x`, 2, "git -c user.name"},
		{api, `GIT_AUTHOR_EMAIL=x@y git commit -m x`, 2, "GIT_AUTHOR_EMAIL"},
		{home, `git -C tmp/unmatched status`, 0, ""},
		{home, `ls -la`, 0, ""},
		{home, `cd /nonexistent && git commit -m x`, 0, ""},
	}
	for _, c := range cases {
		r := runStdin(t, hookPayload(c.cwd, c.cmd), "agent", "hook", "claude")
		if r.code != c.code || !strings.Contains(r.stderr, c.stderr) {
			t.Errorf("%s (in %s): exit %d, stderr:\n%s", c.cmd, c.cwd, r.code, r.stderr)
		}
	}
	// Other tools and garbage input pass through.
	if r := runStdin(t, `{"tool_name":"Edit","tool_input":{}}`, "agent", "hook", "claude"); r.code != 0 {
		t.Errorf("Edit tool: %+v", r)
	}
	if r := runStdin(t, `not json`, "agent", "hook", "claude"); r.code != 0 {
		t.Errorf("bad input: %+v", r)
	}
}

func TestAgentInstall(t *testing.T) {
	home := testutil.Home(t)
	dir := filepath.Join(home, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	const existing = `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-linter"}]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]
  },
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`
	testutil.WriteFile(t, filepath.Join(dir, "settings.json"), existing)

	r := mustRun(t, "agent", "install", "--dry-run")
	if !strings.Contains(r.stdout, "would add the PreToolUse hook") {
		t.Errorf("dry run:\n%s", r.stdout)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(data) != existing {
		t.Error("dry run changed settings")
	}
	r = mustRun(t, "agent", "install", "claude")
	if !strings.Contains(r.stdout, "write ~/claude/skills/gitident/SKILL.md") {
		t.Errorf("install:\n%s", r.stdout)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	s := string(data)
	for _, want := range []string{`"command": "my-linter"`, `"command": "notify"`, `"Bash(ls:*)"`, "agent hook claude", `"timeout": 30`} {
		if !strings.Contains(s, want) {
			t.Errorf("settings lack %s:\n%s", want, s)
		}
	}
	if strings.Index(s, `"model"`) > strings.Index(s, `"hooks"`) || strings.Index(s, `"hooks"`) > strings.Index(s, `"permissions"`) {
		t.Errorf("key order not kept:\n%s", s)
	}
	if r := mustRun(t, "agent", "install"); !strings.Contains(r.stdout, "up to date") {
		t.Errorf("second install:\n%s", r.stdout)
	}
	if r := mustRun(t, "agent", "status"); !strings.Contains(r.stdout, "hook   installed") || !strings.Contains(r.stdout, "skill  installed") {
		t.Errorf("status:\n%s", r.stdout)
	}
	skill, _ := os.ReadFile(filepath.Join(dir, "skills", "gitident", "SKILL.md"))
	if !strings.HasPrefix(string(skill), "---\nname: gitident\ndescription: ") || !strings.Contains(string(skill), "gitident preflight --json") {
		t.Errorf("skill:\n%s", skill)
	}

	mustRun(t, "agent", "uninstall")
	data, _ = os.ReadFile(filepath.Join(dir, "settings.json"))
	var got, want any
	_ = json.Unmarshal(data, &got)
	_ = json.Unmarshal([]byte(existing), &want)
	if !jsonEqual(got, want) {
		t.Errorf("uninstall did not restore settings:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "gitident")); !os.IsNotExist(err) {
		t.Error("skill dir left behind")
	}

	// Fresh install into a project with no settings yet.
	proj := filepath.Join(home, "proj")
	_ = os.MkdirAll(proj, 0o755)
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	_ = os.Chdir(proj)
	mustRun(t, "agent", "install", "--scope", "local")
	data, _ = os.ReadFile(filepath.Join(proj, ".claude", "settings.local.json"))
	if !strings.Contains(string(data), `"matcher": "Bash"`) {
		t.Errorf("local settings:\n%s", data)
	}
	if r := run(t, "agent", "install", "--scope", "team"); r.code != 2 {
		t.Errorf("bad scope: %+v", r)
	}
	if r := mustRun(t, "agent", "instructions"); !strings.HasPrefix(r.stdout, "# Git identity (gitident)") {
		t.Errorf("instructions:\n%s", r.stdout)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
