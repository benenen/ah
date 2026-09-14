package cli

import (
	"bytes"
	"context"
	"fmt"
	"github.com/benenen/ah/internal/config"
	"sort"
	"strings"
	"time"

	"github.com/benenen/ah/internal/completion"
	"github.com/benenen/ah/internal/sshclient"
	"github.com/spf13/cobra"
)

func (a *app) completeNames(_ *cobra.Command, args []string, partial string) ([]string, cobra.ShellCompDirective) {
	directive := cobra.ShellCompDirectiveNoFileComp
	if len(args) > 0 {
		return nil, directive
	}
	m, err := a.connections()
	if err != nil {
		return nil, directive
	}
	var names []string
	for _, name := range sortedNames(m) {
		if strings.HasPrefix(name, partial) {
			names = append(names, name)
		}
	}
	return names, directive
}
func (a *app) completeRemote(cmd *cobra.Command, args []string, partial string) ([]string, cobra.ShellCompDirective) {
	directive := cobra.ShellCompDirectiveNoFileComp
	if len(args) > 1 {
		return nil, directive
	}
	name, p, remote := strings.Cut(partial, ":")
	if !remote || config.ValidateName(name) != nil {
		names, _ := a.completeNames(cmd, nil, partial)
		for i := range names {
			names[i] += ":"
			directive |= cobra.ShellCompDirectiveNoSpace
		}
		local, err := completion.LocalPaths(partial)
		if err == nil {
			for _, candidate := range local {
				if strings.HasSuffix(candidate, "/") {
					directive |= cobra.ShellCompDirectiveNoSpace
				}
			}
			names = append(names, local...)
		}
		sort.Strings(names)
		return names, directive
	}
	c, err := a.connection(name)
	if err != nil {
		return nil, directive
	}
	timeout := min(a.timeout, 3*time.Second)
	if timeout <= 0 {
		return nil, directive
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	// No callbacks: pressing Tab must never prompt, save host keys or echo diagnostics.
	opts := a.connectionSSHOptions(cmd, name, c, false)
	opts.Timeout = timeout
	client, err := sshclient.Dial(ctx, c, opts)
	if err != nil {
		return nil, directive
	}
	defer client.Close()
	sftpClient, err := client.SFTP()
	if err != nil {
		return nil, directive
	}
	defer sftpClient.Close()
	paths, err := completion.RemotePaths(sftpClient, p)
	if err != nil {
		return nil, directive
	}
	for i := range paths {
		if strings.HasSuffix(paths[i], "/") {
			directive |= cobra.ShellCompDirectiveNoSpace
		}
		paths[i] = name + ":" + paths[i]
	}
	return paths, directive
}
func completionCommand() *cobra.Command {
	return &cobra.Command{Use: "completion [bash|zsh|fish]", Short: "Print a shell completion script", Args: cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs), ValidArgs: []string{"bash", "zsh", "fish"}, RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "fish" {
			return cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
		}
		if args[0] == "bash" {
			if err := cmd.Root().GenBashCompletionV2(cmd.OutOrStdout(), true); err != nil {
				return err
			}
			_, err := fmt.Fprint(cmd.OutOrStdout(), bashStandaloneFallback)
			return err
		}
		var script bytes.Buffer
		if err := cmd.Root().GenZshCompletion(&script); err != nil {
			return err
		}
		// _describe consumes a backslash escape layer before shell quoting.
		// Preserve literal remote backslashes before Cobra escapes colons.
		generated := strings.ReplaceAll(script.String(), `comp=${comp//:/\\:}`, `comp=${comp//\\/\\\\}`+"\n            "+`comp=${comp//:/\\:}`)
		generated = strings.Replace(generated, `    # Use eval to handle any environment variables and such`, `    # Balance only the current opening quote in the completion request.
    if [[ "${lastParam[1]}" == "'" || "${lastParam[1]}" == '"' ]] && [[ "$lastChar" != "${lastParam[1]}" ]]; then
        requestComp+="${lastParam[1]}"
    fi
    # Use eval to handle any environment variables and such`, 1)
		_, err := fmt.Fprint(cmd.OutOrStdout(), generated)
		return err
	}}
}

// Cobra's Bash 3 fallback still assumes bash-completion is installed. Keep its
// generated quoting/directive handling, but reconstruct words locally when that
// optional package is absent. Colon and equals are Bash wordbreaks, not ah args.
const bashStandaloneFallback = `
if ! declare -F _get_comp_words_by_ref >/dev/null 2>&1; then
    __ah_init_completion() {
        COMPREPLY=()
        words=()
        local i token previous='' index
        for ((i=0; i<${#COMP_WORDS[@]}; i++)); do
            token=${COMP_WORDS[i]}
            if ((i > 0)) && [[ "$token" == : || "$token" == = || "$previous" == : || "$previous" == = ]]; then
                index=$((${#words[@]}-1))
                words[index]+="$token"
            else
                words+=("$token")
                index=$((${#words[@]}-1))
            fi
            if ((i == COMP_CWORD)); then cword=$index; fi
            previous=$token
        done
        cur=${words[cword]}
        # Balance the current opening quote for Cobra's request only. Readline
        # retains the original quote and closes it after a unique completion.
        __ah_quote=''
        if [[ "${cur:0:1}" == "'" || "${cur:0:1}" == '"' ]]; then
            __ah_quote=${cur:0:1}
            words[cword]+="$__ah_quote"
            cur=${cur:1}
        fi
        prev=''
        if ((cword > 0)); then prev=${words[cword-1]}; fi
    }
fi

# Bash 3 printf shell quoting can corrupt UTF-8 in ANSI-C output, which makes
# Cobra's compgen filtering discard otherwise valid remote filenames.
# The standalone path also needs quote-aware matching on newer Bash versions.
if ((BASH_VERSINFO[0] < 4)) || ! declare -F _get_comp_words_by_ref >/dev/null 2>&1; then
    __ah_handle_standard_completion_case() {
        COMPREPLY=()
        local candidate escaped special tab=$'\t'
        for candidate in "${completions[@]}"; do
            candidate=${candidate%%$tab*}
            escaped=$candidate
            for special in '\' ' ' '"' "'" '$' '` + "`" + `' '(' ')' '&' ';' '|' '<' '>' '*' '?' '[' ']' '{' '}' '!' '#'; do
                escaped=${escaped//"$special"/\\$special}
            done
            if [[ "$escaped" == "$cur"* || "$candidate" == "$cur"* ]]; then
                if [[ "$__ah_quote" == "'" ]]; then
                    escaped=${candidate//"'"/"'\\''"}
                elif [[ "$__ah_quote" == '"' ]]; then
                    escaped=$candidate
                    for special in '\' '"' '$' '` + "`" + `'; do
                        escaped=${escaped//"$special"/\\$special}
                    done
                    # History expansion is active inside double quotes in Bash 3.
                    escaped=${escaped//"!"/'"\!"'}
                fi
                COMPREPLY+=("$escaped")
            fi
        done
    }
    __ah_handle_special_char() {
        [[ -n "$__ah_quote" ]] && return 0
        local comp="$1" char="$2" word idx
        if [[ "$comp" == *${char}* && "$COMP_WORDBREAKS" == *${char}* ]]; then
            word=${comp%%"${comp##*${char}}"}
            idx=${#COMPREPLY[@]}
            while ((--idx >= 0)); do COMPREPLY[idx]=${COMPREPLY[idx]#"$word"}; done
        fi
    }
    # Bash 3 has no compopt to disable local filename fallback per invocation.
    if ((BASH_VERSINFO[0] < 4)); then complete -o nospace -F __start_ah ah; fi
fi
`
