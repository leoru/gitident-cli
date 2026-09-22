package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/paths"
)

const initUsage = `usage: gitident init [--force]

Write a commented sample profiles.yaml (pre-filled with your current global
user.name / user.email when set). Refuses to overwrite an existing file
unless --force is given.

To start from your existing git setup instead, use "gitident import".
`

const sampleConfig = `# gitident profiles — see https://github.com/leoru/gitident-cli
#
# Each profile is one git identity. Rules decide which profile a repository gets:
#   dirs:     every repository below these directories
#   remotes:  every repository with a remote URL matching these globs
#             (* = anything except "/", ** = anything; needs git >= 2.36)
# Later rules override earlier ones, and a profile's ` + "`repos`" + ` list beats all rules.
# After editing, run ` + "`gitident sync`" + `.
version: 1

# Refuse to commit in repositories that match no profile (sets user.useConfigOnly).
strict_identity: true

# Also copy each profile's settings into the .git/config of every repository it
# applies to, for tools that ignore includeIf (dev containers, some GUI clients).
# Can be set per profile too. See ` + "`gitident help apply`" + `.
# materialize: false

# Install a post-checkout hook in git's clone template: every new clone reports
# the profile it gets (or that none applies) and gets its copy when materialize is on.
# clone_hook: false

profiles:
  personal:
    name: %s
    email: %s
    # signing_key: ~/.ssh/id_ed25519.pub   # GPG key id, or SSH public key path
    # gpg_format: ssh                      # openpgp (git's default), ssh or x509
    # gpgsign: true                        # sign every commit
    # ssh_key: ~/.ssh/id_ed25519           # core.sshCommand with IdentitiesOnly=yes
    # extra:                               # any other "section.key: value"
    #   pull.rebase: "true"
    # repos:                               # explicit repos: path or remote URL
    #   - ~/misc/some-repo
    #   - git@github.com:me/dotfiles.git

  work:
    name: %s
    email: you@company.example
    # repos:
    #   - git@github.com:company/infra.git

rules:
  - profile: personal
    dirs: ["~/code"]
  - profile: work
    dirs: ["~/work"]
    # remotes: ["git@github.com:company/**", "https://gitlab.company.example/**"]

# Directories ` + "`check`" + ` and ` + "`import`" + ` skip while scanning (names, globs or ~/paths).
# scan:
#   ignore: ["archive", "~/work/tmp"]
`

func (a *App) cmdInit(args []string) error {
	fs := a.newFlags("init", initUsage)
	force := fs.Bool("force", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("unexpected arguments: %s", strings.Join(pos, " "))
	}
	path := paths.ConfigPath()
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", paths.Contract(path))
	}

	name, email := "Your Name", "you@example.com"
	global := paths.GlobalGitConfig()
	if v, _ := gitx.FileGetAll(global, "user.name"); len(v) > 0 {
		name = v[len(v)-1]
	}
	if v, _ := gitx.FileGetAll(global, "user.email"); len(v) > 0 {
		email = v[len(v)-1]
	}
	data := fmt.Sprintf(sampleConfig, yamlScalar(name), yamlScalar(email), yamlScalar(name))
	if err := fsutil.WriteFileAtomic(path, []byte(data), 0o644); err != nil {
		return err
	}
	a.printf("wrote %s\n\nnext steps:\n  1. edit it: $EDITOR %s\n  2. preview: gitident sync --dry-run\n  3. apply:   gitident sync\n",
		paths.Contract(path), paths.Contract(path))
	return nil
}

// yamlScalar quotes s if it would not survive as a plain YAML scalar.
func yamlScalar(s string) string {
	if s == "" || strings.TrimSpace(s) != s || strings.Contains(s, ": ") || strings.Contains(s, " #") ||
		strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return s
}
