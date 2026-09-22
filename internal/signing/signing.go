// Package signing checks whether git can sign a commit without asking anyone
// for a passphrase, the way an unattended agent or script needs it to.
package signing

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/leoru/gitident-cli/internal/paths"
)

// Timeout bounds one test signature.
var Timeout = 10 * time.Second

// Settings are the effective git settings that decide how commits are signed.
type Settings struct {
	GPGSign    bool   // commit.gpgsign
	Format     string // gpg.format ("" = openpgp)
	SigningKey string // user.signingkey
	Program    string // gpg.<format>.program or gpg.program ("" = default)
	Committer  string // "Name <email>", git's default key id for openpgp
}

// Result of a test signature.
type Result struct {
	Enabled bool   `json:"enabled"`          // commits are signed at all
	Format  string `json:"format,omitempty"` // openpgp, ssh or x509
	Key     string `json:"key,omitempty"`
	OK      bool   `json:"ok"`               // signing works (or is not enabled)
	Checked bool   `json:"checked"`          // a test signature was attempted
	Detail  string `json:"detail,omitempty"` // why it failed, or what was not checked
}

// Check makes a test signature with the same program and key git would use.
// The program runs without a controlling terminal and with pinentry and
// askpass disabled, so a key that needs a passphrase fails instead of hanging.
func Check(s Settings) Result {
	r := Result{Enabled: s.GPGSign, Format: s.Format, Key: s.SigningKey, OK: true}
	if r.Format == "" {
		r.Format = "openpgp"
	}
	if !s.GPGSign {
		return r
	}
	switch r.Format {
	case "openpgp":
		key := s.SigningKey
		if key == "" {
			key = s.Committer
		}
		prog := s.Program
		if prog == "" {
			prog = "gpg"
		}
		r.Checked = true
		r.fail(run(prog, []string{"--status-fd=2", "--batch", "--no-tty", "--pinentry-mode", "error", "-bsau", key}, nil))
	case "ssh":
		prog := s.Program
		if prog == "" {
			prog = "ssh-keygen"
		}
		key := s.SigningKey
		if key == "" {
			r.OK, r.Detail = false, "gpg.format is ssh but user.signingkey is not set"
			return r
		}
		keyFile := paths.Expand(key)
		if lit := literalSSHKey(key); lit != "" {
			f, err := os.CreateTemp("", "gitident-sshkey-")
			if err != nil {
				r.OK, r.Detail = false, err.Error()
				return r
			}
			defer os.Remove(f.Name())
			_, _ = f.WriteString(lit + "\n")
			f.Close()
			keyFile = f.Name()
		}
		r.Checked = true
		r.fail(run(prog, []string{"-Y", "sign", "-n", "git", "-f", keyFile}, []string{"SSH_ASKPASS_REQUIRE=never"}))
	default:
		r.Detail = r.Format + " signing is not checked"
	}
	return r
}

func (r *Result) fail(err error) {
	if err != nil {
		r.OK, r.Detail = false, err.Error()
	}
}

func literalSSHKey(k string) string {
	if strings.HasPrefix(k, "key::") {
		return strings.TrimPrefix(k, "key::")
	}
	for _, p := range []string{"ssh-", "ecdsa-", "sk-"} {
		if strings.HasPrefix(k, p) {
			return k
		}
	}
	return ""
}

// run signs a short test payload; the error carries the program's own message.
func run(prog string, args, env []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Stdin = strings.NewReader("gitident signing test\n")
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "DISPLAY=", "GPG_TTY=")
	// No controlling terminal: passphrase prompts fail instead of waiting.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// On timeout kill the whole session (gpg may have started helpers).
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return errors.New(prog + " did not finish within " + Timeout.String() + " (probably waiting for a passphrase)")
	}
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) || errors.Is(err, os.ErrNotExist) {
			return errors.New(prog + " not found")
		}
		return errors.New(summarize(stderr.String(), err))
	}
	return nil
}

// summarize keeps the human-readable lines of a signing program's stderr,
// dropping gpg's machine-readable "[GNUPG:]" status lines.
func summarize(stderr string, err error) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "[GNUPG:]") && (len(lines) == 0 || lines[len(lines)-1] != l) {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return err.Error()
	}
	if len(lines) > 3 {
		lines = lines[:3]
	}
	return strings.Join(lines, "; ")
}
