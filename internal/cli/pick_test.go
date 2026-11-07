package cli

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/benenen/ah/internal/config"
)

func pickerFixture() *connectionPicker {
	entries := map[string]config.Connection{
		"alpha": {Host: "one.example.com", Port: 2222, User: "alice", Tags: []string{"Prod", "web"}, Note: "主服务", Password: "must-never-display"},
		"beta":  {Host: "10.0.0.2", Port: 22, User: "bob", Tags: []string{"prod", "db"}, Note: "数据库"},
		"gamma": {Host: "three.example.com", Port: 22, User: "carol", Tags: []string{"dev"}, Note: "测试机器"},
	}
	p := &connectionPicker{entries: entries, names: sortedNames(entries)}
	p.filter()
	return p
}

func TestPickerSearchFieldsAndMovement(t *testing.T) {
	p := pickerFixture()
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"ALPHA", []string{"alpha"}}, {"example.com:2222", []string{"alpha"}},
		{"alice@one.example.com", []string{"alpha"}}, {"10.0", []string{"beta"}},
		{"PROD", []string{"alpha", "beta"}}, {"prod db", []string{"beta"}},
		{"数据库", []string{"beta"}}, {"carol", []string{"gamma"}},
		{"missing", nil},
	} {
		p.query = []rune(tc.query)
		p.filter()
		if !reflect.DeepEqual(p.matches, tc.want) {
			t.Fatal(tc.query, p.matches)
		}
	}
	p.query = nil
	p.filter()
	p.move(-1)
	if p.matches[p.selected] != "gamma" {
		t.Fatal("up did not wrap")
	}
	p.move(1)
	if p.matches[p.selected] != "alpha" {
		t.Fatal("down did not wrap")
	}
	p.query = []rune("none")
	p.filter()
	p.move(1)
	if p.selected != 0 {
		t.Fatal("movement without matches")
	}
}

func TestPickerRenderClipsAndHidesCredentials(t *testing.T) {
	p := pickerFixture()
	screen := p.render(100, 8)
	if strings.Contains(screen, "must-never-display") || !strings.Contains(screen, "> alpha") || !strings.Contains(screen, "数据库") {
		t.Fatal("unexpected selector content")
	}
	clipped := pickText("中a文\x1b\n", 5)
	if clipped != "中a文" || !utf8.ValidString(clipped) {
		t.Fatal(clipped)
	}
	if strings.Contains(pickText("x\x1b[31m", 20), "\x1b") {
		t.Fatal("configuration injected terminal controls")
	}
	if !strings.Contains(p.render(10, 2), "Terminal") {
		t.Fatal("small terminal not handled")
	}
	p.query = []rune("none")
	p.filter()
	if !strings.Contains(p.render(80, 24), "No matches") {
		t.Fatal("missing empty-state message")
	}
}

func TestPickRejectsNoninteractiveInput(t *testing.T) {
	if _, err := execute(t, "pick"); err == nil || !strings.Contains(err.Error(), "interactive terminal input") {
		t.Fatal(err)
	}
}
