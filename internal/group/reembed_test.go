package group

import (
	"context"
	"slices"
	"strings"
	"testing"
)

type reembedWrite struct {
	gid     int64
	vec     []float32
	members int
}

// fakeReembedStore plays the persistence side of Reembed — foldable
// titles are what the SQL filter (match <> 'exact', title <> ”) returns.
type fakeReembedStore struct {
	groups []embeddingGroup
	titles map[int64][]string
	writes []reembedWrite
}

func (f *fakeReembedStore) embeddingGroups(_ context.Context, afterID int64, limit int) ([]embeddingGroup, error) {
	var out []embeddingGroup
	for _, g := range f.groups {
		if g.ID > afterID {
			out = append(out, g)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeReembedStore) foldableTitles(_ context.Context, gid int64) ([]string, error) {
	return f.titles[gid], nil
}

func (f *fakeReembedStore) setEmbedding(_ context.Context, gid int64, vec []float32, members int) error {
	f.writes = append(f.writes, reembedWrite{gid: gid, vec: vec, members: members})
	return nil
}

func (f *fakeReembedStore) writesFor(gid int64) []reembedWrite {
	var out []reembedWrite
	for _, w := range f.writes {
		if w.gid == gid {
			out = append(out, w)
		}
	}
	return out
}

// reembedFixture builds the canned store: group 1 folds two member
// titles (plus an unembeddable one that must stay out), group 2 has
// nothing replayable and falls back to its label, group 3 has nothing
// embeddable at all.
func reembedFixture() (*fakeReembedStore, *recEmb) {
	st := &fakeReembedStore{
		groups: []embeddingGroup{
			{ID: 1, Label: "unused label"},
			{ID: 2, Label: "fallback headphones"},
			{ID: 3, Label: "x"},
		},
		titles: map[int64][]string{
			1: {"alpha one", "beta two", "SALE"}, // SALE fails embeddable() — never folded
			2: {},                                // nothing folded — label stands in
			3: {"x"},                             // one thin token — nothing to recompute
		},
	}
	rec := &recEmb{vecs: map[string][]float32{
		"passage: alpha one":           {1, 0, 0, 0},
		"passage: beta two":            {0, 1, 0, 0},
		"passage: fallback headphones": {0, 0, 1, 0},
	}}
	return st, rec
}

// The pass must visit every centroid-carrying group, rebuild each
// centroid from the member texts that originally folded it (passage
// role), write it back with the true sample count, and survive a group
// it cannot recompute.
func TestReembedRecomputesCentroids(t *testing.T) {
	st, rec := reembedFixture()
	stats, err := Reembed(context.Background(), st, rec, 2, 0)
	if err != nil {
		t.Fatalf("reembed: %v", err)
	}
	if stats.Scanned != 3 || stats.Updated != 2 || stats.Skipped != 1 {
		t.Fatalf("stats %+v, want scanned=3 updated=2 skipped=1", stats)
	}
	got := st.writesFor(1)
	if len(got) != 1 || !approxVec(got[0].vec, []float32{0.7071, 0.7071, 0, 0}, 1e-4) || got[0].members != 2 {
		t.Fatalf("group 1: want normalised 2-member mean, got %+v", got)
	}
	got = st.writesFor(2)
	if len(got) != 1 || !approxVec(got[0].vec, []float32{0, 0, 1, 0}, 1e-6) || got[0].members != 1 {
		t.Fatalf("group 2: want label fallback vector, got %+v", got)
	}
	if n := len(st.writesFor(3)); n != 0 {
		t.Fatalf("group 3 must be skipped, got %d writes", n)
	}
	for _, s := range rec.seen {
		if !strings.HasPrefix(s, "passage: ") {
			t.Fatalf("re-embed embeds stored docs only — every text must be passage-prefixed, saw %q", s)
		}
	}
	if slices.Contains(rec.seen, "passage: SALE") {
		t.Fatal("unembeddable member title must not be re-embedded")
	}
}

// A second pass must emit identical writes — the command is idempotent,
// so an interrupted or repeated run converges.
func TestReembedIsIdempotent(t *testing.T) {
	st, rec := reembedFixture()
	if _, err := Reembed(context.Background(), st, rec, 2, 0); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := append([]reembedWrite(nil), st.writes...)
	if _, err := Reembed(context.Background(), st, rec, 2, 0); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(st.writes) != 2*len(first) {
		t.Fatalf("re-run must rewrite every group: %d vs %d writes", len(st.writes), len(first))
	}
	for i, w := range first {
		if !approxVec(w.vec, st.writes[len(first)+i].vec, 1e-7) {
			t.Fatalf("re-run diverged on group %d: %v vs %v", w.gid, w.vec, st.writes[len(first)+i].vec)
		}
	}
}
