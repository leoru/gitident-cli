package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/materialize"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/testutil"
)

// isolatedAuthor commits in dir with the global config hidden, as in a dev
// container that only mounts the repository; it returns "name <email>".
func isolatedAuthor(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "commit", "--allow-empty", "-q", "-m", "test")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "error: " + string(out)
	}
	return strings.TrimSpace(testutil.Git(t, dir, "log", "-1", "--format=%an <%ae>"))
}

func localValue(t *testing.T, repo, key string) string {
	t.Helper()
	out, _ := exec.Command("git", "config", "--file", filepath.Join(repo, ".git", "config"), "--get", key).Output()
	return strings.TrimSpace(string(out))
}

func registry(t *testing.T) []string {
	t.Helper()
	reg, err := materialize.LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range reg {
		out = append(out, paths.Contract(f))
	}
	return out
}

func TestApplyLifecycle(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	api := filepath.Join(home, "work", "api")

	if got := isolatedAuthor(t, api); got == "Pat Work <pat@company.com>" {
		t.Fatalf("without a copy the identity should not reach an isolated git, got %q", got)
	}
	r := mustRun(t, "apply", api)
	if !strings.Contains(r.stdout, `copy profile "work" into ~/work/api/.git/config`) {
		t.Errorf("apply output:\n%s", r.stdout)
	}
	if got := isolatedAuthor(t, api); got != "Pat Work <pat@company.com>" {
		t.Errorf("isolated author = %q", got)
	}
	if v := localValue(t, api, "pull.rebase"); v != "true" {
		t.Errorf("extra key not copied: %q", v)
	}
	if reg := registry(t); len(reg) != 1 || reg[0] != "~/work/api/.git/config" {
		t.Errorf("registry = %v", reg)
	}
	if r := mustRun(t, "apply", api); !strings.Contains(r.stdout, "already has profile") {
		t.Errorf("second apply:\n%s", r.stdout)
	}
	r = mustRun(t, "which", api)
	if !strings.Contains(r.stdout, "work  (copied into ~/work/api/.git/config by gitident apply)") {
		t.Errorf("which:\n%s", r.stdout)
	}
	r = mustRun(t, "check", filepath.Join(home, "work"))
	if !strings.Contains(r.stdout, "copied into .git/config") {
		t.Errorf("check:\n%s", r.stdout)
	}
	mustRun(t, "doctor")

	// Profile edits reach the copy on sync; check flags it until then.
	testutil.WriteFile(t, paths.ConfigPath(), strings.Replace(e2eConfig, "pat@company.com", "pat@corp.example", 1))
	if r := run(t, "check", filepath.Join(home, "work")); r.code != 1 || !strings.Contains(r.stdout, "STALE") {
		t.Errorf("check before sync (%d):\n%s", r.code, r.stdout)
	}
	if r := run(t, "doctor"); r.code != 1 {
		t.Errorf("doctor should fail on a stale copy:\n%s", r.stdout)
	}
	r = mustRun(t, "sync", "--dry-run")
	if !strings.Contains(r.stdout, `would update profile "work" in ~/work/api/.git/config`) || localValue(t, api, "user.email") != "pat@company.com" {
		t.Errorf("dry-run sync:\n%s", r.stdout)
	}
	r = mustRun(t, "sync")
	if !strings.Contains(r.stdout, `update profile "work" in ~/work/api/.git/config`) || !strings.Contains(r.stdout, "1 materialized copy") {
		t.Errorf("sync:\n%s", r.stdout)
	}
	if got := isolatedAuthor(t, api); got != "Pat Work <pat@corp.example>" {
		t.Errorf("after sync isolated author = %q", got)
	}

	// Values changed by hand are left alone until --force.
	testutil.Git(t, api, "config", "user.name", "By Hand")
	testutil.WriteFile(t, paths.ConfigPath(), e2eConfig)
	r = mustRun(t, "sync")
	if !strings.Contains(r.stderr, "were changed by hand (user.name = By Hand; user.email = pat@corp.example)") {
		t.Errorf("sync over hand edit, stderr:\n%s", r.stderr)
	}
	if r := run(t, "check", filepath.Join(home, "work")); r.code != 1 || !strings.Contains(r.stdout, "changed by hand") {
		t.Errorf("check after hand edit:\n%s", r.stdout)
	}
	if r := run(t, "apply", api); r.code != 1 {
		t.Errorf("apply over hand edit should fail: %+v", r)
	}
	mustRun(t, "apply", "--force", api)
	if got := isolatedAuthor(t, api); got != "Pat Work <pat@company.com>" {
		t.Errorf("after --force isolated author = %q", got)
	}

	// Pins switch the copy; unpinning to "no profile" removes it.
	unmatched := filepath.Join(home, "tmp", "unmatched")
	mustRun(t, "use", "personal", unmatched)
	mustRun(t, "apply", unmatched)
	r = mustRun(t, "use", "oss", unmatched)
	if !strings.Contains(r.stdout, `switch ~/tmp/unmatched/.git/config from profile "personal" to "oss"`) {
		t.Errorf("use on a copied repo:\n%s", r.stdout)
	}
	r = mustRun(t, "unuse", unmatched)
	if !strings.Contains(r.stdout, `remove profile "oss" from ~/tmp/unmatched/.git/config`) || !strings.Contains(r.stdout, "no profile matches") {
		t.Errorf("unuse on a copied repo:\n%s", r.stdout)
	}
	if v := localValue(t, unmatched, "user.email"); v != "" {
		t.Errorf("copy should be gone, user.email = %q", v)
	}

	// Local values gitident did not write are not overwritten.
	same := testutil.InitRepo(t, filepath.Join(home, "work", "same"))
	testutil.Git(t, same, "config", "user.name", "Someone Else")
	if r := run(t, "apply", same); r.code != 1 || !strings.Contains(r.stderr, "already sets user.name = Someone Else") {
		t.Errorf("apply over local values: %+v", r)
	}

	r = mustRun(t, "unapply", api)
	if !strings.Contains(r.stdout, `remove profile "work" from ~/work/api/.git/config`) {
		t.Errorf("unapply:\n%s", r.stdout)
	}
	if data, _ := os.ReadFile(filepath.Join(api, ".git", "config")); strings.Contains(string(data), "gitident") || strings.Contains(string(data), "[user]") {
		t.Errorf("unapply left:\n%s", data)
	}
	if reg := registry(t); len(reg) != 0 {
		t.Errorf("registry after unapply = %v", reg)
	}
}

