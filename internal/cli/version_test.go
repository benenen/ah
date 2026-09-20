package cli

import (
	"runtime"
	"testing"
)

func TestVersionCommandReportsBuildMetadata(t *testing.T) {
	goRuntime := runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH

	// A plain go build leaves the injected metadata empty, so the line keeps the
	// version and the runtime only.
	out, err := execute(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ah dev (" + goRuntime + ")\n"; out != want {
		t.Fatalf("version output %q, want %q", out, want)
	}

	savedVersion, savedCommit, savedDate := version, commit, buildDate
	version, commit, buildDate = "1.2.3", "abc1234", "2026-09-20T14:00:00Z"
	t.Cleanup(func() { version, commit, buildDate = savedVersion, savedCommit, savedDate })

	want := "ah 1.2.3 (commit abc1234, built 2026-09-20T14:00:00Z, " + goRuntime + ")\n"
	for _, name := range []string{"version", "v"} {
		out, err := execute(t, name)
		if err != nil {
			t.Fatal(err)
		}
		if out != want {
			t.Fatalf("%s output %q, want %q", name, out, want)
		}
	}
}
