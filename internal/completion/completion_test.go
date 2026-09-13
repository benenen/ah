package completion

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pkg/sftp"
)

func TestRemotePathsPreserveNamesAndDirectorySuffixes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a file.txt", "a中文.txt", "a$(literal).txt", "unrelated", "a\nunsafe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ok"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0700); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	server, err := sftp.NewServer(a, sftp.WithServerWorkingDirectory(dir))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(); _ = server.Close() }()
	client, err := sftp.NewClientPipe(b, b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(); _ = a.Close(); _ = b.Close(); <-done }()
	got, err := RemotePaths(client, "a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a file.txt", "a$(literal).txt", "adir/", "a中文.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	got, err = RemotePaths(client, "~/a")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"~/a file.txt", "~/a$(literal).txt", "~/adir/", "~/a中文.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("home paths: got %q want %q", got, want)
	}
	if _, err = RemotePaths(client, "missing/a"); err == nil {
		t.Fatal("missing parent must fail")
	}
	if _, err = RemotePaths(client, "~other/a"); err == nil {
		t.Fatal("~user must not expand locally")
	}
}
