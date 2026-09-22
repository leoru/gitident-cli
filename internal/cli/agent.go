package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/leoru/gitident-cli/internal/agent"
	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/paths"
)

const agentUsage = `usage: gitident agent <install|uninstall|status> [claude] [--scope user|project|local] [--dry-run]
       gitident agent instructions

Set up AI coding agents to respect your git identities.

  install        for Claude Code (the only agent supported so far):
                 - a PreToolUse hook that runs before every shell command. It
                   blocks commits, merges, pulls, rebases and tags that
                   "gitident preflight" says would use a missing or wrong
                   identity or would hang on a signing passphrase, and blocks
                   attempts to set user.name / user.email by hand; Claude sees
                   the reason and the fix
                 - a "gitident" skill telling Claude to run preflight before
                   committing and how to fix what it reports
  uninstall      remove both again
  status         show what is installed
  instructions   print the agent guidance, for AGENTS.md, CLAUDE.md or other
                 agents' rule files

flags:
  --scope    user     ~/.claude/settings.json and ~/.claude/skills (default)
             project  .claude/ in the current directory, shared with the team
             local    .claude/settings.local.json, just you
  --dry-run  show what would change, write nothing
`

func (a *App) cmdAgent(args []string) error {
	if len(args) == 0 {
		return usageError("expected install, uninstall, status or instructions")
	}
	action := args[0]
	fs := a.newFlags("agent", agentUsage)
	scope := fs.String("scope", "user", "")
	dryRun := fs.Bool("dry-run", false, "")
	pos, err := parseFlags(fs, args[1:])
	if err != nil {
		return err
	}
	switch action {
	case "instructions":
		fmt.Fprint(a.Stdout, agent.Instructions())
		return nil
	case "hook":
		return a.agentHook()
	case "install", "uninstall", "status":
	default:
		return usageError("unknown action %q", action)
	}
	if len(pos) > 1 || (len(pos) == 1 && pos[0] != "claude") {
		return usageError("only `claude` is supported")
	}
	cwd, _ := os.Getwd()
	files, err := agent.ClaudePaths(agent.Scope(*scope), cwd)
	if err != nil {
		return usageError("%v", err)
	}
	switch action {
	case "status":
		return a.agentStatus(files)
	case "install":
		return a.agentInstall(files, *dryRun)
	default:
		return a.agentUninstall(files, *dryRun)
	}
}

// hookCommand is how Claude Code should call gitident: by name when it is on
// PATH (survives upgrades), else by absolute path.
func hookCommand() string {
	if _, err := exec.LookPath(paths.AppName); err == nil {
		return paths.AppName + " " + agent.HookArgs
	}
	if exe, err := os.Executable(); err == nil {
		return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "' " + agent.HookArgs
	}
	return paths.AppName + " " + agent.HookArgs
}

func (a *App) agentInstall(files agent.ClaudeFiles, dryRun bool) error {
	would := ""
	if dryRun {
		would = "would "
	}
	old, err := fsutil.ReadFileIfExists(files.Settings)
	if err != nil {
		return err
	}
	updated, changed, err := agent.EditSettings(old, hookCommand(), true)
	if err != nil {
		return fmt.Errorf("%s: %w", paths.Contract(files.Settings), err)
	}
	if changed {
		a.printf("%sadd the PreToolUse hook to %s\n", would, paths.Contract(files.Settings))
		if !dryRun {
			if err := fsutil.WriteFileAtomic(files.Settings, updated, 0o644); err != nil {
				return err
			}
		}
	}
	skillChanged, err := agent.WriteSkill(files.Skill, dryRun)
	if err != nil {
		return err
	}
	if skillChanged {
		a.printf("%swrite %s\n", would, paths.Contract(files.Skill))
	}
	if !changed && !skillChanged {
		a.printf("Claude Code integration is up to date (%s)\n", paths.Contract(files.Settings))
	} else if !dryRun {
		a.printf("done — new Claude Code sessions pick it up\n")
	}
	return nil
}

func (a *App) agentUninstall(files agent.ClaudeFiles, dryRun bool) error {
	would := ""
	if dryRun {
		would = "would "
	}
	old, err := fsutil.ReadFileIfExists(files.Settings)
	if err != nil {
		return err
	}
	removed := false
	if old != nil {
		updated, changed, err := agent.EditSettings(old, "", false)
		if err != nil {
			return fmt.Errorf("%s: %w", paths.Contract(files.Settings), err)
		}
		if changed {
			removed = true
			a.printf("%sremove the hook from %s\n", would, paths.Contract(files.Settings))
			if !dryRun {
				if err := fsutil.WriteFileAtomic(files.Settings, updated, 0o644); err != nil {
					return err
				}
			}
		}
	}
	ok, err := agent.RemoveSkill(files.Skill, dryRun)
	if err != nil {
		return err
	}
	if ok {
		removed = true
		a.printf("%sremove %s\n", would, paths.Contract(files.Skill))
	}
	if !removed {
		a.printf("nothing to remove\n")
	}
	return nil
}

func (a *App) agentStatus(files agent.ClaudeFiles) error {
	data, _ := os.ReadFile(files.Settings)
	hook := "not installed"
	if agent.HasHook(data) {
		hook = "installed"
	}
	skill := "not installed"
	if _, err := os.Stat(files.Skill); err == nil {
		skill = "installed"
	}
	a.printf("hook   %s  (%s)\nskill  %s  (%s)\n", hook, paths.Contract(files.Settings), skill, paths.Contract(files.Skill))
	return nil
}

// hookInput is the part of Claude Code's PreToolUse payload gitident reads.
type hookInput struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// agentHook runs as a Claude Code PreToolUse hook. Exit status 2 blocks the
// tool call and shows stderr to Claude; anything unexpected lets it through.
func (a *App) agentHook() error {
	data, err := io.ReadAll(a.Stdin)
	if err != nil {
		return nil
	}
	var in hookInput
	if json.Unmarshal(data, &in) != nil || in.ToolName != "Bash" || in.ToolInput.Command == "" {
		return nil
	}
	cwd := in.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	for _, g := range agent.ParseGit(in.ToolInput.Command, cwd) {
		if how := g.SetsIdentity(); how != "" {
			fmt.Fprintf(a.Stderr, "gitident: don't set the git identity by hand (%s). gitident manages it per repository: run `gitident preflight` in %s; if no profile applies, ask the user which profile the repository belongs to and run `gitident use <profile>` there.\n",
				how, paths.Contract(g.Dir))
			return &exitError{code: 2}
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
		var problems []preflightProblem
		for _, p := range rep.Problems {
			if p.Code != codeNotRepo && p.Code != codeNoConfig {
				problems = append(problems, p)
			}
		}
		if len(problems) == 0 {
			continue
		}
		fmt.Fprintf(a.Stderr, "gitident blocked `git %s` in %s:\n", g.Sub, paths.Contract(g.Dir))
		for _, p := range problems {
			fmt.Fprintf(a.Stderr, "- %s: %s\n  fix: %s\n", p.Code, p.Message, p.Fix)
		}
		fmt.Fprintf(a.Stderr, "Run `gitident preflight` there after fixing.\n")
		return &exitError{code: 2}
	}
	return nil
}
