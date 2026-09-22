# Changelog

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
