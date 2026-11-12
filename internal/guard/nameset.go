//go:build unix

package guard

import (
	"slices"
	"strings"
)

func setOf(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}

	return set
}

func containsFold(names []string, s string) bool {
	return slices.ContainsFunc(names, func(name string) bool { return strings.EqualFold(name, s) })
}
