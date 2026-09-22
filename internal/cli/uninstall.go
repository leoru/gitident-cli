package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/render"
)

const uninstallUsage = `usage: gitident uninstall [--dry-run]

Remove the managed block from your global gitconfig and delete the generated
fragments in ~/.gitconfig.d/gitident. profiles.yaml is kept. Repositories
pinned with "gitident use" keep an include.path to a now-missing fragment
(git ignores it); run "gitident unuse" in them first if you want them clean.
`

func (a *App) cmdUninstall(args []string) error {
	fs := a.newFlags("uninstall", uninstallUsage)
	dryRun := fs.Bool("dry-run", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("unexpected arguments: %s", strings.Join(pos, " "))
	}
	verb := "removed"
	if *dryRun {
		verb = "would remove"
	}

	global := paths.GlobalGitConfig()
	old, err := fsutil.ReadFileIfExists(global)
	if err != nil {
		return err
	}
	updated, found, err := render.RemoveBlock(old)
	if err != nil {
		if errors.Is(err, render.ErrCorruptBlock) {
			fmt.Fprintln(a.Stderr, render.CorruptBlockHelp(paths.Contract(global)))
		}
		return err
	}
	if found {
		a.printf("%s managed block from %s\n", verb, paths.Contract(global))
		if !*dryRun {
			if err := fsutil.WriteFileAtomic(global, updated, 0o644); err != nil {
				return err
			}
		}
	}

	dir := paths.FragmentDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil || !bytes.HasPrefix(data, fragmentHeader) {
			continue
		}
		a.printf("%s %s\n", verb, paths.Contract(p))
		if !*dryRun {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	if !*dryRun {
		_ = os.Remove(dir) // only succeeds when empty
	}
	if !found && len(entries) == 0 {
		a.printf("nothing to remove\n")
	}
	return nil
}
