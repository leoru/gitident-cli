package signing

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sshKey(t *testing.T, passphrase string) string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	key := filepath.Join(t.TempDir(), "id")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", passphrase, "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	return key
}

func TestNotEnabled(t *testing.T) {
	if r := Check(Settings{}); !r.OK || r.Checked || r.Enabled {
		t.Errorf("Check = %+v", r)
	}
}

func TestSSHUnencryptedKeySigns(t *testing.T) {
	key := sshKey(t, "")
	if r := Check(Settings{GPGSign: true, Format: "ssh", SigningKey: key}); !r.OK || !r.Checked {
		t.Errorf("Check = %+v", r)
	}
}

func TestSSHPassphraseFailsFast(t *testing.T) {
	key := sshKey(t, "secret passphrase")
	os.Unsetenv("SSH_AUTH_SOCK")
	start := time.Now()
	r := Check(Settings{GPGSign: true, Format: "ssh", SigningKey: key})
	if r.OK || !r.Checked {
		t.Errorf("Check = %+v", r)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v; should fail without waiting for a passphrase", d)
	}
}

func TestSSHMissingKey(t *testing.T) {
	r := Check(Settings{GPGSign: true, Format: "ssh"})
	if r.OK || !strings.Contains(r.Detail, "user.signingkey is not set") {
		t.Errorf("Check = %+v", r)
	}
}

func TestOpenPGPProgram(t *testing.T) {
	dir := t.TempDir()
	fail := filepath.Join(dir, "gpg-fail")
	os.WriteFile(fail, []byte("#!/bin/sh\necho '[GNUPG:] KEY_CONSIDERED x' >&2\necho 'gpg: signing failed: No pinentry' >&2\nexit 2\n"), 0o755)
	ok := filepath.Join(dir, "gpg-ok")
	os.WriteFile(ok, []byte("#!/bin/sh\ncat >/dev/null\necho sig\n"), 0o755)

	r := Check(Settings{GPGSign: true, SigningKey: "ABC", Program: fail})
	if r.OK || r.Detail != "gpg: signing failed: No pinentry" || r.Format != "openpgp" {
		t.Errorf("failing gpg: %+v", r)
	}
	if r := Check(Settings{GPGSign: true, SigningKey: "ABC", Program: ok}); !r.OK {
		t.Errorf("working gpg: %+v", r)
	}
	if r := Check(Settings{GPGSign: true, Program: filepath.Join(dir, "missing")}); r.OK || !strings.Contains(r.Detail, "not found") {
		t.Errorf("missing gpg: %+v", r)
	}

	Timeout = 500 * time.Millisecond
	defer func() { Timeout = 10 * time.Second }()
	slow := filepath.Join(dir, "gpg-slow")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o755)
	if r := Check(Settings{GPGSign: true, Program: slow}); r.OK || !strings.Contains(r.Detail, "did not finish") {
		t.Errorf("slow gpg: %+v", r)
	}
}
