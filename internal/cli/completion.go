package cli

import (
	"fmt"
)

const completionUsage = `usage: gitident completion bash|zsh|fish

Print a shell completion script. Profile names are completed for "use".

  bash:  echo 'source <(gitident completion bash)' >> ~/.bashrc
  zsh:   gitident completion zsh > "${fpath[1]}/_gitident"
  fish:  gitident completion fish > ~/.config/fish/completions/gitident.fish

The bash script also completes "git ident ..." when git's completion is loaded.
`

const bashCompletion = `# bash completion for gitident
_gitident_complete() {
    local cur=$1 cmd=$2 pos=$3
    local cmds="init import sync which preflight check use unuse apply unapply agent doctor uninstall completion version help"
    if [ "$pos" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "$cmds" -- "$cur") )
        return
    fi
    case "$cur" in
        -*)
            local flags=""
            case "$cmd" in
                init) flags="--force" ;;
                import) flags="--write --merge --force --from-gitkraken --interactive" ;;
                sync) flags="--dry-run --no-prune" ;;
                which) flags="--json" ;;
                check) flags="--json -q" ;;
                use) flags="--save" ;;
                apply|unapply) flags="--all --force --dry-run" ;;
                preflight) flags="--json --no-sign" ;;
                agent) flags="--scope --dry-run" ;;
                uninstall) flags="--dry-run" ;;
            esac
            COMPREPLY=( $(compgen -W "$flags" -- "$cur") )
            return ;;
    esac
    case "$cmd" in
        use)
            if [ "$pos" -eq 2 ]; then
                COMPREPLY=( $(compgen -W "$(gitident __profiles 2>/dev/null)" -- "$cur") )
            else
                COMPREPLY=( $(compgen -d -- "$cur") )
            fi ;;
        agent)
            if [ "$pos" -eq 2 ]; then
                COMPREPLY=( $(compgen -W "install uninstall status instructions" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "claude" -- "$cur") )
            fi ;;
        which|preflight|unuse|apply|unapply|check|import) COMPREPLY=( $(compgen -d -- "$cur") ) ;;
        completion) COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ) ;;
        help) COMPREPLY=( $(compgen -W "init import sync which preflight check use unuse apply unapply agent doctor uninstall completion version" -- "$cur") ) ;;
    esac
}
_gitident() {
    _gitident_complete "${COMP_WORDS[COMP_CWORD]}" "${COMP_WORDS[1]}" "$COMP_CWORD"
}
complete -o filenames -F _gitident gitident
complete -o filenames -F _gitident git-ident
# "git ident <TAB>" via git's own completion (git-completion.bash).
_git_ident() {
    local i=1
    while [ $i -lt $COMP_CWORD ] && [ "${COMP_WORDS[$i]}" != "ident" ]; do i=$((i+1)); done
    _gitident_complete "${COMP_WORDS[COMP_CWORD]}" "${COMP_WORDS[$((i+1))]}" "$((COMP_CWORD - i))"
}
`

const zshCompletion = `#compdef gitident git-ident

_gitident_profiles() {
    local -a profiles
    profiles=(${(f)"$(gitident __profiles 2>/dev/null)"})
    _describe 'profile' profiles
}

_gitident() {
    local -a commands
    commands=(
        'init:write a commented sample profiles.yaml'
        'import:generate profiles.yaml from existing git configuration'
        'sync:render profiles into ~/.gitconfig'
        'which:show the identity git uses in a directory'
        'preflight:check that a commit here would work unattended'
        'check:audit every repository under the configured roots'
        'use:pin a repository to a profile'
        'unuse:remove a repository pin'
        'apply:copy a repository'"'"'s profile into its .git/config'
        'unapply:remove a profile copied by apply'
        'agent:set up AI coding agents to respect your identities'
        'doctor:diagnose the installation'
        'uninstall:remove the managed block and fragments'
        'completion:print a shell completion script'
        'version:print the version'
        'help:show help for a command'
    )
    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    case $words[2] in
        init) _arguments '--force[overwrite an existing file]' ;;
        import) _arguments '--write[write profiles.yaml]' '--merge[add new profiles and repos to the existing file]' \
                    '--force[overwrite the existing file]' '--from-gitkraken[also read GitKraken profiles]' \
                    '--interactive[prompt for profile names]' '*:root:_directories' ;;
        sync) _arguments '--dry-run[show changes, write nothing]' '--no-prune[keep stale fragments]' ;;
        which) _arguments '--json[machine-readable output]' '1:directory:_directories' ;;
        preflight) _arguments '--json[machine-readable output]' '--no-sign[skip the signing test]' '1:directory:_directories' ;;
        agent) _arguments '1:action:(install uninstall status instructions)' '2:agent:(claude)' \
                    '--scope[where to install]:scope:(user project local)' '--dry-run[show changes, write nothing]' ;;
        check) _arguments '--json[machine-readable output]' '-q[only print problems]' '*:root:_directories' ;;
        use) _arguments '--save[also add the repo to profiles.yaml]' '1:profile:_gitident_profiles' '2:directory:_directories' ;;
        unuse) _arguments '1:directory:_directories' ;;
        apply|unapply) _arguments '--all[every repository]' '--force[overwrite values changed by hand]' \
                    '--dry-run[show changes, write nothing]' '*:directory:_directories' ;;
        uninstall) _arguments '--dry-run[show changes, write nothing]' ;;
        completion) _values 'shell' bash zsh fish ;;
        help) _describe 'command' commands ;;
    esac
}

_gitident "$@"
`

