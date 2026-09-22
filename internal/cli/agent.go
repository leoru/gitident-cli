package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/leoru/gitident-cli/internal/agent"
	"github.com/leoru/gitident-cli/internal/paths"
)

const agentUsage = `usage: gitident agent install   [agent...] [--scope user|project|local] [--dry-run]
       gitident agent uninstall [agent...] [--scope user|project|local] [--dry-run]
       gitident agent status    [agent...] [--scope user|project|local]
       gitident agent list
       gitident agent instructions

Set up AI coding agents to respect your git identities. For each agent:

  - a hook that runs before every shell command the agent executes. It blocks
    commits, merges, pulls, rebases and annotated tags that "gitident
    preflight" says would use a missing or wrong identity or would hang on a
    signing passphrase, and blocks setting user.name / user.email by hand. The
    agent sees the reason and the fix.
  - guidance to run preflight before committing: a skill, a rule file or a
    marked section in the agent's instructions file (AGENTS.md, GEMINI.md, …)

Without agent names, install / uninstall / status act on every supported
agent found on this machine. "gitident agent list" shows what each gets.
"gitident agent instructions" prints the guidance for any other agent.

Supported: ` + "claude, codex, cursor, gemini, copilot, factory, windsurf" + `

flags:
  --scope    user     the agents' user-level config, for all projects (default)
             project  files in the current directory, shared with the team
             local    .claude/settings.local.json, just you (claude only)
  --dry-run  show what would change, write nothing
`

func (a *App) cmdAgent(args []string) error {
	if len(args) == 0 {
		return usageError("expected install, uninstall, status, list or instructions")
	}
	action := args[0]
	fs := a.newFlags("agent", agentUsage)
	scope := fs.String("scope", "user", "")
	dryRun := fs.Bool("dry-run", false, "")
	names, err := parseFlags(fs, args[1:])
	if err != nil {
		return err
	}
	switch action {
	case "instructions":
		fmt.Fprint(a.Stdout, agent.Instructions())
		return nil
	case "hook":
		name := "claude"
		if len(names) > 0 {
			name = names[0]
		}
		return a.agentHook(name)
	case "list":
		for _, ag := range agent.All() {
			found := "not found"
			if ag.Detected() {
				found = "found"
			}
			a.printf("%-9s %-19s %-10s %s\n", ag.Name, ag.Title, found, ag.Note)
		}
		return nil
	case "install", "uninstall", "status":
	default:
		return usageError("unknown action %q", action)
	}

	var agents []*agent.Agent
	explicit := len(names) > 0
	if explicit {
		for _, n := range names {
			ag, ok := agent.Find(n)
			if !ok {
				return usageError("unknown agent %q (supported: %s)", n, strings.Join(agent.Names(), ", "))
			}
			agents = append(agents, ag)
		}
	} else {
		for _, ag := range agent.All() {
			if ag.Detected() {
				agents = append(agents, ag)
			}
		}
		if len(agents) == 0 {
			a.printf("no supported agents found (supported: %s); name one to install anyway\n", strings.Join(agent.Names(), ", "))
			return nil
		}
		if agent.Scope(*scope) == agent.ScopeLocal {
			agents = []*agent.Agent{mustAgent("claude")}
		}
	}
	cwd, _ := os.Getwd()
	failed := 0
	for _, ag := range agents {
		hook, notes, err := ag.Targets(agent.Scope(*scope), cwd)
		if err != nil {
			return usageError("%s: %v", ag.Name, err)
		}
		switch action {
		case "status":
			a.agentStatus(ag, hook, notes)
		default:
			if err := a.agentApply(ag, hook, notes, action == "install", *dryRun); err != nil {
				a.warn("%s: %v", ag.Name, err)
				failed++
			}
		}
	}
	if failed > 0 {
		return errProblems
	}
	return nil
}

func mustAgent(name string) *agent.Agent {
	ag, _ := agent.Find(name)
	return ag
}

// hookCommand is how an agent should call gitident: by name when it is on
// PATH (survives upgrades), else by absolute path.
func hookCommand(name string) string {
	bin := paths.AppName
	if _, err := exec.LookPath(paths.AppName); err != nil {
		if exe, err := os.Executable(); err == nil {
			bin = "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
		}
	}
	return bin + " " + agent.HookArgs + " " + name
}

