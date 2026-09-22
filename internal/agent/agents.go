package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leoru/gitident-cli/internal/paths"
)

// Scope is where an integration is installed.
type Scope string

const (
	ScopeUser    Scope = "user"    // the agent's user-level config (all projects)
	ScopeProject Scope = "project" // files in the current project, shared with the team
	ScopeLocal   Scope = "local"   // project files just for you (Claude Code only)
)

// hookShape is how an agent lists hooks in its config file.
type hookShape int

const (
	// shapeGrouped: {"hooks": {"<Event>": [{"matcher": "…", "hooks": [{…}]}]}}
	// (Claude Code, Codex, Gemini CLI, Factory).
	shapeGrouped hookShape = iota
	// shapeFlat: {"hooks": {"<event>": [{…}]}} (Cursor, Windsurf).
	shapeFlat
	// shapeOwnFile: a whole JSON file that belongs to gitident (Copilot).
	shapeOwnFile
)

// HookTarget is where and how an agent's hook is registered.
type HookTarget struct {
	Path    string
	shape   hookShape
	event   string
	matcher string               // shapeGrouped only
	entry   func(cmd string) any // the hook object
	top     map[string]any       // keys the file needs at the top level (e.g. "version")
}

// NotesTarget is where the agent guidance goes.
type NotesTarget struct {
	Path string
	// own: the whole file is gitident's (a skill or rule file); otherwise a
	// marked section is kept in a shared file such as AGENTS.md.
	own     bool
	content func() []byte
}

// Agent describes one supported coding agent.
type Agent struct {
	Name  string // used on the command line and in the hook command
	Title string
	// Home is the agent's user config directory; its existence means the
	// agent is installed.
	Home  func() string
	hook  func(scope Scope, project string) *HookTarget
	notes func(scope Scope, project string) *NotesTarget
	// Note is shown by `agent list`.
	Note string
}

// Detected reports whether the agent seems to be installed.
func (a *Agent) Detected() bool {
	st, err := os.Stat(a.Home())
	return err == nil && st.IsDir()
}

// Targets returns the hook and notes targets for a scope (either may be nil).
func (a *Agent) Targets(scope Scope, project string) (*HookTarget, *NotesTarget, error) {
	switch scope {
	case ScopeUser, ScopeProject:
	case ScopeLocal:
		if a.Name != "claude" {
			return nil, nil, fmt.Errorf("--scope local is only supported for claude (use project)")
		}
	default:
		return nil, nil, fmt.Errorf("unknown scope %q (want user, project or local)", scope)
	}
	return a.hook(scope, project), a.notes(scope, project), nil
}

func envDir(env, fallback string) func() string {
	return func() string {
		if v := os.Getenv(env); v != "" {
			return paths.Expand(v)
		}
		return paths.Expand(fallback)
	}
}

// claudeStyle is the hook entry of agents that copied Claude Code's format.
func claudeStyle(timeout int) func(string) any {
	return func(cmd string) any {
		return map[string]any{"type": "command", "command": cmd, "timeout": timeout}
	}
}