func TestMaterializeSetting(t *testing.T) {
	home := setupE2E(t)
	cfg := strings.Replace(e2eConfig, "    email: pat@oss.dev\n", "    email: pat@oss.dev\n    materialize: true\n", 1)
	testutil.WriteFile(t, paths.ConfigPath(), cfg)
	r := mustRun(t, "sync")
	for _, want := range []string{`copy profile "oss" into ~/misc/legacy/.git/config`, `copy profile "oss" into ~/code/notes/.git/config`, "2 materialized copies"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("sync lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "work/api") {
		t.Errorf("profile without materialize was copied:\n%s", r.stdout)
	}
	if r := mustRun(t, "sync"); !strings.HasPrefix(r.stdout, "up to date") {
		t.Errorf("second sync:\n%s", r.stdout)
	}
	// A new clone is flagged by check and picked up by the next sync.
	fresh := testutil.InitRepo(t, filepath.Join(home, "code", "fresh"), "https://github.com/me/notes")
	if r := run(t, "check", "-q"); !strings.Contains(r.stdout, "materialize on but .git/config has no copy") {
		t.Errorf("check for new clone:\n%s", r.stdout)
	}
	mustRun(t, "sync")
	if got := isolatedAuthor(t, fresh); got != "Pat OSS <pat@oss.dev>" {
		t.Errorf("new clone isolated author = %q", got)
	}

	// Deleted repositories are forgotten.
	if err := os.RemoveAll(filepath.Join(home, "misc", "legacy")); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, "sync"); !strings.Contains(r.stdout, "forgot ~/misc/legacy/.git/config") {
		t.Errorf("sync after delete:\n%s", r.stdout)
	}

	// Turning materialize off keeps existing copies current; unapply --all removes them.
	testutil.WriteFile(t, paths.ConfigPath(), e2eConfig)
	mustRun(t, "sync")
	if len(registry(t)) != 2 {
		t.Errorf("registry = %v", registry(t))
	}
	r = mustRun(t, "unapply", "--all")
	if !strings.Contains(r.stdout, "changed 2 repositories") {
		t.Errorf("unapply --all:\n%s", r.stdout)
	}
	if len(registry(t)) != 0 {
		t.Errorf("registry after unapply --all = %v", registry(t))
	}
}

func TestApplyAllAndUninstall(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	r := mustRun(t, "apply", "--all")
	if !strings.Contains(r.stdout, "changed 6 repositories, 0 unchanged") {
		t.Errorf("apply --all:\n%s", r.stdout)
	}
	if got := isolatedAuthor(t, filepath.Join(home, "code", "company-tool")); got != "Pat Work <pat@company.com>" {
		t.Errorf("remote rule copy = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "tmp", "unmatched", ".git", "config")); err == nil &&
		localValue(t, filepath.Join(home, "tmp", "unmatched"), "gitident.profile") != "" {
		t.Error("repository without a profile got a copy")
	}
	r = mustRun(t, "uninstall")
	if !strings.Contains(r.stdout, `removed profile "work" from ~/work/api/.git/config`) {
		t.Errorf("uninstall:\n%s", r.stdout)
	}
	if v := localValue(t, filepath.Join(home, "work", "api"), "user.email"); v != "" {
		t.Errorf("uninstall left user.email = %q", v)
	}
	if _, err := os.Stat(materialize.RegistryPath()); !os.IsNotExist(err) {
		t.Error("registry not removed")
	}
}

func TestApplyWorktree(t *testing.T) {
	home := setupE2E(t)
	mustRun(t, "sync")
	main := filepath.Join(home, "work", "api")
	testutil.Git(t, main, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(home, "work", "api-wt")
	testutil.Git(t, main, "worktree", "add", "-q", wt)
	r := mustRun(t, "apply", wt, main)
	if strings.Count(r.stdout, "copy profile") != 1 || !strings.Contains(r.stdout, "~/work/api/.git/config") {
		t.Errorf("apply via worktree:\n%s", r.stdout)
	}
	if got := isolatedAuthor(t, wt); got != "Pat Work <pat@company.com>" {
		t.Errorf("worktree isolated author = %q", got)
	}
	if r := mustRun(t, "which", wt); !strings.Contains(r.stdout, "copied into ~/work/api/.git/config") {
		t.Errorf("which in worktree:\n%s", r.stdout)
	}
}
