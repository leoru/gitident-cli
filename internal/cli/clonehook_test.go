package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/render"
	"github.com/leoru/gitident-cli/internal/testutil"
)

// buildBinary puts a gitident binary on PATH for hooks to run.
func buildBinary(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	cmd := exec.Command("go", "build", "-o", filepath.Join(bin, "gitident"), "github.com/leoru/gitident-cli/cmd/gitident")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func gitClone(t *testing.T, src, dst string) string {
	t.Helper()
	out, err := exec.Command("git", "clone", "-q", src, dst).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	return string(out)
}

func TestCloneHook(t *testing.T) {
	buildBinary(t)
	home := setupE2E(t)
	src := filepath.Join(home, "code", "blog")
	testutil.Git(t, src, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "--allow-empty", "-q", "-m", "init")

	cfg := strings.Replace(e2eConfig, "version: 1\n", "version: 1\nclone_hook: true\n", 1)
	cfg = strings.Replace(cfg, "    extra:\n      pull.rebase", "    materialize: true\n    extra:\n      pull.rebase", 1)
	testutil.WriteFile(t, paths.ConfigPath(), cfg)
	r := mustRun(t, "sync")
	if !strings.Contains(r.stdout, "write ~/.gitconfig.d/gitident/template/hooks/post-checkout") {
		t.Errorf("sync:\n%s", r.stdout)
	}
	if st, err := os.Stat(ownHookPath()); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("hook not executable: %v", err)
	}
	gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if !strings.Contains(string(gc), "path = ~/.gitconfig.d/gitident/_clone.gitconfig") {
		t.Errorf("block lacks the clone include:\n%s", gc)
	}
	mustRun(t, "doctor")

	// A clone into a work dir gets the profile copied and says so.
	out := gitClone(t, src, filepath.Join(home, "work", "cloned"))
	if !strings.Contains(out, `gitident: profile "work" (Pat Work <pat@company.com>), copied into .git/config`) {
		t.Errorf("clone output:\n%s", out)
	}
	if got := isolatedAuthor(t, filepath.Join(home, "work", "cloned")); got != "Pat Work <pat@company.com>" {
		t.Errorf("cloned repo isolated author = %q", got)
	}
	// A clone nobody claims is flagged at once.
	out = gitClone(t, src, filepath.Join(home, "tmp", "stray"))
	if !strings.Contains(out, "no profile applies to this repository, so commits will fail") {
		t.Errorf("stray clone output:\n%s", out)
	}

	// A hook we did not write is never replaced.
	testutil.WriteFile(t, ownHookPath(), "#!/bin/sh\necho mine\n")
	if r := mustRun(t, "sync"); !strings.Contains(r.stderr, "not replacing it") {
		t.Errorf("sync over foreign hook, stderr:\n%s", r.stderr)
	}
	testutil.WriteFile(t, ownHookPath(), render.HookScript)

	// Turning it off removes hook, fragment and include.
	testutil.WriteFile(t, paths.ConfigPath(), e2eConfig)
	r = mustRun(t, "sync")
	if !strings.Contains(r.stdout, "remove ~/.gitconfig.d/gitident/template/hooks/post-checkout") ||
		!strings.Contains(r.stdout, "removed ~/.gitconfig.d/gitident/_clone.gitconfig") {
		t.Errorf("sync with clone_hook off:\n%s", r.stdout)
	}
	if _, err := os.Stat(paths.Expand(paths.TemplateDirTilde)); !os.IsNotExist(err) {
		t.Error("template dir left behind")
	}
}

func TestCloneHookUsesExistingTemplateDir(t *testing.T) {
	home := setupE2E(t)
	tmpl := filepath.Join(home, "my-template")
	testutil.WriteFile(t, filepath.Join(home, ".gitconfig"), "[init]\n\ttemplateDir = ~/my-template\n")
	testutil.WriteFile(t, paths.ConfigPath(), strings.Replace(e2eConfig, "version: 1\n", "version: 1\nclone_hook: true\n", 1))
	r := mustRun(t, "sync")
	if !strings.Contains(r.stderr, "init.templateDir is already set to ~/my-template") {
		t.Errorf("sync stderr:\n%s", r.stderr)
	}
	if !isOurHook(filepath.Join(tmpl, "hooks", "post-checkout")) {
		t.Error("hook not installed into the existing template dir")
	}
	if gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig")); strings.Contains(string(gc), "_clone.gitconfig") {
		t.Errorf("block must not override your init.templateDir:\n%s", gc)
	}
	mustRun(t, "uninstall")
	if _, err := os.Stat(filepath.Join(tmpl, "hooks", "post-checkout")); !os.IsNotExist(err) {
		t.Error("uninstall left the hook")
	}
}
