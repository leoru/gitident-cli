package agent

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	got := tokenize(`cd "my dir" && git commit -m 'fix: a && b' ; echo "x\"y" | cat # comment && git push`)
	want := []string{"cd", "my dir", "&&", "git", "commit", "-m", "fix: a && b", ";", "echo", `x"y`, "|", "cat"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokenize = %q", got)
	}
}

func TestParseGit(t *testing.T) {
	gits := ParseGit(`cd repo && GIT_AUTHOR_EMAIL=a@b git -C sub -c user.email=x@y commit -m "msg"; git -C /abs push`, "/home/me")
	if len(gits) != 2 {
		t.Fatalf("got %d invocations: %+v", len(gits), gits)
	}
	g := gits[0]
	if g.Dir != "/home/me/repo/sub" || g.Sub != "commit" || !reflect.DeepEqual(g.Overrides, []string{"user.email=x@y"}) ||
		!reflect.DeepEqual(g.Env, []string{"GIT_AUTHOR_EMAIL=a@b"}) || !g.MakesCommit() {
		t.Errorf("first = %+v", g)
	}
	if gits[1].Dir != "/abs" || gits[1].Sub != "push" || gits[1].MakesCommit() {
		t.Errorf("second = %+v", gits[1])
	}
}

func TestSetsIdentity(t *testing.T) {
	cases := map[string]bool{
		`git config user.email me@x.com`:                 true,
		`git config --local user.name "Me"`:              true,
		`git config --global --replace-all user.email x`: true,
		`git config set user.email x`:                    true,
		`git -c user.name=Bot commit -m x`:               true,
		`GIT_COMMITTER_EMAIL=x git commit -m y`:          true,
		`export GIT_AUTHOR_EMAIL=x; git commit -m y`:     true,
		`git config user.email`:                          false,
		`git config --get user.email`:                    false,
		`git config --unset user.email`:                  false,
		`git config core.editor vim`:                     false,
		`git -c core.pager=cat log`:                      false,
		`git commit -m "set user.email later"`:           false,
	}
	for cmd, want := range cases {
		got := false
		for _, g := range ParseGit(cmd, "/r") {
			if g.SetsIdentity() != "" {
				got = true
			}
		}
		if got != want {
			t.Errorf("%s: SetsIdentity = %v, want %v", cmd, got, want)
		}
	}
}

func TestMakesCommitAndSigning(t *testing.T) {
	cases := map[string]bool{
		"git commit -m x": true, "git merge main": true, "git pull": true, "git tag -a v1 -m x": true,
		"git tag": false, "git tag v1": false, "git status": false, "git log --oneline": false,
	}
	for cmd, want := range cases {
		if got := ParseGit(cmd, "/r")[0].MakesCommit(); got != want {
			t.Errorf("%s: MakesCommit = %v", cmd, got)
		}
	}
	if !ParseGit("git commit --no-gpg-sign -m x", "/r")[0].SkipsSigning() || !ParseGit("git -c commit.gpgsign=false commit", "/r")[0].SkipsSigning() {
		t.Error("SkipsSigning not detected")
	}
}

func TestIsOurCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"gitident agent hook claude":                     true,
		"'/opt/x/bin/gi tident' agent hook cursor":       true,
		"/tmp/go-build1/b111/cli.test agent hook gemini": true,
		"gitident agent hook nope":                       false,
		"my-linter":                                      false,
		"agent hook claude":                              false,
	} {
		if got := isOurCommand(cmd); got != want {
			t.Errorf("isOurCommand(%q) = %v", cmd, got)
		}
	}
}
