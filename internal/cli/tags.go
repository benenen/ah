package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
)

// normalizeTags trims each tag and drops case-insensitive duplicates, keeping
// the first spelling, so `--tag prod,Prod` does not fail validation.
func normalizeTags(tags []string) []string {
	var out []string
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		dup := false
		for _, kept := range out {
			if strings.EqualFold(kept, tag) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, tag)
		}
	}
	return out
}

// removeTags drops each named tag (case-insensitive) and reports names that
// were not present, so a typo is not mistaken for success.
func removeTags(tags, remove []string) ([]string, error) {
	for _, r := range normalizeTags(remove) {
		found := false
		kept := tags[:0:0]
		for _, tag := range tags {
			if strings.EqualFold(tag, r) {
				found = true
				continue
			}
			kept = append(kept, tag)
		}
		if !found {
			return nil, fmt.Errorf("connection has no tag %q", r)
		}
		tags = kept
	}
	return tags, nil
}

// connectionFilter selects connections carrying every tag and containing every
// keyword (case-insensitive substring of name, host, user, note or a tag).
type connectionFilter struct {
	tags     []string
	keywords []string
}

func (f connectionFilter) match(name string, c config.Connection) bool {
	for _, tag := range f.tags {
		if !c.HasTag(tag) {
			return false
		}
	}
	fields := append([]string{name, c.Host, c.User, c.Note}, c.Tags...)
	for _, keyword := range f.keywords {
		k := strings.ToLower(keyword)
		hit := false
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field), k) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// completeTags offers tags already used in the configuration; `existing`
// restricts the candidates to the tags of the connection named in args[0].
func (a *app) completeTags(existing bool) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, partial string) ([]string, cobra.ShellCompDirective) {
		directive := cobra.ShellCompDirectiveNoFileComp
		m, err := a.connections()
		if err != nil {
			return nil, directive
		}
		seen := map[string]bool{}
		for name, c := range m {
			if existing && (len(args) == 0 || name != args[0]) {
				continue
			}
			for _, tag := range c.Tags {
				if strings.HasPrefix(strings.ToLower(tag), strings.ToLower(partial)) {
					seen[tag] = true
				}
			}
		}
		tags := make([]string, 0, len(seen))
		for tag := range seen {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		return tags, directive
	}
}