const fishCompletion = `# fish completion for gitident
set -l cmds init import sync which preflight check use unuse apply unapply agent doctor uninstall completion version help
for bin in gitident git-ident
    complete -c $bin -f
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a init -d 'write a commented sample profiles.yaml'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a import -d 'generate profiles.yaml from existing git configuration'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a sync -d 'render profiles into ~/.gitconfig'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a which -d 'show the identity git uses in a directory'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a preflight -d 'check that a commit here would work unattended'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a agent -d 'set up AI coding agents to respect your identities'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a check -d 'audit every repository under the configured roots'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a use -d 'pin a repository to a profile'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a unuse -d 'remove a repository pin'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a apply -d "copy a repository's profile into its .git/config"
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a unapply -d 'remove a profile copied by apply'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a doctor -d 'diagnose the installation'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a uninstall -d 'remove the managed block and fragments'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a completion -d 'print a shell completion script'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a version -d 'print the version'
    complete -c $bin -n "not __fish_seen_subcommand_from $cmds" -a help -d 'show help for a command'

    complete -c $bin -n "__fish_seen_subcommand_from init" -l force -d 'overwrite an existing file'
    complete -c $bin -n "__fish_seen_subcommand_from import" -l write -d 'write profiles.yaml'
    complete -c $bin -n "__fish_seen_subcommand_from import" -l merge -d 'add new profiles and repos to the existing file'
    complete -c $bin -n "__fish_seen_subcommand_from import" -l force -d 'overwrite the existing file'
    complete -c $bin -n "__fish_seen_subcommand_from import" -l from-gitkraken -d 'also read GitKraken profiles'
    complete -c $bin -n "__fish_seen_subcommand_from import" -l interactive -d 'prompt for profile names'
    complete -c $bin -n "__fish_seen_subcommand_from import check which preflight unuse apply unapply" -a '(__fish_complete_directories)'
    complete -c $bin -n "__fish_seen_subcommand_from sync" -l dry-run -d 'show changes, write nothing'
    complete -c $bin -n "__fish_seen_subcommand_from preflight" -l json -d 'machine-readable output'
    complete -c $bin -n "__fish_seen_subcommand_from preflight" -l no-sign -d 'skip the signing test'
    complete -c $bin -n "__fish_seen_subcommand_from agent" -a 'install uninstall status instructions claude'
    complete -c $bin -n "__fish_seen_subcommand_from agent" -l scope -xa 'user project local' -d 'where to install'
    complete -c $bin -n "__fish_seen_subcommand_from apply unapply" -l all -d 'every repository'
    complete -c $bin -n "__fish_seen_subcommand_from apply unapply" -l force -d 'overwrite values changed by hand'
    complete -c $bin -n "__fish_seen_subcommand_from apply unapply" -l dry-run -d 'show changes, write nothing'
    complete -c $bin -n "__fish_seen_subcommand_from sync" -l no-prune -d 'keep stale fragments'
    complete -c $bin -n "__fish_seen_subcommand_from which check" -l json -d 'machine-readable output'
    complete -c $bin -n "__fish_seen_subcommand_from check" -s q -d 'only print problems'
    complete -c $bin -n "__fish_seen_subcommand_from use" -l save -d 'also add the repo to profiles.yaml'
    complete -c $bin -n "__fish_seen_subcommand_from use" -a '(gitident __profiles 2>/dev/null)' -d 'profile'
    complete -c $bin -n "__fish_seen_subcommand_from uninstall" -l dry-run -d 'show changes, write nothing'
    complete -c $bin -n "__fish_seen_subcommand_from completion" -a 'bash zsh fish'
end
`

func (a *App) cmdCompletion(args []string) error {
	fs := a.newFlags("completion", completionUsage)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("expected one of: bash, zsh, fish")
	}
	switch pos[0] {
	case "bash":
		fmt.Fprint(a.Stdout, bashCompletion)
	case "zsh":
		fmt.Fprint(a.Stdout, zshCompletion)
	case "fish":
		fmt.Fprint(a.Stdout, fishCompletion)
	default:
		return usageError("unsupported shell %q (expected bash, zsh or fish)", pos[0])
	}
	return nil
}
