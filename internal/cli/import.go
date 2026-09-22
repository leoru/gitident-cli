package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/importer"
	"github.com/leoru/gitident-cli/internal/paths"
)

const importUsage = `usage: gitident import [roots...] [--write | --merge | --force] [--from-gitkraken] [--interactive]

Build a profiles.yaml from the configuration you already have:
  - your global gitconfig: [user] and every includeIf "gitdir:" /
    "hasconfig:remote.*.url:" fragment it references
  - repositories under roots that set an identity in their own .git/config
    (roots default to the directories of imported gitdir rules)
  - GitKraken profiles, with --from-gitkraken

Identities are de-duplicated into profiles; includeIf conditions become rules
or repos entries; local identities become repos entries unless the rules already
produce them. Every profile carries a "# source:" comment.

By default the result is printed to stdout and nothing is written. import never
touches your gitconfig — review the output, then run "gitident sync".

flags:
  --write           write profiles.yaml (refuses if it exists)
  --merge           add only new profiles, repos and rules to an existing profiles.yaml
  --force           overwrite an existing profiles.yaml
  --from-gitkraken  also read ~/.gitkraken/profiles/*/profile
  --interactive     ask for each profile's name
`

func (a *App) cmdImport(args []string) error {
	fs := a.newFlags("import", importUsage)
	write := fs.Bool("write", false, "")
	merge := fs.Bool("merge", false, "")
	force := fs.Bool("force", false, "")
	gitkraken := fs.Bool("from-gitkraken", false, "")
	interactive := fs.Bool("interactive", false, "")
	roots, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if *merge && *force {
		return usageError("--merge and --force are mutually exclusive")
	}

	opts := importer.Options{GlobalFiles: globalFiles(), Roots: roots}
	if *gitkraken {
		opts.GitKrakenDir = paths.Expand("~/.gitkraken")
		if d := os.Getenv("GITIDENT_GITKRAKEN_DIR"); d != "" {
			opts.GitKrakenDir = paths.Expand(d)
		}
	}
	if *interactive {
		in := bufio.NewReader(a.Stdin)
		opts.Prompt = func(q, suggestion string) string {
			fmt.Fprintf(a.Stderr, "%s [%s]: ", q, suggestion)
			line, _ := in.ReadString('\n')
			return line
		}
	}
	res, err := importer.Import(opts)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		a.warn("%s", w)
	}
	if len(res.Order) == 0 {
		return errors.New("no identities found in your git configuration")
	}
	if len(roots) == 0 && len(res.Scanned) > 0 {
		fmt.Fprintf(a.Stderr, "scanned rule directories for local identities: %s\n", joinContracted(res.Scanned))
	} else if len(res.Scanned) == 0 {
		fmt.Fprintf(a.Stderr, "note: no roots given, so per-repository identities were not scanned (pass directories, e.g. `gitident import ~/work ~/code`)\n")
	}

	data, err := res.YAML()
	if err != nil {
		return err
	}
	path := paths.ConfigPath()
	_, statErr := os.Stat(path)
	exists := statErr == nil
	switch {
	case *merge && exists:
		existing, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, changes, err := importer.Merge(existing, res)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			a.printf("nothing new to merge into %s\n", paths.Contract(path))
			break
		}
		if err := fsutil.WriteFileAtomic(path, out, 0o644); err != nil {
			return err
		}
		for _, c := range changes {
			a.printf("%s\n", c)
		}
		a.printf("merged into %s\n", paths.Contract(path))
	case *write || *merge || *force:
		if exists && !*force {
			return fmt.Errorf("%s already exists (use --merge to add to it, or --force to overwrite)", paths.Contract(path))
		}
		if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
			return err
		}
		a.printf("wrote %s (%s)\n", paths.Contract(path), plural(len(res.Order), "profile"))
	default:
		a.printf("%s", data)
	}

	for _, h := range res.Hints {
		fmt.Fprintf(a.Stderr, "hint: %s\n", h)
	}
	if *write || *merge || *force {
		fmt.Fprintf(a.Stderr, "\nnext: review %s, then `gitident sync --dry-run`, `gitident sync` and `gitident check`.\n"+
			"Once check is clean, delete the old [user] and includeIf entries from %s — the managed block replaces them.\n",
			paths.Contract(path), paths.Contract(paths.GlobalGitConfig()))
	} else {
		fmt.Fprintf(a.Stderr, "\n(dry run — nothing written; use --write to save to %s)\n", paths.Contract(path))
	}
	return nil
}

// globalFiles lists the global config files git reads, in order.
func globalFiles() []string {
	if p := os.Getenv("GIT_CONFIG_GLOBAL"); p != "" {
		return []string{paths.Expand(p)}
	}
	xdg := paths.Expand("~/.config/git/config")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		xdg = filepath.Join(x, "git", "config")
	}
	return []string{xdg, paths.Expand("~/.gitconfig")}
}

func joinContracted(ps []string) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += ", "
		}
		out += paths.Contract(paths.Abs(p))
	}
	return out
}
