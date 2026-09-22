// Package cli implements the gitident command-line interface.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/paths"
)

// App carries the I/O streams and build information for one invocation.
type App struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Stdin   io.Reader
	Version string
}

// New returns an App wired to the process's standard streams.
func New(version string) *App {
	return &App{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin, Version: version}
}

type command struct {
	name    string
	summary string
	usage   string
	hidden  bool
	run     func(a *App, args []string) error
}

func commands() []command {
	return []command{
		{name: "init", summary: "write a commented sample profiles.yaml", usage: initUsage, run: (*App).cmdInit},
		{name: "import", summary: "generate profiles.yaml from existing git configuration", usage: importUsage, run: (*App).cmdImport},
		{name: "sync", summary: "render profiles into ~/.gitconfig", usage: syncUsage, run: (*App).cmdSync},
		{name: "which", summary: "show the identity git uses in a directory", usage: whichUsage, run: (*App).cmdWhich},
		{name: "check", summary: "audit every repository under the configured roots", usage: checkUsage, run: (*App).cmdCheck},
		{name: "use", summary: "pin a repository to a profile", usage: useUsage, run: (*App).cmdUse},
		{name: "unuse", summary: "remove a repository's pin", usage: unuseUsage, run: (*App).cmdUnuse},
		{name: "apply", summary: "copy a repository's profile into its .git/config", usage: applyUsage, run: (*App).cmdApply},
		{name: "unapply", summary: "remove a profile copied by apply", usage: unapplyUsage, run: (*App).cmdUnapply},
		{name: "doctor", summary: "diagnose the installation", usage: doctorUsage, run: (*App).cmdDoctor},
		{name: "uninstall", summary: "remove the managed block and fragments", usage: uninstallUsage, run: (*App).cmdUninstall},
		{name: "completion", summary: "print a shell completion script", usage: completionUsage, run: (*App).cmdCompletion},
		{name: "version", summary: "print the version", usage: "usage: gitident version\n", run: (*App).cmdVersion},
		{name: "help", summary: "show help for a command", usage: "usage: gitident help [command]\n", run: (*App).cmdHelp},
		{name: "__profiles", hidden: true, run: (*App).cmdProfiles},
	}
}

func findCommand(name string) (command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// exitError ends the program with a status code; a non-empty message is printed.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// problems signals "the command ran, but found problems" (exit 1, nothing more to print).
var errProblems = &exitError{code: 1}

func usageError(format string, args ...any) error {
	return &exitError{code: 2, msg: fmt.Sprintf(format, args...)}
}

// Run executes the command line (without the program name) and returns the exit code.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.printUsage(a.Stderr)
		return 2
	}
	name := args[0]
	switch name {
	case "-h", "--help":
		a.printUsage(a.Stdout)
		return 0
	case "-v", "--version":
		name = "version"
	}
	cmd, ok := findCommand(name)
	if !ok {
		fmt.Fprintf(a.Stderr, "gitident: unknown command %q\n\n", name)
		a.printUsage(a.Stderr)
		return 2
	}
	err := cmd.run(a, args[1:])
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintf(a.Stderr, "gitident %s: %s\n", name, ee.msg)
			if ee.code == 2 && cmd.usage != "" {
				fmt.Fprintf(a.Stderr, "\n%s", cmd.usage)
			}
		}
		return ee.code
	}
	fmt.Fprintf(a.Stderr, "gitident %s: %v\n", name, err)
	return 1
}

func (a *App) printUsage(w io.Writer) {
	fmt.Fprintf(w, `gitident keeps your git identities in one profiles.yaml and renders them into
includeIf rules, so every repository picks the right name, email, signing key
and SSH key automatically — by directory, by remote URL, or by explicit list.

usage: gitident <command> [flags] [args]

commands:
`)
	for _, c := range commands() {
		if c.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-11s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, `
Run "gitident help <command>" for details. Config: %s
(override with $%s). Also available as "git ident <command>".
`, paths.Contract(paths.ConfigPath()), paths.ConfigEnv)
}

func (a *App) cmdHelp(args []string) error {
	if len(args) == 0 {
		a.printUsage(a.Stdout)
		return nil
	}
	c, ok := findCommand(args[0])
	if !ok || c.hidden {
		return usageError("unknown command %q", args[0])
	}
	fmt.Fprint(a.Stdout, c.usage)
	return nil
}

func (a *App) cmdVersion(args []string) error {
	if len(args) > 0 {
		return usageError("unexpected arguments")
	}
	fmt.Fprintf(a.Stdout, "gitident %s\n", a.version())
	return nil
}

func (a *App) version() string {
	if a.Version != "" && a.Version != "dev" {
		return a.Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// cmdProfiles prints profile names, one per line (used by shell completion).
func (a *App) cmdProfiles(args []string) error {
	cfg, err := config.Load(paths.ConfigPath())
	if err != nil {
		return nil
	}
	for _, n := range cfg.ProfileNames() {
		fmt.Fprintln(a.Stdout, n)
	}
	return nil
}

// newFlags creates a FlagSet whose -h prints the command usage.
func (a *App) newFlags(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.Usage = func() { fmt.Fprint(a.Stderr, usage) }
	return fs
}

// parseFlags parses flags that may appear before, between or after positional
// arguments (the standard flag package stops at the first positional one).
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, &exitError{code: 2}
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		// A literal "--" was consumed by Parse only if it preceded rest; anything
		// after it is positional.
		if len(args) > len(rest) && args[len(args)-len(rest)-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// loadConfig loads and validates profiles.yaml, printing warnings. Validation
// errors are printed and turned into an exit error.
func (a *App) loadConfig() (*config.Config, error) {
	cfg, err := config.Load(paths.ConfigPath())
	if err != nil {
		return nil, err
	}
	rep := cfg.Validate()
	for _, w := range rep.Warnings {
		a.warn("%s", w)
	}
	if !rep.OK() {
		for _, e := range rep.Errors {
			fmt.Fprintf(a.Stderr, "error: %s\n", e)
		}
		return nil, &exitError{code: 1, msg: fmt.Sprintf("%s has %d error(s)", paths.Contract(paths.ConfigPath()), len(rep.Errors))}
	}
	return cfg, nil
}

func (a *App) warn(format string, args ...any) {
	fmt.Fprintf(a.Stderr, "warning: "+format+"\n", args...)
}

func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.Stdout, format, args...)
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	if strings.HasSuffix(word, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(word, "y"))
	}
	return fmt.Sprintf("%d %ss", n, word)
}
