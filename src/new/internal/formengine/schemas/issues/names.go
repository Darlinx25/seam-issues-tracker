package issues

import (
	"sort"
	"strings"
)

func Names() []string {
	entries, _ := schemaFS.ReadDir(".")
	var out []string
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".json") {
			out = append(out, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(out)
	return out
}
