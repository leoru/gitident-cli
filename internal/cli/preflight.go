package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/inspect"
	"github.com/leoru/gitident-cli/internal/paths"
	"github.com/leoru/gitident-cli/internal/signing"
)

const preflightUsage = `usage: gitident preflight [dir] [--json] [--no-sign]

Check that a commit in dir (default: the current directory) would go through
with the right identity, without anyone at the keyboard. Meant for scripts and
AI agents to run before committing:

  identity  git has a user.email, it belongs to a profile, and it is the
            profile that profiles.yaml says this repository should get
  signing   when commits are signed, a test signature succeeds without a
            passphrase prompt (gpg runs with pinentry disabled, ssh-keygen
            without a terminal)

Every problem comes with a code and a suggested fix. Exit status: 0 when a
commit would go through, 1 for an identity problem, 2 when only signing would
fail or block.

flags:
  --json      machine-readable output
  --no-sign   skip the signing test
`

// Preflight problem codes.
const (
	codeNotRepo       = "not_a_repo"
	codeNoConfig      = "no_config"
	codeNoIdentity    = "no_identity"
	codeUnknownEmail  = "unknown_email"
	codeMismatch      = "mismatch"
	codeUnmatched     = "unmatched"
	codeStaleCopy     = "stale_copy"
	codeSigningFailed = "signing_failed"
)

type preflightProblem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

type preflightReport struct {
	OK       bool               `json:"ok"`
	Dir      string             `json:"dir"`
	Repo     string             `json:"repo,omitempty"`
	Name     string             `json:"name,omitempty"`
	Email    string             `json:"email,omitempty"`
	Profile  string             `json:"profile,omitempty"`
	Expected string             `json:"expected,omitempty"`
	Signing  *signing.Result    `json:"signing,omitempty"`
	Problems []preflightProblem `json:"problems"`
}

// exitCode is 0 when a commit would work, 1 for identity problems, 2 for signing only.
func (p *preflightReport) exitCode() int {
	code := 0
	for _, pr := range p.Problems {
		if pr.Code == codeSigningFailed {
			if code == 0 {
				code = 2
			}
			continue
		}
		code = 1
	}
	return code
}

func (p *preflightReport) add(code, msg, fix string) {
	p.Problems = append(p.Problems, preflightProblem{code, msg, fix})
}

func (a *App) cmdPreflight(args []string) error {
	fs := a.newFlags("preflight", preflightUsage)
	asJSON := fs.Bool("json", false, "")
	noSign := fs.Bool("no-sign", false, "")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError("expected at most one directory")
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	rep, err := preflight(dir, !*noSign)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		a.printPreflight(rep)
	}
	if code := rep.exitCode(); code != 0 {
		return &exitError{code: code}
	}
	return nil
}

// preflight inspects dir and reports whether a commit there would succeed.
func preflight(dir string, checkSigning bool) (*preflightReport, error) {
	cfg, err := config.Load(paths.ConfigPath())
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return nil, err
	}
	if cfg != nil && !cfg.Validate().OK() {
		cfg = nil // matching against a broken config would mislead
	}
	r, err := inspect.Inspect(cfg, dir)
	if err != nil {
		return nil, err
	}
	rep := &preflightReport{Dir: r.Dir, Repo: r.Repo, Name: r.Values["user.name"], Email: r.Email(),
		Profile: r.Profile, Problems: []preflightProblem{}}
	if r.Repo == "" {
		rep.add(codeNotRepo, paths.Contract(r.Dir)+" is not inside a git repository", "")
		return rep, nil
	}
	if cfg == nil {
		rep.add(codeNoConfig, fmt.Sprintf("%s is missing or invalid, so the identity cannot be verified", paths.Contract(paths.ConfigPath())),
			"ask the user; `gitident doctor` shows what is wrong")
	} else {
		identityProblems(cfg, r, rep)
	}
	if checkSigning && rep.Email != "" {
		res := signing.Check(signingSettings(r))
		rep.Signing = &res
		if !res.OK {
			rep.add(codeSigningFailed, fmt.Sprintf("commits here are signed (%s key %s), but a test signature failed: %s", res.Format, res.Key, res.Detail),
				signingFix(res))
		}
	}
	rep.OK = len(rep.Problems) == 0
	return rep, nil
}

