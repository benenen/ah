package completion

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/benenen/ah/internal/transfer"
)

func LocalPaths(partial string) ([]string, error) {
	if strings.ContainsFunc(partial, unicode.IsControl) {
		return nil, fmt.Errorf("control characters in path")
	}
	if partial == "~" {
		return []string{"~/"}, nil
	}
	if strings.HasPrefix(partial, "~") && !strings.HasPrefix(partial, "~/") {
		return nil, fmt.Errorf("use ~/ or an absolute path")
	}
	displayDir, prefix := filepath.Split(partial)
	dir := displayDir
	if dir == "" {
		dir = "."
	}
	resolved, err := transfer.ResolveLocalPath(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || strings.ContainsFunc(name, unicode.IsControl) {
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(resolved, name)); err == nil {
				isDir = info.IsDir()
			}
		}
		candidate := displayDir + name
		if isDir {
			candidate += "/"
		}
		candidates = append(candidates, candidate)
	}
	sort.Strings(candidates)
	return candidates, nil
}
