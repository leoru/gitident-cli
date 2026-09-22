package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/fsutil"
	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/render"
)

const doctorUsage = `usage: gitident doctor

Check that everything gitident relies on is in place: git version and
hasconfig support, profiles.yaml validity, key files, GPG keys, SSH signing
setup, the managed block and fragments being current, and no global identity
outside the block. Exits 1 if any check fails.
`

type doctor struct {
	a      *App
	failed int
	warned int
}

func (d *doctor) pass(format string, args ...any) {
	d.a.printf("  ok    %s\n", fmt.Sprintf(format, args...))
}

func (d *doctor) fail(hint, format string, args ...any) {
	d.failed++
	d.a.printf("  FAIL  %s\n", fmt.Sprintf(format, args...))
	if hint != "" {
		d.a.printf("        → %s\n", hint)
	}
}

func (d *doctor) warn(hint, format string, args ...any) {
	d.warned++
	d.a.printf("  warn  %s\n", fmt.Sprintf(format, args...))
	if hint != "" {
		d.a.printf("        → %s\n", hint)
	}
}

func (a *App) cmdDoctor(args []string) error {
	fs := a.newFlags("doctor", doctorUsage)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("unexpected arguments")
	}
	d := &doctor{a: a}
	a.printf("gitident %s\n\n", a.version())

	// git
	a.printf("git\n")
	v, err := gitx.GitVersion()
	if err != nil {
		d.fail("install git and make sure it is on $PATH", "git not usable: %v", err)
		a.printf("\n1 check failed\n")
		return errProblems
	}
	if v.AtLeast(gitx.HasConfigMinVersion.Major, gitx.HasConfigMinVersion.Minor) {
		d.pass("git %s (supports includeIf hasconfig:remote.*.url)", v)
	} else {
		d.warn(fmt.Sprintf("upgrade to git %d.%d+ to use `remotes` rules and URL repos", gitx.HasConfigMinVersion.Major, gitx.HasConfigMinVersion.Minor),
			"git %s is older than %d.%d: hasconfig conditions are ignored", v, gitx.HasConfigMinVersion.Major, gitx.HasConfigMinVersion.Minor)
	}

	// config
	a.printf("\nconfig\n")
	cfgPath := paths.ConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		hint := "fix the YAML error"
		if errors.Is(err, config.ErrNotFound) {
			hint = "run `gitident init` or `gitident import --write`"
		}
		d.fail(hint, "%v", err)
		return d.finish()
	}
	rep := cfg.Validate()
	if rep.OK() {
		d.pass("%s is valid (%s, %s)", paths.Contract(cfgPath), plural(len(cfg.Profiles), "profile"), plural(len(cfg.Rules), "rule"))
	} else {
		for _, e := range rep.Errors {
			d.fail("edit "+paths.Contract(cfgPath), "%s", e)
		}
	}
	if usesHasconfig(cfg) && !v.AtLeast(gitx.HasConfigMinVersion.Major, gitx.HasConfigMinVersion.Minor) {
		d.fail("upgrade git, or replace `remotes` rules and URL repos with dirs/paths", "profiles.yaml uses remote URL matching, which git %s ignores", v)
	}

	// keys
	a.printf("\nkeys\n")
	checkedKeys := 0
	for _, name := range cfg.ProfileNames() {
		p := cfg.Profiles[name]
		if p.SSHKey != "" {
			checkedKeys++
			if _, err := os.Stat(paths.Expand(p.SSHKey)); err != nil {
				d.fail("fix ssh_key or create the key with ssh-keygen", "%s: ssh_key %s not found", name, p.SSHKey)
			} else {
				d.pass("%s: ssh_key %s exists", name, p.SSHKey)
			}
		}
		if p.SigningKey == "" {
			if p.GPGSign != nil && *p.GPGSign {
				d.fail("set signing_key", "%s: gpgsign is on but no signing_key is set", name)
			}
			continue
		}
		checkedKeys++
		switch p.GPGFormat {
		case "ssh":
			d.checkSSHSigning(name, p)
		case "x509":
			d.pass("%s: x509 signing key %s (not verified)", name, p.SigningKey)
		default:
			d.checkGPGKey(name, p.SigningKey)
		}
	}
	if checkedKeys == 0 {
		d.pass("no key files or signing keys configured")
	}

	// gitconfig
	a.printf("\ngitconfig\n")
	d.checkManaged(cfg)
	return d.finish()
}

