package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LAW (issue #53): a check ends in a notification and nothing else —
// no purchase path. Enforced structurally: this package's only outbound
// HTTP call site is notify.go. Any new file reaching for net/http or an
// http.Client outside notify.go fails this test at review time.
func TestLawNotifyIsTheOnlyOutbound(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") || f == "notify.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if strings.Contains(src, `"net/http"`) || strings.Contains(src, "http.Client") ||
			strings.Contains(src, "http.Post") || strings.Contains(src, "http.Get") {
			t.Errorf("%s: outbound HTTP outside notify.go — a watch check must end in a notification and nothing else", f)
		}
	}
}
