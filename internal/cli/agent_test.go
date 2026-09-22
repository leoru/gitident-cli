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

func TestAgentInstallClaude(t *testing.T) {
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
	if !strings.Contains(r.stdout, "claude    would add the hook to ~/claude/settings.json") {
		t.Errorf("dry run:\n%s", r.stdout)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(data) != existing {
		t.Error("dry run changed settings")
	}
	r = mustRun(t, "agent", "install") // only claude is detected here
	if !strings.Contains(r.stdout, "write ~/claude/skills/gitident/SKILL.md") || strings.Contains(r.stdout, "codex") {
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
	if r := mustRun(t, "agent", "install", "claude"); !strings.Contains(r.stdout, "claude    up to date") {
		t.Errorf("second install:\n%s", r.stdout)
	}
	if r := mustRun(t, "agent", "status", "claude"); strings.Count(r.stdout, "installed") != 2 {
		t.Errorf("status:\n%s", r.stdout)
	}
	skill, _ := os.ReadFile(filepath.Join(dir, "skills", "gitident", "SKILL.md"))
	if !strings.HasPrefix(string(skill), "---\nname: gitident\ndescription: ") || !strings.Contains(string(skill), "gitident preflight --json") {
		t.Errorf("skill:\n%s", skill)
	}

	mustRun(t, "agent", "uninstall", "claude")
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
	if r := run(t, "agent", "install", "--scope", "team", "claude"); r.code != 2 {
		t.Errorf("bad scope: %+v", r)
	}
	if r := run(t, "agent", "install", "nope"); r.code != 2 || !strings.Contains(r.stderr, "supported: claude, codex") {
		t.Errorf("unknown agent: %+v", r)
	}
	if r := run(t, "agent", "install", "--scope", "local", "codex"); r.code != 2 {
		t.Errorf("local scope for codex: %+v", r)
	}
	if r := mustRun(t, "agent", "instructions"); !strings.HasPrefix(r.stdout, "# Git identity (gitident)") {
		t.Errorf("instructions:\n%s", r.stdout)
	}
}

func TestAgentInstallAll(t *testing.T) {
	home := testutil.Home(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("COPILOT_HOME", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	for _, d := range []string{".claude", ".codex", ".cursor", ".gemini", ".copilot", ".factory", ".codeium/windsurf"} {
		_ = os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	// Existing configs of other tools must survive.
	testutil.WriteFile(t, filepath.Join(home, ".codex", "AGENTS.md"), "# My rules\n\nBe terse.\n")
	testutil.WriteFile(t, filepath.Join(home, ".cursor", "hooks.json"),
		`{"version":1,"hooks":{"beforeShellExecution":[{"command":"other-hook.sh"}],"stop":[{"command":"x"}]}}`)
	testutil.WriteFile(t, filepath.Join(home, ".gemini", "settings.json"),
		`{"mcpServers":{"a":{"command":"a"}},"hooks":{"AfterTool":[{"hooks":[{"type":"command","command":"after.sh"}]}]}}`)

	r := mustRun(t, "agent", "list")
	if strings.Count(r.stdout, " found ") != 7 {
		t.Errorf("list:\n%s", r.stdout)
	}
	mustRun(t, "agent", "install")
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		return string(data)
	}
	checks := map[string][]string{
		".codex/hooks.json":                {`"PreToolUse"`, `"matcher": "Bash"`, "agent hook codex"},
		".codex/AGENTS.md":                 {"# My rules", "Be terse.", "<!-- >>> gitident", "gitident preflight --json", "<!-- <<< gitident <<< -->"},
		".cursor/hooks.json":               {"other-hook.sh", `"stop"`, "agent hook cursor", `"version": 1`},
		".gemini/settings.json":            {`"mcpServers"`, "after.sh", `"BeforeTool"`, `"matcher": "run_shell_command"`, "agent hook gemini"},
		".gemini/GEMINI.md":                {"gitident preflight --json"},
		".copilot/hooks/gitident.json":     {`"preToolUse"`, `"bash": "gitident agent hook copilot"`, `"version": 1`},
		".copilot/copilot-instructions.md": {"gitident preflight --json"},
		".factory/hooks.json":              {`"matcher": "Execute"`, "agent hook factory"},
		".codeium/windsurf/hooks.json":     {`"pre_run_command"`, "agent hook windsurf", `"show_output": true`},
		".claude/skills/gitident/SKILL.md": {"name: gitident"},
	}
	for rel, wants := range checks {
		got := read(rel)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", rel, w, got)
			}
		}
	}
	if r := mustRun(t, "agent", "install"); strings.Count(r.stdout, "up to date") != 7 {
		t.Errorf("second install should change nothing:\n%s", r.stdout)
	}
	if r := mustRun(t, "agent", "status"); strings.Count(r.stdout, "installed") != 11 {
		t.Errorf("status:\n%s", r.stdout)
	}

	mustRun(t, "agent", "uninstall")
	if got := read(".codex/AGENTS.md"); got != "# My rules\n\nBe terse.\n" {
		t.Errorf("AGENTS.md after uninstall: %q", got)
	}
	if got := read(".cursor/hooks.json"); strings.Contains(got, "gitident") || !strings.Contains(got, "other-hook.sh") {
		t.Errorf("cursor hooks after uninstall:\n%s", got)
	}
	if got := read(".gemini/settings.json"); strings.Contains(got, "BeforeTool") || !strings.Contains(got, "after.sh") {
		t.Errorf("gemini settings after uninstall:\n%s", got)
	}
	for _, rel := range []string{".codex/hooks.json", ".copilot/hooks/gitident.json", ".gemini/GEMINI.md", ".copilot/copilot-instructions.md"} {
		if data, err := os.ReadFile(filepath.Join(home, rel)); err == nil && strings.Contains(string(data), "gitident") {
			t.Errorf("%s still mentions gitident:\n%s", rel, data)
		}
	}

	// Project scope writes shared project files.
	proj := filepath.Join(home, "proj")
	_ = os.MkdirAll(proj, 0o755)
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	_ = os.Chdir(proj)
	mustRun(t, "agent", "install", "--scope", "project", "cursor", "codex", "factory")
	for _, rel := range []string{"proj/.cursor/hooks.json", "proj/.cursor/rules/gitident.mdc", "proj/.codex/hooks.json", "proj/AGENTS.md", "proj/.factory/hooks.json"} {
		if !strings.Contains(read(rel), "gitident") {
			t.Errorf("%s not written", rel)
		}
	}
	if n := strings.Count(read("proj/AGENTS.md"), "<!-- >>> gitident"); n != 1 {
		t.Errorf("AGENTS.md has %d gitident sections", n)
	}
	mustRun(t, "agent", "install", "--scope", "local")
	if !strings.Contains(read("proj/.claude/settings.local.json"), `"matcher": "Bash"`) {
		t.Error("local scope not written")
	}
}

func TestAgentHookFormats(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	bad := filepath.Join(home, "tmp", "unmatched")
	payloads := map[string]string{
		"codex":    hookPayload(bad, "git commit -m x"),
		"factory":  `{"tool_name":"Execute","cwd":"` + bad + `","tool_input":{"command":"git commit -m x"}}`,
		"gemini":   `{"tool_name":"run_shell_command","cwd":"` + bad + `","tool_input":{"command":"git commit -m x"}}`,
		"cursor":   `{"command":"git commit -m x","cwd":"` + bad + `","sandbox":false}`,
		"windsurf": `{"agent_action_name":"pre_run_command","tool_info":{"command_line":"git commit -m x","cwd":"` + bad + `"}}`,
		"copilot":  `{"toolName":"bash","cwd":"` + bad + `","toolArgs":"{\"command\":\"git commit -m x\"}"}`,
	}
	for name, p := range payloads {
		r := runStdin(t, p, "agent", "hook", name)
		if !strings.Contains(r.stderr, "no_identity") {
			t.Errorf("%s: not blocked (exit %d)\nstdout: %s\nstderr: %s", name, r.code, r.stdout, r.stderr)
			continue
		}
		switch name {
		case "cursor":
			if r.code != 0 || !strings.Contains(r.stdout, `"permission":"deny"`) || !strings.Contains(r.stdout, `"agent_message":"gitident blocked`) {
				t.Errorf("cursor deny: %+v", r)
			}
		case "copilot":
			if r.code != 0 || !strings.Contains(r.stdout, `"permissionDecision":"deny"`) {
				t.Errorf("copilot deny: %+v", r)
			}
		default:
			if r.code != 2 || r.stdout != "" {
				t.Errorf("%s deny: %+v", name, r)
			}
		}
	}
	// Allowed commands print nothing, for every agent.
	for _, name := range []string{"cursor", "copilot", "gemini"} {
		ok := strings.ReplaceAll(payloads[name], "git commit -m x", "git status")
		if r := runStdin(t, ok, "agent", "hook", name); r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Errorf("%s allow: %+v", name, r)
		}
	}
	// Copilot's other tools pass.
	if r := runStdin(t, `{"toolName":"edit","toolArgs":{"command":"git commit"}}`, "agent", "hook", "copilot"); r.code != 0 || r.stdout != "" {
		t.Errorf("copilot edit tool: %+v", r)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
