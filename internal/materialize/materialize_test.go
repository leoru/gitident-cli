package materialize

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/render"
	"github.com/leoru/gitident-cli/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolated(m)) }

func settings(email string) []render.Setting {
	return []render.Setting{
		{Key: "user.name", Value: "Pat Work"},
		{Key: "user.email", Value: email},
		{Key: "core.sshCommand", Value: "ssh -i ~/.ssh/work -o IdentitiesOnly=yes"},
		{Key: "url.git@github.com:.insteadOf", Value: "https://github.com/"},
	}
}

func newConfig(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config")
	testutil.WriteFile(t, f, content)
	return f
}

func read(t *testing.T, f string) string {
	t.Helper()
	data, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplyUpdateRemove(t *testing.T) {
	const orig = "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:me/x.git\n"
	f := newConfig(t, orig)

	res, err := Apply(f, "work", settings("pat@company.com"), Options{})
	if err != nil || res.Action != Written || res.Previous != "" {
		t.Fatalf("first apply = %+v, %v", res, err)
	}
	rec, _ := Read(f)
	if rec.Profile != "work" || rec.Edited() || !rec.UpToDate("work", settings("pat@company.com")) {
		t.Errorf("record after apply = %+v", rec)
	}
	if res, _ := Apply(f, "work", settings("pat@company.com"), Options{}); res.Action != Unchanged {
		t.Errorf("second apply = %+v", res)
	}

	// A changed profile rewrites the values; dropped keys are removed.
	res, _ = Apply(f, "work", settings("pat@new.com")[:2], Options{})
	if res.Action != Written {
		t.Errorf("update = %+v", res)
	}
	got := read(t, f)
	if !strings.Contains(got, "email = pat@new.com") || strings.Contains(got, "sshCommand") || strings.Contains(got, "insteadOf") {
		t.Errorf("after update:\n%s", got)
	}

	res, _ = Remove(f, Options{})
	if res.Action != Removed || res.Previous != "work" {
		t.Errorf("remove = %+v", res)
	}
	if got := read(t, f); got != orig {
		t.Errorf("remove left:\n%q\nwant\n%q", got, orig)
	}
	if res, _ := Remove(f, Options{}); res.Action != Unchanged {
		t.Errorf("second remove = %+v", res)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newConfig(t, "[core]\n\tbare = false\n")
	if res, _ := Apply(f, "work", settings("a@b"), Options{DryRun: true}); res.Action != Written {
		t.Errorf("dry apply = %+v", res)
	}
	if got := read(t, f); got != "[core]\n\tbare = false\n" {
		t.Errorf("dry run wrote:\n%s", got)
	}
}

func TestEditedAndConflicts(t *testing.T) {
	f := newConfig(t, "[user]\n\temail = mine@elsewhere.net\n")
	res, _ := Apply(f, "work", settings("pat@company.com"), Options{})
	if res.Action != Conflict || !reflect.DeepEqual(res.Details, []string{"user.email = mine@elsewhere.net"}) {
		t.Fatalf("conflict = %+v", res)
	}
	if res, _ := Apply(f, "work", settings("pat@company.com"), Options{Force: true}); res.Action != Written {
		t.Fatalf("forced apply = %+v", res)
	}

	testutil.Git(t, "", "config", "--file", f, "user.name", "By Hand")
	rec, _ := Read(f)
	if !rec.Edited() {
		t.Fatal("hand edit not detected")
	}
	res, _ = Apply(f, "work", settings("pat@company.com"), Options{})
	if res.Action != Edited || !reflect.DeepEqual(res.Details, []string{"user.name = By Hand"}) {
		t.Errorf("apply over edit = %+v", res)
	}
	if res, _ := Remove(f, Options{}); res.Action != Edited {
		t.Errorf("remove over edit = %+v", res)
	}
	// Edited back to what the profile wants: adopted without --force.
	testutil.Git(t, "", "config", "--file", f, "user.name", "Pat Work")
	if res, _ := Apply(f, "other", settings("pat@company.com"), Options{}); res.Action != Written || res.Previous != "work" {
		t.Errorf("apply after revert = %+v", res)
	}
	if res, _ := Remove(f, Options{Force: true}); res.Action != Removed {
		t.Errorf("forced remove = %+v", res)
	}
}

func TestEqualLocalValuesAreAdopted(t *testing.T) {
	f := newConfig(t, "[user]\n\temail = pat@company.com\n")
	if res, _ := Apply(f, "work", settings("pat@company.com"), Options{}); res.Action != Written {
		t.Errorf("apply = %+v", res)
	}
}

func TestNormalizeAndHash(t *testing.T) {
	in := []render.Setting{{Key: "user.email", Value: "a"}, {Key: "User.Email", Value: "b"}, {Key: "core.x", Value: "c"}}
	want := []render.Setting{{Key: "user.email", Value: "b"}, {Key: "core.x", Value: "c"}}
	if got := Normalize(in); !reflect.DeepEqual(got, want) {
		t.Errorf("Normalize = %+v", got)
	}
	if Hash(want) == Hash(want[:1]) || Hash(want) != Hash([]render.Setting{{Key: "USER.email", Value: "b"}, {Key: "core.X", Value: "c"}}) {
		t.Error("Hash is not canonical")
	}
}

func TestRegistry(t *testing.T) {
	testutil.Home(t)
	if got, err := LoadRegistry(); err != nil || got != nil {
		t.Fatalf("empty registry = %v, %v", got, err)
	}
	home, _ := os.UserHomeDir()
	a, b := filepath.Join(home, "b", ".git", "config"), filepath.Join(home, "a", ".git", "config")
	if err := SaveRegistry([]string{a, b, a}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(RegistryPath())
	if !strings.HasSuffix(string(data), "\n~/a/.git/config\n~/b/.git/config\n") {
		t.Errorf("registry file:\n%s", data)
	}
	if got, _ := LoadRegistry(); !reflect.DeepEqual(got, []string{b, a}) {
		t.Errorf("LoadRegistry = %v", got)
	}
	if err := SaveRegistry(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(RegistryPath()); !os.IsNotExist(err) {
		t.Error("empty registry not removed")
	}
}
