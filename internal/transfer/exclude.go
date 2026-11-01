package transfer

import (
	"fmt"
	"path"
	"strings"
)

// ValidateExcludes accepts slash-separated globs relative to the source root.
// Patterns without a slash match a basename at any depth. A matched directory
// is pruned; ** has the same meaning as * and does not cross a slash.
func ValidateExcludes(patterns []string) error {
	for _, pattern := range patterns {
		if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\\") {
			return fmt.Errorf("exclude must be a nonempty relative slash-separated glob: %q", pattern)
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == ".." {
				return fmt.Errorf("exclude must not contain ..: %q", pattern)
			}
		}
		if _, err := path.Match(strings.TrimSuffix(pattern, "/"), ""); err != nil {
			return fmt.Errorf("invalid exclude %q: %w", pattern, err)
		}
	}
	return nil
}

func excluded(rel string, directory bool, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "/") && !directory {
			continue
		}
		pattern = strings.TrimSuffix(pattern, "/")
		candidate := rel
		if !strings.Contains(pattern, "/") {
			candidate = path.Base(rel)
		}
		if match, _ := path.Match(pattern, candidate); match {
			return true
		}
	}
	return false
}
