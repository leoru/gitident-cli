package render

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leoru/gitident-cli/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./internal/render -update` to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func boolPtr(b bool) *bool { return &b }

func sampleConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Profiles: map[string]*config.Profile{
			"work": {
				Name:       "Kirill",
				Email:      "kirill@company.com",
				SigningKey: "ABCDEF1234567890",
				GPGFormat:  "openpgp",
				GPGSign:    boolPtr(true),
				SSHKey:     "~/.ssh/id_work",
				Extra: map[string]string{
					"pull.rebase":                   "true",
					"remote.origin.prune":           "true",
					"core.editor":                   "code --wait",
					"alias.lg":                      `log --graph --format="%h %s"`,
					"url.git@github.com:.insteadOf": "https://github.com/",
				},
				Repos: []string{"/srv/legacy-thing", "git@github.com:company/infra.git"},
			},
			"personal": {
				Name:  "Kirill K",
				Email: "me@example.org",
				Repos: []string{"https://github.com/me/dotfiles"},
			},
		},
		Rules: []config.Rule{
			{Profile: "personal", Dirs: []string{"/nonexistent-root/code"}},
			{Profile: "work", Dirs: []string{"/nonexistent-root/work/"}, Remotes: []string{"git@github.com:company/**", "https://gitlab.company.com/**"}},
		},
	}
}

func TestFragmentGolden(t *testing.T) {
	cfg := sampleConfig()
	golden(t, "work.gitconfig", Fragment("work", cfg.Profiles["work"]))
	golden(t, "personal.gitconfig", Fragment("personal", cfg.Profiles["personal"]))
	// Byte-stable across runs despite map iteration order.
	first := Fragment("work", cfg.Profiles["work"])
	for i := 0; i < 20; i++ {
		if !bytes.Equal(first, Fragment("work", cfg.Profiles["work"])) {
			t.Fatal("fragment output is not stable")
		}
	}
}

func TestBlockGolden(t *testing.T) {
	cfg := sampleConfig()
	golden(t, "block.gitconfig", Block(cfg, false))
	cfg.StrictIdentity = boolPtr(false)
	if bytes.Contains(Block(cfg, false), []byte("useConfigOnly")) {
		t.Error("non-strict block must not set useConfigOnly")
	}
}

func TestBlockAddsResolvedSymlinkVariant(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	_ = os.Mkdir(real, 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	conds := RuleConditions(config.Rule{Profile: "p", Dirs: []string{link}})
	if len(conds) != 2 || conds[0] != "gitdir:"+link+"/" || !strings.HasSuffix(conds[1], "/real/") {
		t.Errorf("conditions = %q", conds)
	}
}

func TestSSHCommand(t *testing.T) {
	t.Setenv("HOME", "/home/k")
	if got := SSHCommand("~/.ssh/id_work"); got != "ssh -i ~/.ssh/id_work -o IdentitiesOnly=yes" {
		t.Errorf("plain: %q", got)
	}
	if got := SSHCommand("~/My Keys/id"); got != "ssh -i '/home/k/My Keys/id' -o IdentitiesOnly=yes" {
		t.Errorf("spaces: %q", got)
	}
	if k, ok := SSHKeyFromCommand("ssh -i ~/.ssh/id_work -o IdentitiesOnly=yes"); !ok || k != "~/.ssh/id_work" {
		t.Errorf("round trip: %q %v", k, ok)
	}
	if _, ok := SSHKeyFromCommand("ssh -i ~/.ssh/id -o ProxyJump=bastion"); ok {
		t.Error("extra options must not be parsed as a plain key")
	}
	if _, ok := SSHKeyFromCommand("ssh -i '/a b/id' -o IdentitiesOnly=yes"); ok {
		t.Error("quoted path must be kept verbatim")
	}
}

func TestQuoteValue(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"":            `""`,
		"two words":   `"two words"`,
		"a#b":         `"a#b"`,
		"a;b":         `"a;b"`,
		`say "hi"`:    `"say \"hi\""`,
		`C:\path`:     `"C:\\path"`,
		"line\nbreak": `"line\nbreak"`,
	}
	for in, want := range cases {
		if got := QuoteValue(in); got != want {
			t.Errorf("QuoteValue(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSplitKey(t *testing.T) {
	cases := []struct{ in, s, sub, k string }{
		{"pull.rebase", "pull", "", "rebase"},
		{"remote.origin.url", "remote", "origin", "url"},
		{"url.git@github.com:.insteadOf", "url", "git@github.com:", "insteadOf"},
		{"includeIf.gitdir:~/a.b/.path", "includeIf", "gitdir:~/a.b/", "path"},
	}
	for _, c := range cases {
		s, sub, k := SplitKey(c.in)
		if s != c.s || sub != c.sub || k != c.k {
			t.Errorf("SplitKey(%q) = %q %q %q", c.in, s, sub, k)
		}
	}
}

const testBlock = BeginMarker + "\n[user]\n\tuseConfigOnly = true\n" + EndMarker + "\n"

func TestSplice(t *testing.T) {
	oldBlock := BeginMarker + "\n[includeIf \"gitdir:~/old/\"]\n\tpath = x\n" + EndMarker + "\n"
	cases := []struct {
		name, in, want string
	}{
		{"empty file", "", testBlock},
		{"no block", "[core]\n\teditor = vim\n", "[core]\n\teditor = vim\n\n" + testBlock},
		{"no trailing newline", "[core]\n\teditor = vim", "[core]\n\teditor = vim\n\n" + testBlock},
		{"block at start", oldBlock + "[core]\n\teditor = vim\n", testBlock + "[core]\n\teditor = vim\n"},
		{"block in middle", "[a]\n\tb = c\n\n" + oldBlock + "\n[d]\n\te = f\n", "[a]\n\tb = c\n\n" + testBlock + "\n[d]\n\te = f\n"},
		{"block at end", "[a]\n\tb = c\n\n" + oldBlock, "[a]\n\tb = c\n\n" + testBlock},
		{"end marker without newline", "[a]\n" + strings.TrimSuffix(oldBlock, "\n"), "[a]\n" + testBlock},
		{"crlf file", "[a]\r\n\tb = c\r\n", "[a]\r\n\tb = c\r\n\r\n" + strings.ReplaceAll(testBlock, "\n", "\r\n")},
		{"crlf block", "[a]\r\n" + strings.ReplaceAll(oldBlock, "\n", "\r\n") + "[z]\r\n",
			"[a]\r\n" + strings.ReplaceAll(testBlock, "\n", "\r\n") + "[z]\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Splice([]byte(c.in), []byte(testBlock))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, c.want)
			}
			again, err := Splice(got, []byte(testBlock))
			if err != nil || !bytes.Equal(again, got) {
				t.Errorf("splice is not idempotent: %q", again)
			}
		})
	}
}

func TestSpliceCorrupt(t *testing.T) {
	for name, in := range map[string]string{
		"missing end":   "[a]\n" + BeginMarker + "\n[b]\n",
		"missing begin": "[a]\n" + EndMarker + "\n",
		"two blocks":    testBlock + testBlock,
		"nested begin":  BeginMarker + "\n" + testBlock,
	} {
		if _, err := Splice([]byte(in), []byte(testBlock)); !errors.Is(err, ErrCorruptBlock) {
			t.Errorf("%s: expected ErrCorruptBlock, got %v", name, err)
		}
	}
}

func TestExtractAndRemoveBlock(t *testing.T) {
	in := "[a]\n\tb = c\n\n" + testBlock
	blk, ok, err := ExtractBlock([]byte(in))
	if err != nil || !ok || string(blk) != testBlock {
		t.Fatalf("ExtractBlock = %q %v %v", blk, ok, err)
	}
	out, ok, err := RemoveBlock([]byte(in))
	if err != nil || !ok || string(out) != "[a]\n\tb = c\n" {
		t.Fatalf("RemoveBlock = %q %v %v", out, ok, err)
	}
}