// All lists the supported agents.
func All() []*Agent {
	claudeHome := envDir("CLAUDE_CONFIG_DIR", "~/.claude")
	codexHome := envDir("CODEX_HOME", "~/.codex")
	copilotHome := envDir("COPILOT_HOME", "~/.copilot")
	geminiHome := func() string {
		if v := os.Getenv("GEMINI_CLI_HOME"); v != "" {
			return filepath.Join(paths.Expand(v), ".gemini")
		}
		return paths.Expand("~/.gemini")
	}
	factoryHome := envDir("", "~/.factory")
	cursorHome := envDir("", "~/.cursor")
	windsurfHome := envDir("", "~/.codeium/windsurf")

	agentsMD := func(scope Scope, project, userFile string) *NotesTarget {
		p := filepath.Join(project, "AGENTS.md")
		if scope == ScopeUser {
			if userFile == "" {
				return nil
			}
			p = userFile
		}
		return &NotesTarget{Path: p, content: sectionContent}
	}

	return []*Agent{
		{
			Name: "claude", Title: "Claude Code", Home: claudeHome,
			hook: func(scope Scope, project string) *HookTarget {
				p := filepath.Join(claudeHome(), "settings.json")
				switch scope {
				case ScopeProject:
					p = filepath.Join(project, ".claude", "settings.json")
				case ScopeLocal:
					p = filepath.Join(project, ".claude", "settings.local.json")
				}
				return &HookTarget{Path: p, shape: shapeGrouped, event: "PreToolUse", matcher: "Bash", entry: claudeStyle(30)}
			},
			notes: func(scope Scope, project string) *NotesTarget {
				dir := claudeHome()
				if scope != ScopeUser {
					dir = filepath.Join(project, ".claude")
				}
				return &NotesTarget{Path: filepath.Join(dir, "skills", paths.AppName, "SKILL.md"), own: true, content: claudeSkill}
			},
			Note: "PreToolUse hook + gitident skill",
		},
		{
			Name: "codex", Title: "OpenAI Codex", Home: codexHome,
			hook: func(scope Scope, project string) *HookTarget {
				p := filepath.Join(codexHome(), "hooks.json")
				if scope == ScopeProject {
					p = filepath.Join(project, ".codex", "hooks.json")
				}
				return &HookTarget{Path: p, shape: shapeGrouped, event: "PreToolUse", matcher: "Bash", entry: claudeStyle(30)}
			},
			notes: func(scope Scope, project string) *NotesTarget {
				return agentsMD(scope, project, filepath.Join(codexHome(), "AGENTS.md"))
			},
			Note: "PreToolUse hook + AGENTS.md section",
		},
		{
			Name: "cursor", Title: "Cursor", Home: cursorHome,
			hook: func(scope Scope, project string) *HookTarget {
				p := filepath.Join(cursorHome(), "hooks.json")
				if scope == ScopeProject {
					p = filepath.Join(project, ".cursor", "hooks.json")
				}
				return &HookTarget{Path: p, shape: shapeFlat, event: "beforeShellExecution",
					entry: func(cmd string) any { return map[string]any{"command": cmd, "timeout": 30} },
					top:   map[string]any{"version": 1}}
			},
			notes: func(scope Scope, project string) *NotesTarget {
				if scope == ScopeUser {
					return nil // user rules live in Cursor's settings UI, not in a file
				}
				return &NotesTarget{Path: filepath.Join(project, ".cursor", "rules", paths.AppName+".mdc"), own: true, content: cursorRule}
			},
			Note: "beforeShellExecution hook; project scope adds a rule (user rules are UI-only)",
		},
		{
			Name: "gemini", Title: "Gemini CLI", Home: geminiHome,
			hook: func(scope Scope, project string) *HookTarget {
				p := filepath.Join(geminiHome(), "settings.json")
				if scope == ScopeProject {
					p = filepath.Join(project, ".gemini", "settings.json")
				}
				return &HookTarget{Path: p, shape: shapeGrouped, event: "BeforeTool", matcher: "run_shell_command",
					entry: func(cmd string) any {
						return map[string]any{"name": paths.AppName, "type": "command", "command": cmd, "timeout": 30000}
					}}
			},
			notes: func(scope Scope, project string) *NotesTarget {
				p := filepath.Join(geminiHome(), "GEMINI.md")
				if scope == ScopeProject {
					p = filepath.Join(project, "GEMINI.md")
				}
				return &NotesTarget{Path: p, content: sectionContent}
			},
			Note: "BeforeTool hook + GEMINI.md section",
		},
		{
			Name: "copilot", Title: "GitHub Copilot CLI", Home: copilotHome,
			hook: func(scope Scope, project string) *HookTarget {
				p := filepath.Join(copilotHome(), "hooks", paths.AppName+".json")
				if scope == ScopeProject {
					p = filepath.Join(project, ".github", "hooks", paths.AppName+".json")
				}
				return &HookTarget{Path: p, shape: shapeOwnFile, event: "preToolUse",
					entry: func(cmd string) any {
						return map[string]any{"type": "command", "bash": cmd, "timeoutSec": 30}
					},
					top: map[string]any{"version": 1}}
			},
			notes: func(scope Scope, project string) *NotesTarget {
				p := filepath.Join(copilotHome(), "copilot-instructions.md")
				if scope == ScopeProject {
					p = filepath.Join(project, ".github", "copilot-instructions.md")
				}
				return &NotesTarget{Path: p, content: sectionContent}
			},
			Note: "preToolUse hook + copilot-instructions.md section",
		},
		{
			Name: "factory", Title: "Factory Droid", Home: factoryHome,
			hook: func(scope Scope, project string) *HookTarget {
				p := filepath.Join(factoryHome(), "hooks.json")
				if scope == ScopeProject {
					p = filepath.Join(project, ".factory", "hooks.json")
				}
				return &HookTarget{Path: p, shape: shapeGrouped, event: "PreToolUse", matcher: "Execute", entry: claudeStyle(30)}
			},
			notes: func(scope Scope, project string) *NotesTarget {
				return agentsMD(scope, project, "") // no documented user-level file
			},
			Note: "PreToolUse hook; project scope adds an AGENTS.md section",
		},
		{
			Name: "windsurf", Title: "Windsurf", Home: windsurfHome,
			hook: func(scope Scope, project string) *HookTarget {
				p := filepath.Join(windsurfHome(), "hooks.json")
				if scope == ScopeProject {
					p = filepath.Join(project, ".windsurf", "hooks.json")
				}
				return &HookTarget{Path: p, shape: shapeFlat, event: "pre_run_command",
					entry: func(cmd string) any { return map[string]any{"command": cmd, "show_output": true} }}
			},
			notes: func(scope Scope, project string) *NotesTarget {
				return agentsMD(scope, project, "")
			},
			Note: "pre_run_command hook; project scope adds an AGENTS.md section",
		},
	}
}

// Find returns the agent with the given name.
func Find(name string) (*Agent, bool) {
	for _, a := range All() {
		if a.Name == strings.ToLower(name) {
			return a, true
		}
	}
	return nil, false
}

// Names lists the supported agent names.
func Names() []string {
	var out []string
	for _, a := range All() {
		out = append(out, a.Name)
	}
	return out
}