func identityProblems(cfg *config.Config, r *inspect.Result, rep *preflightReport) {
	rep.Expected = r.TargetProfile()
	it := classify(cfg, r)
	if problem := copyProblem(cfg, r); problem != "" && (it.Status == statusOK || r.Materialized != "") {
		rep.add(codeStaleCopy, problem, "gitident sync (or `gitident apply --force` if values were changed by hand; ask the user first)")
		return
	}
	useFix := "ask the user which profile this repository belongs to, then run `gitident use <profile>` here (profiles: " +
		strings.Join(cfg.ProfileNames(), ", ") + ")"
	switch it.Status {
	case statusNoIdent:
		if it.Expected != "" {
			rep.add(codeNoIdentity, it.Detail, "gitident sync")
		} else {
			rep.add(codeNoIdentity, "no profile applies to this repository, so git has no user.email", useFix)
		}
	case statusUnknown:
		rep.add(codeUnknownEmail, "user.email is "+it.Detail+", which is not in any gitident profile", useFix)
	case statusMismatch:
		rep.add(codeMismatch, it.Detail, "gitident sync; if it persists, remove user.* from the file shown")
	case statusOK:
		// A profile's email arrived without any rule, repos entry or pin
		// choosing it (typically a global user.email): probably a guess.
		if it.Expected == "" {
			rep.add(codeUnmatched, fmt.Sprintf("no profile applies to this repository; user.email %s comes from %s", r.Email(), paths.Contract(r.Origins["user.email"])),
				useFix)
		}
	}
}

func signingSettings(r *inspect.Result) signing.Settings {
	s := signing.Settings{
		GPGSign:    isTrueValue(r.Values["commit.gpgsign"]),
		Format:     r.Values["gpg.format"],
		SigningKey: r.Values["user.signingkey"],
		Committer:  fmt.Sprintf("%s <%s>", r.Values["user.name"], r.Email()),
	}
	format := s.Format
	if format == "" {
		format = "openpgp"
	}
	for _, k := range []string{"gpg." + format + ".program", "gpg.program"} {
		if format == "ssh" && k == "gpg.program" {
			break // gpg.program only applies to openpgp
		}
		if v, ok, _ := gitx.Config(r.Dir, k); ok && v != "" {
			s.Program = v
			break
		}
	}
	return s
}

func signingFix(res signing.Result) string {
	switch res.Format {
	case "ssh":
		return fmt.Sprintf("ask the user to load the key into ssh-agent (`ssh-add %s`, without .pub); do not disable signing unless the user agrees",
			strings.TrimSuffix(res.Key, ".pub"))
	default:
		return fmt.Sprintf("ask the user to unlock the GPG key in a terminal (`echo test | gpg --clearsign -u %s >/dev/null`); do not disable signing unless the user agrees", res.Key)
	}
}

func isTrueValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

func (a *App) printPreflight(rep *preflightReport) {
	if rep.Email != "" {
		who := fmt.Sprintf("%s <%s>", rep.Name, rep.Email)
		if rep.Profile != "" {
			who += fmt.Sprintf("  (profile %s)", rep.Profile)
		}
		a.printf("identity  %s\n", who)
	}
	if s := rep.Signing; s != nil {
		switch {
		case !s.Enabled:
			a.printf("signing   off\n")
		case s.OK && s.Checked:
			a.printf("signing   %s key %s signs without a prompt\n", s.Format, s.Key)
		case s.OK:
			a.printf("signing   %s (%s)\n", s.Format, s.Detail)
		}
	}
	if rep.OK {
		a.printf("ok: a commit here would go through\n")
		return
	}
	for _, p := range rep.Problems {
		a.printf("FAIL %s: %s\n", p.Code, p.Message)
		if p.Fix != "" {
			a.printf("     → %s\n", p.Fix)
		}
	}
}
