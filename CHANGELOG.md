# Changelog

## 1.0.0

First stable release. The `profiles.yaml` format (`version: 1`), the command-line interface, the `--json` output of `which`, `check` and `preflight`, and preflight's problem codes and exit statuses are now stable: breaking changes to them will only come with a new major version.

- Build with Go 1.27 (`go 1.27.0` in go.mod; CI and releases follow it).

## 0.3.0

- `gitident preflight [dir] [--json]` checks that a commit would go through with the right identity without anyone at the keyboard, including a test signature with passphrase prompts disabled. Each problem has a code and a suggested fix; exit status 0 / 1 (identity) / 2 (signing).
- `gitident agent install [agent…]` sets up coding agents: Claude Code, OpenAI Codex, Cursor, Gemini CLI, GitHub Copilot CLI, Factory Droid and Windsurf (all detected ones by default). Each gets a pre-shell-command hook that blocks commits `preflight` would fail and blocks setting `user.name` / `user.email` by hand, plus guidance (a skill, rule file or managed section in AGENTS.md / GEMINI.md / copilot-instructions.md). User or project scope; `agent uninstall`, `status`, `list`, and `instructions` for other agents.
- README badges for CI, coverage (published by CI to the `badges` branch, no third-party service), release, Go version and license.
- `clone_hook: true` installs a post-checkout hook in git's template directory: every new clone reports the profile it gets (or that none applies), and gets its copy right away when `materialize` is on.

## 0.2.0

- `gitident apply` / `unapply` copy a repository's profile into its own `.git/config`, for dev containers and git clients that don't evaluate includeIf rules. `materialize: true` (top level or per profile) makes `sync` do this for every matching repository. Copies are kept current by `sync`, `use` and `unuse`; values changed by hand or set by something else are never overwritten without `--force`. `check` reports problems as `STALE`, and `doctor` and `uninstall` handle copies too.
- Scanning skips empty or broken `.git` directories instead of reporting them as repositories.

## 0.1.0

First release.

- `profiles.yaml` with profiles (name, email, signing key, GPG format, gpgsign, SSH key, extra keys, repos), rules (`dirs`, `remotes`) and strict identity mode.
- `sync` renders per-profile fragments and a managed `includeIf` block in the global gitconfig. It is idempotent, writes atomically, prunes stale fragments and supports `--dry-run`.
- `which`, `check` (with `--json`), `use` / `unuse` (with comment-preserving `--save`), `doctor`, `uninstall`.
- `import` from global gitconfig, includeIf fragments, per-repo local config and GitKraken profiles, with `--write`, `--merge`, `--force` and `--interactive`.
- Shell completion for bash (including `git ident`), zsh and fish.
