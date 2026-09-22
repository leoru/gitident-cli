// Package agent integrates gitident with AI coding agents: it recognises git
// invocations in the shell commands an agent is about to run, and installs
// hooks and instructions into the agent's configuration.
package agent

import (
	"path/filepath"
	"strings"

	"github.com/leoru/gitident-cli/internal/paths"
)

// Git is one git invocation found in a shell command.
type Git struct {
	Dir       string   // directory git runs in (after cd and -C)
	Sub       string   // subcommand, e.g. "commit"
	Args      []string // arguments after the subcommand
	Overrides []string // -c key=value options
	Env       []string // VAR=value assignments in front of the command
}

// ParseGit finds the git invocations in a shell command line run from cwd. It
// understands the common shapes agents produce — `cd dir && git …`,
// `git -C dir …`, `VAR=x git …`, pipelines and command lists — not full shell.
func ParseGit(command, cwd string) []Git {
	var out []Git
	dir := cwd
	var exported []string
	for _, seg := range segments(tokenize(command)) {
		i := 0
		var env []string
		for i < len(seg) && isAssignment(seg[i]) {
			env = append(env, seg[i])
			i++
		}
		for i < len(seg) && (seg[i] == "env" || seg[i] == "command" || seg[i] == "exec" || seg[i] == "time" || seg[i] == "sudo" || seg[i] == "nohup") {
			i++
			for i < len(seg) && isAssignment(seg[i]) {
				env = append(env, seg[i])
				i++
			}
		}
		if i >= len(seg) {
			continue
		}
		switch name := seg[i]; {
		case name == "cd" && i+1 < len(seg):
			dir = join(dir, seg[i+1])
		case name == "export":
			for _, a := range seg[i+1:] {
				if isAssignment(a) {
					exported = append(exported, a)
				}
			}
		case name == "git" || strings.HasSuffix(name, "/git"):
			g := Git{Dir: dir, Env: append(append([]string{}, exported...), env...)}
			rest := seg[i+1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
				opt := rest[0]
				rest = rest[1:]
				switch {
				case opt == "-C" && len(rest) > 0:
					g.Dir = join(g.Dir, rest[0])
					rest = rest[1:]
				case opt == "-c" && len(rest) > 0:
					g.Overrides = append(g.Overrides, rest[0])
					rest = rest[1:]
				case (opt == "--git-dir" || opt == "--work-tree" || opt == "--namespace" || opt == "--config-env") && len(rest) > 0:
					rest = rest[1:]
				}
			}
			if len(rest) > 0 {
				g.Sub, g.Args = rest[0], rest[1:]
			}
			out = append(out, g)
		}
	}
	return out
}

func join(dir, p string) string {
	p = paths.Expand(p)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// operator tokens separate commands.
var operators = map[string]bool{"&&": true, "||": true, ";": true, "|": true, "&": true, "\n": true, "(": true, ")": true}

func segments(tokens []string) [][]string {
	var out [][]string
	var cur []string
	for _, t := range tokens {
		if operators[t] {
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// tokenize splits a command line into words and operators, handling quotes
// and backslash escapes. Operators come out as separate tokens; quoted
// operator characters stay part of their word.
func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			toks = append(toks, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				cur.WriteString(s[i+1:])
				i = len(s)
			} else {
				cur.WriteString(s[i+1 : i+1+j])
				i += j + 1
			}
		case c == '"':
			inWord = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
		case c == '\\' && i+1 < len(s):
			i++
			if s[i] != '\n' {
				inWord = true
				cur.WriteByte(s[i])
			}
		case c == ' ' || c == '\t':
			flush()
		case c == '#' && !inWord:
			for i < len(s) && s[i] != '\n' {
				i++
			}
			i--
		case c == '&' || c == '|':
			flush()
			if i+1 < len(s) && s[i+1] == c {
				toks = append(toks, string([]byte{c, c}))
				i++
			} else {
				toks = append(toks, string(c))
			}
		case c == ';' || c == '\n' || c == '(' || c == ')':
			flush()
			toks = append(toks, string(c))
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	flush()
	return toks
}

// identityKeys are the settings an agent must not set by hand.
var identityKeys = map[string]bool{"user.name": true, "user.email": true, "author.name": true, "author.email": true, "committer.name": true, "committer.email": true}

var identityEnv = []string{"GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=", "GIT_COMMITTER_EMAIL=", "EMAIL="}

// SetsIdentity reports how an invocation sets the identity by hand, or "".
func (g Git) SetsIdentity() string {
	for _, e := range g.Env {
		for _, p := range identityEnv {
			if strings.HasPrefix(e, p) {
				return "the environment variable " + strings.TrimSuffix(p, "=")
			}
		}
	}
	for _, o := range g.Overrides {
		k, _, _ := strings.Cut(o, "=")
		if identityKeys[strings.ToLower(k)] {
			return "`git -c " + k + "=…`"
		}
	}
	if g.Sub != "config" {
		return ""
	}
	// git config [scope/options] [set] <key> <value>
	var words []string
	for i := 0; i < len(g.Args); i++ {
		a := g.Args[i]
		switch {
		case a == "--unset" || a == "--unset-all" || a == "unset" || a == "--get" || a == "--get-all" || a == "get" ||
			a == "--list" || a == "-l" || a == "list" || a == "--get-regexp" || a == "--remove-section":
			return ""
		case a == "-f" || a == "--file" || a == "--blob" || a == "--type" || a == "--default" || a == "--comment" || a == "--value":
			i++
		case strings.HasPrefix(a, "-"):
		case a == "set":
		default:
			words = append(words, a)
		}
	}
	if len(words) >= 2 && identityKeys[strings.ToLower(words[0])] {
		return "`git config " + words[0] + "`"
	}
	return ""
}

// commitSubs create commits or tags with the configured identity.
var commitSubs = map[string]bool{"commit": true, "merge": true, "rebase": true, "cherry-pick": true, "revert": true, "am": true, "pull": true}

// MakesCommit reports whether the invocation records the identity in a new
// commit or annotated tag.
func (g Git) MakesCommit() bool {
	if commitSubs[g.Sub] {
		return true
	}
	if g.Sub == "tag" {
		for _, a := range g.Args {
			switch a {
			case "-a", "-s", "-u", "-m", "--annotate", "--sign", "--local-user":
				return true
			}
			if strings.HasPrefix(a, "-m") || strings.HasPrefix(a, "--message") || strings.HasPrefix(a, "-F") {
				return true
			}
		}
	}
	return false
}

// SkipsSigning reports whether the invocation turns signing off.
func (g Git) SkipsSigning() bool {
	for _, a := range g.Args {
		if a == "--no-gpg-sign" || a == "--no-sign" {
			return true
		}
	}
	for _, o := range g.Overrides {
		k, v, _ := strings.Cut(o, "=")
		if strings.EqualFold(k, "commit.gpgsign") && (strings.EqualFold(v, "false") || v == "0" || strings.EqualFold(v, "no") || strings.EqualFold(v, "off")) {
			return true
		}
	}
	return false
}
