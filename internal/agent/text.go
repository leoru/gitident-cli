package agent

import "github.com/leoru/gitident-cli/internal/paths"

func claudeSkill() []byte {
	return []byte(`---
name: gitident
description: Check and fix the git identity (author name, email, signing key) before committing, tagging, merging, rebasing or pulling, and when git reports "Author identity unknown" or a signing failure. This machine manages git identities with gitident.
---
` + ownMarker + `

` + Instructions())
}

func cursorRule() []byte {
	return []byte(`---
description: Git identity rules (gitident) for commits, tags, merges and pulls
alwaysApply: true
---
` + ownMarker + `

` + Instructions())
}

// Instructions is the agent guidance, also printed by `gitident agent instructions`.
func Instructions() string {
	return `# Git identity (` + paths.AppName + `)

This machine manages git identities with gitident: every repository gets its
author name, email and signing key from a profile in
~/.config/gitident/profiles.yaml, chosen by directory, remote URL or an
explicit pin.

Before you commit, tag, merge, rebase, cherry-pick or pull, run:

    gitident preflight --json

- ` + "`\"ok\": true`" + `: go ahead.
- Otherwise every entry in ` + "`problems`" + ` has a ` + "`code`" + `, a ` + "`message`" + ` and a ` + "`fix`" + `.
  Exit status 1 means the identity is missing or wrong; 2 means only signing
  would fail.

Rules:

- Never set the identity yourself: no ` + "`git config user.name/user.email`" + `, no
  ` + "`git -c user.email=…`" + `, no GIT_AUTHOR_*/GIT_COMMITTER_* variables.
- Never turn signing off (` + "`--no-gpg-sign`" + `, ` + "`-c commit.gpgsign=false`" + `) unless
  the user says so.
- ` + "`no_identity`" + `, ` + "`unknown_email`" + `, ` + "`unmatched`" + `: ask the user which profile the
  repository belongs to, then run ` + "`gitident use <profile>`" + ` in it (add ` + "`--save`" + `
  to record it in profiles.yaml).
- ` + "`mismatch`" + `, ` + "`stale_copy`" + `: run ` + "`gitident sync`" + `, then preflight again.
- ` + "`signing_failed`" + `: the key needs a passphrase nobody can type here. Ask the
  user to unlock it (the fix says how), then retry.
- After cloning a repository, run ` + "`gitident preflight`" + ` in it before the first
  commit.

More: ` + "`gitident which --json`" + ` shows the effective identity and where it comes
from; ` + "`gitident check -q`" + ` lists repositories with identity problems.
`
}