func (a *App) agentApply(ag *agent.Agent, hook *agent.HookTarget, notes *agent.NotesTarget, install, dryRun bool) error {
	would := ""
	if dryRun {
		would = "would "
	}
	var did []string
	if hook != nil {
		cmd := ""
		if install {
			cmd = hookCommand(ag.Name)
		}
		changed, err := hook.ApplyHook(cmd, dryRun)
		if err != nil {
			return err
		}
		if changed {
			verb := "add the hook to"
			if !install {
				verb = "remove the hook from"
			}
			did = append(did, fmt.Sprintf("%s%s %s", would, verb, paths.Contract(hook.Path)))
		}
	}
	if notes != nil {
		changed, err := notes.ApplyNotes(install, dryRun)
		if err != nil {
			return err
		}
		if changed {
			verb := "write"
			switch {
			case !install:
				verb = "remove gitident's guidance from"
			case !notes.Own():
				verb = "add gitident's guidance to"
			}
			did = append(did, fmt.Sprintf("%s%s %s", would, verb, paths.Contract(notes.Path)))
		}
	}
	switch {
	case len(did) > 0:
		a.printf("%-9s %s\n", ag.Name, strings.Join(did, "\n          "))
	case install:
		a.printf("%-9s up to date\n", ag.Name)
	default:
		a.printf("%-9s nothing to remove\n", ag.Name)
	}
	return nil
}

func (a *App) agentStatus(ag *agent.Agent, hook *agent.HookTarget, notes *agent.NotesTarget) {
	state := func(ok bool) string {
		if ok {
			return "installed"
		}
		return "-"
	}
	h, n := "n/a", "n/a"
	if hook != nil {
		h = state(hook.Installed()) + " (" + paths.Contract(hook.Path) + ")"
	}
	if notes != nil {
		n = state(notes.Installed()) + " (" + paths.Contract(notes.Path) + ")"
	}
	a.printf("%-9s hook %s\n          guidance %s\n", ag.Name, h, n)
}

// agentHook runs as an agent's pre-shell-command hook. It blocks git commands
// that would commit with a missing or wrong identity; anything unexpected lets
// the command through.
func (a *App) agentHook(name string) error {
	data, err := io.ReadAll(a.Stdin)
	if err != nil {
		return nil
	}
	call, ok := agent.ParsePayload(data)
	if !ok {
		return nil
	}
	cwd := call.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	reason := blockReason(call.Command, cwd)
	if reason == "" {
		return nil
	}
	fmt.Fprint(a.Stderr, reason)
	out, code := agent.Deny(name, strings.TrimSpace(reason))
	a.Stdout.Write(out)
	if code == 0 {
		return nil
	}
	return &exitError{code: code}
}

// blockReason explains why command must not run, or returns "".
func blockReason(command, cwd string) string {
	for _, g := range agent.ParseGit(command, cwd) {
		if how := g.SetsIdentity(); how != "" {
			return fmt.Sprintf("gitident: don't set the git identity by hand (%s). gitident manages it per repository: run `gitident preflight` in %s; if no profile applies, ask the user which profile the repository belongs to and run `gitident use <profile>` there.\n",
				how, paths.Contract(g.Dir))
		}
		if !g.MakesCommit() {
			continue
		}
		if st, err := os.Stat(g.Dir); err != nil || !st.IsDir() {
			continue
		}
		rep, err := preflight(g.Dir, !g.SkipsSigning())
		if err != nil {
			continue
		}
		var b strings.Builder
		for _, p := range rep.Problems {
			if p.Code != codeNotRepo && p.Code != codeNoConfig {
				fmt.Fprintf(&b, "- %s: %s\n  fix: %s\n", p.Code, p.Message, p.Fix)
			}
		}
		if b.Len() > 0 {
			return fmt.Sprintf("gitident blocked `git %s` in %s:\n%sRun `gitident preflight` there after fixing.\n", g.Sub, paths.Contract(g.Dir), b.String())
		}
	}
	return ""
}
