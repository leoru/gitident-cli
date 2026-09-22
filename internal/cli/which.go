package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leoru/gitident-cli/internal/config"
	"github.com/leoru/gitident-cli/internal/inspect"
	"github.com/leoru/gitident-cli/internal/paths"
)

const whichUsage = `usage: gitident which [dir] [--json]

Show the identity git uses in dir (default: the current directory): the
effective user.*, commit.gpgsign, gpg.format and core.sshCommand, where
user.email comes from, and which profile that is.

flags:
  --json   machine-readable output
`

type whichJSON struct {
	Dir            string            `json:"dir"`
	Repo           string            `json:"repo,omitempty"`
	Profile        string            `json:"profile,omitempty"`
	Via            string            `json:"via,omitempty"`
	Pinned         string            `json:"pinned,omitempty"`
	Expected       string            `json:"expected,omitempty"`
	ExpectedReason string            `json:"expected_reason,omitempty"`
	Strict         bool              `json:"strict"`
	CanCommit      bool              `json:"can_commit"`
	Values         map[string]string `json:"values"`
	Origins        map[string]string `json:"origins"`
}

func (a *App) cmdWhich(args []string) error {
	fs := a.newFlags("which", whichUsage)
	asJSON := fs.Bool("json", false, "")
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
	cfg, err := config.Load(paths.ConfigPath())
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			return err
		}
		cfg = nil
	}
	r, err := inspect.Inspect(cfg, dir)
	if err != nil {
		return err
	}
	if *asJSON {
		out := whichJSON{
			Dir: r.Dir, Repo: r.Repo, Profile: r.Profile, Via: r.Via, Pinned: r.Pinned,
			Expected: r.Expected.Profile, ExpectedReason: r.Expected.Reason,
			Strict: r.Strict, CanCommit: r.Email() != "" || !r.Strict,
			Values: map[string]string{}, Origins: r.Origins,
		}
		for _, k := range inspect.IdentityKeys {
			if v, ok := r.Values[k]; ok {
				out.Values[k] = v
			}
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	a.printWhich(cfg, r)
	return nil
}

func (a *App) printWhich(cfg *config.Config, r *inspect.Result) {
	row := func(label, value string) { a.printf("%-10s %s\n", label, value) }
	if r.Repo != "" {
		row("repo", paths.Contract(r.Repo))
	} else {
		row("dir", paths.Contract(r.Dir)+"  (not a git repository)")
	}

	emailOrigin := paths.Contract(r.Origins["user.email"])
	switch {
	case r.Email() == "" && r.Strict:
		row("profile", "NONE — commits will fail (user.useConfigOnly is set and no profile matched)")
	case r.Email() == "":
		row("profile", "NONE — git will guess an identity from the system")
	case r.Via == inspect.ViaFragment:
		row("profile", fmt.Sprintf("%s  (via %s)", r.Profile, emailOrigin))
	case r.Via == inspect.ViaEmail:
		row("profile", fmt.Sprintf("%s  (matched by email, not via rules; set in %s)", r.Profile, emailOrigin))
	default:
		note := "not in any profile"
		if cfg == nil {
			note = "no profiles.yaml"
		}
		row("profile", fmt.Sprintf("unknown — %s is %s (set in %s)", r.Email(), note, emailOrigin))
	}
	if len(r.Ambiguous) > 0 {
		row("", "(same email as profile "+strings.Join(r.Ambiguous, ", ")+")")
	}
	if r.Pinned != "" {
		row("pinned", r.Pinned+"  (include.path in "+paths.Contract(r.CommonDir)+"/config)")
	}
	if cfg != nil && r.Repo != "" {
		exp := "none — no rule or repos entry matches"
		if r.Expected.Profile != "" {
			exp = fmt.Sprintf("%s  (%s)", r.Expected.Profile, r.Expected.Reason)
		}
		row("expected", exp)
	}
	a.printf("\n")
	for _, k := range inspect.IdentityKeys {
		v, ok := r.Values[k]
		if !ok {
			v = "(unset)"
		}
		a.printf("  %-16s %s\n", k, v)
	}

	if cfg != nil && r.Repo != "" {
		want := r.Expected.Profile
		if r.Pinned != "" {
			want = r.Pinned
		}
		if want != "" && r.Profile != want && r.Email() != "" {
			a.printf("\nnote: profiles.yaml says %q but the effective identity is %q — run `gitident sync`, or look for user.* in %s\n",
				want, r.Profile, emailOrigin)
		}
		if want != "" && r.Email() == "" {
			a.printf("\nnote: profiles.yaml says %q but nothing is applied — run `gitident sync`\n", want)
		}
	}
}
