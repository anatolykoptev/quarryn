// Package dbguard holds only a fitness test (no production code).
package dbguard

// Anti-regrowth gate: every *_test.go file that reads PG_LIVE_DSN must pass it
// through go-kit's pgutil/dbtest guard, which refuses any server that holds a
// non-test database.
//
// Falsification: remove the kitdbtest.MustBeScratch call from any opener and
// this test fails, listing the file.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFitness_PGLiveDSNOpenersUseScratchGuard(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") || d.Name() == "fitness_test.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if strings.Contains(src, `Getenv("PG_LIVE_DSN")`) && !strings.Contains(src, "kitdbtest.MustBeScratch(") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("test files read PG_LIVE_DSN without kitdbtest.MustBeScratch:\n  %s", strings.Join(offenders, "\n  "))
	}
}