func (d *doctor) finish() error {
	d.a.printf("\n")
	switch {
	case d.failed > 0:
		d.a.printf("%s failed\n", plural(d.failed, "check"))
		return errProblems
	case d.warned > 0:
		d.a.printf("all checks passed (%s)\n", plural(d.warned, "warning"))
	default:
		d.a.printf("all checks passed\n")
	}
	return nil
}

func (d *doctor) checkSSHSigning(name string, p *config.Profile) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		d.fail("install OpenSSH (ssh-keygen signs commits when gpg.format = ssh)", "%s: ssh-keygen not found", name)
	}
	if !config.IsSSHKeyPath(p.SigningKey) {
		d.pass("%s: literal SSH signing key", name)
		return
	}
	path := paths.Expand(p.SigningKey)
	data, err := os.ReadFile(path)
	if err != nil {
		d.fail("point signing_key at your public key (e.g. ~/.ssh/id_ed25519.pub)", "%s: signing_key %s not found", name, p.SigningKey)
		return
	}
	if !bytes.HasPrefix(data, []byte("ssh-")) && !bytes.HasPrefix(data, []byte("ecdsa-")) && !bytes.HasPrefix(data, []byte("sk-")) {
		if strings.HasSuffix(path, ".pub") {
			d.warn("", "%s: signing_key %s does not look like an SSH public key", name, p.SigningKey)
		} else {
			d.pass("%s: signing_key %s exists (private key; git derives the public key)", name, p.SigningKey)
		}
		return
	}
	d.pass("%s: SSH signing key %s", name, p.SigningKey)
	if v, ok, _ := gitx.Config("", "gpg.ssh.allowedSignersFile"); !ok || v == "" {
		d.warn("set gpg.ssh.allowedSignersFile (e.g. in extra) if you want `git log --show-signature` to verify", "%s: gpg.ssh.allowedSignersFile is not set", name)
	}
}

func (d *doctor) checkGPGKey(name, key string) {
	gpg := "gpg"
	if v, ok, _ := gitx.Config("", "gpg.program"); ok && v != "" {
		gpg = v
	}
	if _, err := exec.LookPath(gpg); err != nil {
		d.fail("install GnuPG or set gpg.program", "%s: %s not found (needed for OpenPGP signing key %s)", name, gpg, key)
		return
	}
	out, err := exec.Command(gpg, "--batch", "--list-secret-keys", key).CombinedOutput()
	if err != nil {
		d.fail("import the secret key, or fix signing_key (see `gpg --list-secret-keys --keyid-format long`)",
			"%s: no GPG secret key %s: %s", name, key, firstLine(string(out)))
		return
	}
	d.pass("%s: GPG secret key %s available", name, key)
}

func (d *doctor) checkManaged(cfg *config.Config) {
	global := paths.GlobalGitConfig()
	data, err := fsutil.ReadFileIfExists(global)
	if err != nil {
		d.fail("", "cannot read %s: %v", paths.Contract(global), err)
		return
	}
	onDisk, found, err := render.ExtractBlock(data)
	switch {
	case err != nil:
		d.fail("fix the markers by hand (see `gitident sync` output)", "%s: %v", paths.Contract(global), err)
	case !found:
		d.fail("run `gitident sync`", "no managed block in %s", paths.Contract(global))
	case len(foreignLines(data)) > 0:
		d.fail("move them above the managed block; `gitident sync` would delete them", "%s has settings inside the managed block: %s",
			paths.Contract(global), strings.Join(foreignLines(data), "; "))
	case !bytes.Equal(onDisk, render.Block(cfg)):
		d.fail("run `gitident sync`", "managed block in %s is out of date", paths.Contract(global))
	default:
		d.pass("managed block in %s is current", paths.Contract(global))
	}

	stale := 0
	for _, f := range wantedFragments(cfg) {
		got, err := fsutil.ReadFileIfExists(f.path)
		if err != nil || !bytes.Equal(got, f.data) {
			stale++
		}
	}
	if stale > 0 {
		d.fail("run `gitident sync`", "%s in %s missing or out of date", plural(stale, "fragment"), paths.FragmentDirTilde)
	} else {
		d.pass("%s in %s current", plural(len(cfg.Profiles), "fragment"), paths.FragmentDirTilde)
	}

	warns := globalIdentityWarnings(cfg, global)
	for _, w := range warns {
		d.fail("delete user.name / user.email from your global config; profiles supply them", "%s", w)
	}
	if len(warns) == 0 {
		d.pass("no global identity outside the managed block")
	}
}

func foreignLines(data []byte) []string {
	lines, _ := render.ForeignLines(data)
	return lines
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
