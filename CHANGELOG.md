# Changelog

## Unreleased

First release.

- `profiles.yaml` with profiles (name, email, signing key, GPG format, gpgsign, SSH key, extra keys, repos), rules (`dirs`, `remotes`) and strict identity mode.
- `sync` renders per-profile fragments and a managed `includeIf` block in the global gitconfig. It is idempotent, writes atomically, prunes stale fragments and supports `--dry-run`.
- `which`, `check` (with `--json`), `use` / `unuse` (with comment-preserving `--save`), `doctor`, `uninstall`.
- `import` from global gitconfig, includeIf fragments, per-repo local config and GitKraken profiles, with `--write`, `--merge`, `--force` and `--interactive`.
- Shell completion for bash (including `git ident`), zsh and fish.
