package agent

import (
	"encoding/json"
	"strings"
)

// Call is a shell command an agent is about to run.
type Call struct {
	Command string
	Cwd     string
}

// ParsePayload extracts the shell command from a hook payload. It accepts
// every supported agent's shape; ok is false for other tools or bad input.
func ParsePayload(data []byte) (Call, bool) {
	var p struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		Cwd       string          `json:"cwd"`
		Command   string          `json:"command"` // Cursor
		ToolInfo  struct {        // Windsurf
			CommandLine string `json:"command_line"`
			Cwd         string `json:"cwd"`
		} `json:"tool_info"`
		ToolNameCamel string          `json:"toolName"` // Copilot
		ToolArgs      json.RawMessage `json:"toolArgs"`
	}
	if json.Unmarshal(data, &p) != nil {
		return Call{}, false
	}
	c := Call{Cwd: p.Cwd}
	switch {
	case p.ToolInput != nil:
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(p.ToolInput, &in) != nil {
			return Call{}, false
		}
		switch p.ToolName {
		case "Bash", "run_shell_command", "Execute", "shell", "":
			c.Command = in.Command
		}
	case p.Command != "":
		c.Command = p.Command
	case p.ToolInfo.CommandLine != "":
		c.Command = p.ToolInfo.CommandLine
		if c.Cwd == "" {
			c.Cwd = p.ToolInfo.Cwd
		}
	case p.ToolArgs != nil:
		if name := strings.ToLower(p.ToolNameCamel); name != "bash" && name != "shell" && name != "" {
			return Call{}, false
		}
		args := p.ToolArgs
		// Copilot sends toolArgs as a JSON-encoded string in some versions.
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = json.RawMessage(s)
		}
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &in) != nil {
			return Call{}, false
		}
		c.Command = in.Command
	}
	return c, c.Command != ""
}

// Deny renders a blocking answer for agent name: the bytes for stdout and the
// exit code. Agents whose documented contract is "exit 2, reason on stderr"
// get no stdout; the caller prints the reason to stderr for all of them.
func Deny(name, reason string) ([]byte, int) {
	switch name {
	case "cursor":
		b, _ := json.Marshal(map[string]string{"permission": "deny", "user_message": reason, "agent_message": reason})
		return append(b, '\n'), 0
	case "copilot":
		b, _ := json.Marshal(map[string]string{"permissionDecision": "deny", "permissionDecisionReason": reason})
		return append(b, '\n'), 0
	}
	return nil, 2
}
