package cli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/benenen/ah/internal/config"
)

func TestConnectionTagsNotesAndFilters(t *testing.T) {
	p := filepath.Join(t.TempDir(), "connections.toml")
	run := func(args ...string) (string, error) { return execute(t, append([]string{"--config", p}, args...)...) }
	mustRun := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	mustRun("new", "web1", "--host", "web1.example.com", "--user", "alice", "--tag", "prod,shop", "--tag", "Prod", "--note", "订单服务 主节点")
	mustRun("new", "web2", "--host", "web2.example.com", "--user", "alice", "--tag", "dev", "--tag", "shop")
	mustRun("new", "db", "--host", "10.0.0.5", "--user", "postgres", "--tag", "prod")

	got, err := config.Store{Path: p}.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["web1"].Tags, []string{"prod", "shop"}) || got["web1"].Note != "订单服务 主节点" {
		t.Fatalf("tags/note not saved: %+v", got["web1"])
	}

	names := func(args ...string) []string {
		t.Helper()
		var rows []struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(mustRun(append([]string{"ls", "--json"}, args...)...)), &rows); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			if r.Tags == nil {
				t.Fatal("tags must encode as an array")
			}
			out = append(out, r.Name)
		}
		return out
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"db", "web1", "web2"}},
		{[]string{"--tag", "PROD"}, []string{"db", "web1"}},
		{[]string{"--tag", "prod", "--tag", "shop"}, []string{"web1"}},
		{[]string{"--tag", "prod,shop"}, []string{"web1"}},
		{[]string{"--tag", "missing"}, []string{}},
		{[]string{"WEB"}, []string{"web1", "web2"}},
		{[]string{"10.0.0"}, []string{"db"}},
		{[]string{"主节点"}, []string{"web1"}},
		{[]string{"web", "dev"}, []string{"web2"}},
		{[]string{"--tag", "shop", "web2.example"}, []string{"web2"}},
	} {
		if got := names(tc.args...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ls %v = %v, want %v", tc.args, got, tc.want)
		}
	}
	table := mustRun("ls", "--tag", "prod")
	if !strings.Contains(table, "TAGS") || !strings.Contains(table, "prod,shop") || strings.Contains(table, "web2") {
		t.Fatalf("table:\n%s", table)
	}

	mustRun("edit", "web2", "--add-tag", "staging,dev", "--remove-tag", "DEV", "--note", "test box")
	got, _ = config.Store{Path: p}.Load()
	if !reflect.DeepEqual(got["web2"].Tags, []string{"shop", "staging", "dev"}) || got["web2"].Note != "test box" {
		t.Fatalf("edit tags: %+v", got["web2"])
	}
	mustRun("edit", "web2", "--tag", "qa", "--note", "")
	got, _ = config.Store{Path: p}.Load()
	if !reflect.DeepEqual(got["web2"].Tags, []string{"qa"}) || got["web2"].Note != "" {
		t.Fatalf("replace tags: %+v", got["web2"])
	}
	mustRun("edit", "web2", "--clear-tags")
	got, _ = config.Store{Path: p}.Load()
	if len(got["web2"].Tags) != 0 || got["web1"].Note == "" {
		t.Fatalf("clear tags: %+v", got)
	}

	for _, args := range [][]string{
		{"edit", "web1", "--remove-tag", "nope"},
		{"edit", "web1", "--add-tag", "has space"},
		{"edit", "web1", "--tag", "x", "--add-tag", "y"},
		{"edit", "web1", "--clear-tags", "--remove-tag", "prod"},
		{"edit", "web1", "--note", "line1\nline2"},
		{"edit", "web1", "--note", strings.Repeat("长", config.MaxNoteLength+1)},
		{"new", "bad", "--host", "h", "--user", "u", "--tag", "-lead"},
	} {
		if out, err := run(args...); err == nil {
			t.Errorf("%v accepted:\n%s", args, out)
		}
	}
	got, _ = config.Store{Path: p}.Load()
	if !reflect.DeepEqual(got["web1"].Tags, []string{"prod", "shop"}) {
		t.Fatalf("failed edit changed config: %+v", got["web1"])
	}

	inspected := mustRun("inspect", "web1")
	if !strings.Contains(inspected, `"tags": [`) || !strings.Contains(inspected, `"note": "订单服务 主节点"`) {
		t.Fatalf("inspect:\n%s", inspected)
	}

	out := mustRun("__complete", "ls", "--tag", "s")
	if !strings.Contains(out, "shop\n") || strings.Contains(out, "prod") {
		t.Fatalf("tag completion:\n%s", out)
	}
	out = mustRun("__complete", "edit", "db", "--remove-tag", "")
	if !strings.Contains(out, "prod\n") || strings.Contains(out, "shop") {
		t.Fatalf("remove-tag completion:\n%s", out)
	}
}

func TestConfigRejectsDuplicateTags(t *testing.T) {
	c := config.Connection{Host: "h", Port: 22, User: "u", Tags: []string{"prod", "PROD"}}
	if err := c.Validate(); err == nil {
		t.Fatal("duplicate tags accepted")
	}
	c.Tags = []string{"team/a", "k8s:prod", "生产"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
