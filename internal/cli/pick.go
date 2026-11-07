package cli

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/benenen/ah/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type connectionPicker struct {
	entries        map[string]config.Connection
	names, matches []string
	query          []rune
	selected       int
}

func (a *app) pickCommand() *cobra.Command {
	var tags []string
	cmd := &cobra.Command{Use: "pick [QUERY...]", Short: "Search saved connections interactively and press Enter to connect", RunE: func(cmd *cobra.Command, args []string) error {
		input, ok := cmd.InOrStdin().(*os.File)
		if !ok || !term.IsTerminal(int(input.Fd())) {
			return fmt.Errorf("pick requires interactive terminal input")
		}
		output, ok := cmd.ErrOrStderr().(*os.File)
		if !ok || !term.IsTerminal(int(output.Fd())) {
			return fmt.Errorf("pick requires an interactive terminal on stderr")
		}
		if os.Getenv("TERM") == "dumb" {
			return fmt.Errorf("pick requires a terminal supporting ANSI cursor control")
		}
		tags = normalizeTags(tags)
		for _, tag := range tags {
			if err := config.ValidateTag(tag); err != nil {
				return err
			}
		}
		query := strings.Join(args, " ")
		if len(query) > 4096 || strings.ContainsFunc(query, func(r rune) bool { return !unicode.IsPrint(r) }) {
			return fmt.Errorf("search must be printable text of at most 4096 bytes")
		}
		entries, err := a.connections()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return fmt.Errorf("no saved connections (create one with ah new)")
		}
		filter := connectionFilter{tags: tags}
		state := &connectionPicker{entries: entries, query: []rune(query)}
		for _, name := range sortedNames(entries) {
			if filter.match(name, entries[name]) {
				state.names = append(state.names, name)
			}
		}
		if len(state.names) == 0 {
			return fmt.Errorf("no connections match the selected tags")
		}
		state.filter()
		name, err := selectConnection(cmd.Context(), input, output, state)
		if err != nil {
			return err
		}
		if name == "" {
			return nil
		}
		return a.runConnect(cmd, []string{name})
	}}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "limit choices to connections carrying every tag (repeatable or comma-separated)")
	_ = cmd.RegisterFlagCompletionFunc("tag", a.completeTags(false))
	return cmd
}

func (p *connectionPicker) filter() {
	p.matches = nil
	p.selected = 0
	filter := connectionFilter{keywords: strings.Fields(string(p.query))}
	for _, name := range p.names {
		c := p.entries[name]
		address := c.User + "@" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
		if filter.match(name+" "+address, c) {
			p.matches = append(p.matches, name)
		}
	}
}

func (p *connectionPicker) move(delta int) {
	if len(p.matches) == 0 {
		return
	}
	p.selected = (p.selected + delta + len(p.matches)) % len(p.matches)
}

// pickText conservatively budgets two cells for non-ASCII runes so long Unicode
// fields cannot wrap over the next row. Non-printable configuration text is hidden.
func pickText(value string, cells int) string {
	var out strings.Builder
	for _, r := range value {
		if !unicode.IsPrint(r) {
			r = '?'
		}
		size := 1
		if r > 127 {
			size = 2
		}
		if cells < size {
			break
		}
		out.WriteRune(r)
		cells -= size
	}
	return out.String()
}

func (p *connectionPicker) render(columns, rows int) string {
	var out strings.Builder
	out.WriteString("\x1b[H\x1b[2J")
	if columns < 20 || rows < 5 {
		out.WriteString(pickText("Terminal too small; resize or press Esc", max(0, columns-1)))
		return out.String()
	}
	line := func(text string) { out.WriteString(pickText(text, columns-1)); out.WriteString("\r\n") }
	line(fmt.Sprintf("ah pick  %d/%d connections", len(p.matches), len(p.names)))
	line("Search: " + string(p.query))
	visible := rows - 4
	start := max(0, min(p.selected-visible/2, len(p.matches)-visible))
	end := min(start+visible, len(p.matches))
	if len(p.matches) == 0 {
		line("  No matches; edit the search to continue")
	}
	for i := start; i < end; i++ {
		name := p.matches[i]
		c := p.entries[name]
		marker := "  "
		if i == p.selected {
			marker = "> "
			out.WriteString("\x1b[7m")
		}
		text := fmt.Sprintf("%s%s  %s@%s  [%s]  %s", marker, name, c.User, net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), strings.Join(c.Tags, ","), c.Note)
		out.WriteString(pickText(text, columns-1))
		out.WriteString("\x1b[0m\r\n")
	}
	line("Type to search | Up/Down/Tab select | Enter connect | Esc cancel")
	return out.String()
}
