package group

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixtureModel is the model tag the canned groups carry — Reembed must
// scope enumeration and writes to the model it was invoked with.
const fixtureModel = "e5-test"

var fixtureRev = time.Unix(1000, 0)

type reembedWrite struct {
	gid     int64
	vec     []float32
	members int
}

// fakeReembedStore plays the persistence side of Reembed — foldable
// titles are what the SQL filter (match <> 'exact', title <> ”) returns,
// models mirrors the model column, and revs mirrors the updated_at
// revision the write compare-and-sets on.
type fakeReembedStore struct {
	groups    []embeddingGroup
	models    map[int64]string
	titles    map[int64][]string
	titleErrs map[int64]error
	revs      map[int64]time.Time
	writes    []reembedWrite
}

func (f *fakeReembedStore) embeddingGroups(_ context.Context, model string, afterID int64, limit int) ([]embeddingGroup, error) {
	var out []embeddingGroup
	for _, g := range f.groups {
		if f.models[g.ID] == model && g.ID > afterID {
			out = append(out, g)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeReembedStore) foldableTitles(_ context.Context, gid int64) ([]string, time.Time, error) {
	if err := f.titleErrs[gid]; err != nil {
		delete(f.titleErrs, gid) // one-shot — a transient store failure
		return nil, time.Time{}, err
	}
	return f.titles[gid], f.revs[gid], nil
}

// setEmbedding mirrors the production compare-and-set: the write lands
// only when the row still carries the revision read with the titles and
// belongs to the model's space — what UPDATE ... WHERE id AND model AND
// updated_at enforces. A landed write bumps the revision like now() does.
func (f *fakeReembedStore) setEmbedding(_ context.Context, gid int64, model string, vec []float32, members int, seen time.Time) (bool, error) {
	if f.models[gid] != model || !f.revs[gid].Equal(seen) {
		return false, nil
	}
	f.writes = append(f.writes, reembedWrite{gid: gid, vec: vec, members: members})
	f.revs[gid] = f.revs[gid].Add(time.Second)
	return true, nil
}

// bumpRev plays an AddMember commit: a membership change moves the row's
// updated_at even when it does not advance the centroid.
func (f *fakeReembedStore) bumpRev(gid int64) {
	f.revs[gid] = f.revs[gid].Add(time.Second)
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

// hookEmb runs fn after each successful Embed — tests inject a concurrent
// event (a member add, a SIGTERM) into the read/write window. A cancelled
// context surfaces like the real client's would.
type hookEmb struct {
	inner *recEmb
	fn    func()
}

func (h *hookEmb) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out, err := h.inner.Embed(ctx, texts)
	if err == nil && h.fn != nil {
		h.fn()
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	return out, err
}

func (h *hookEmb) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return h.inner.EmbedQuery(ctx, text)
}

func (h *hookEmb) Dimension() int { return h.inner.Dimension() }
func (h *hookEmb) Close() error   { return h.inner.Close() }

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
		models: map[int64]string{1: fixtureModel, 2: fixtureModel, 3: fixtureModel},
		titles: map[int64][]string{
			1: {"alpha one", "beta two", "SALE"}, // SALE fails embeddable() — never folded
			2: {},                                // nothing folded — label stands in
			3: {"x"},                             // one thin token — nothing to recompute
		},
		revs: map[int64]time.Time{1: fixtureRev, 2: fixtureRev, 3: fixtureRev},
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
	stats, err := Reembed(context.Background(), st, rec, fixtureModel, 2, 0)
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
	if _, err := Reembed(context.Background(), st, rec, fixtureModel, 2, 0); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := append([]reembedWrite(nil), st.writes...)
	if _, err := Reembed(context.Background(), st, rec, fixtureModel, 2, 0); err != nil {
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

// A member folded in by a live AddMember between the title read and the
// centroid write must not be lost: the compare-and-set refuses the stale
// snapshot, the group is deferred past the resumable cursor, and the next
// pass rebuilds it WITH the new member's title.
func TestReembedConcurrentAddMemberDefers(t *testing.T) {
	st, rec := reembedFixture()
	bumped := false
	emb := &hookEmb{inner: rec, fn: func() {
		if bumped {
			return
		}
		bumped = true
		// AddMember commits while group 1's embed request is in flight —
		// a new title joins the fold set and updated_at moves.
		st.titles[1] = append(st.titles[1], "gamma three")
		st.bumpRev(1)
	}}
	rec.vecs["passage: gamma three"] = []float32{0, 0, 1, 0}
	stats, err := Reembed(context.Background(), st, emb, fixtureModel, 10, 0)
	if err != nil {
		t.Fatalf("reembed: %v", err)
	}
	if stats.Conflicts != 1 || stats.Updated != 1 {
		t.Fatalf("group 1 must defer as a conflict, stats %+v", stats)
	}
	if n := len(st.writesFor(1)); n != 0 {
		t.Fatalf("stale snapshot must not overwrite the concurrent member: %d writes", n)
	}
	if stats.ResumeID != 0 {
		t.Fatalf("conflicted group 1 ends the completed prefix — ResumeID must stay 0, got %d", stats.ResumeID)
	}
	// The next pass recomputes the deferred group — nothing is dropped.
	stats, err = Reembed(context.Background(), st, rec, fixtureModel, 10, stats.ResumeID)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	got := st.writesFor(1)
	if len(got) != 1 || got[0].members != 3 || !approxVec(got[0].vec, []float32{0.5774, 0.5774, 0.5774, 0}, 1e-4) {
		t.Fatalf("deferred group must fold the concurrent member in — want 3-member mean, got %+v", got)
	}
}

// A row tagged with another model lives in a different vector space —
// the pass must not scan it, embed its titles or rewrite its centroid.
func TestReembedLeavesForeignModelAlone(t *testing.T) {
	st, rec := reembedFixture()
	st.groups = append(st.groups, embeddingGroup{ID: 9, Label: "foreign cans"})
	st.models[9] = "other-model"
	st.revs[9] = fixtureRev
	st.titles[9] = []string{"foreign title"}
	rec.vecs["passage: foreign title"] = []float32{0, 0, 0, 1}
	stats, err := Reembed(context.Background(), st, rec, fixtureModel, 10, 0)
	if err != nil {
		t.Fatalf("reembed: %v", err)
	}
	if stats.Scanned != 3 {
		t.Fatalf("foreign-model group must not be scanned: %+v", stats)
	}
	if n := len(st.writesFor(9)); n != 0 {
		t.Fatalf("foreign-model centroid must stay untouched: %d writes", n)
	}
	for _, s := range rec.seen {
		if strings.Contains(s, "foreign") {
			t.Fatalf("foreign-model titles must never reach the embedder: %q", s)
		}
	}
}

// A SIGTERM mid-batch must leave the resumable cursor at the last
// completed group — resuming from it re-embeds every interrupted group
// instead of skipping it. A group that merely fails the pass stalls the
// cursor the same way: -from-id may only pass groups that completed.
func TestReembedResumeCoversCancelledGroups(t *testing.T) {
	st := &fakeReembedStore{
		groups: []embeddingGroup{{ID: 11}, {ID: 12}, {ID: 13}, {ID: 14}, {ID: 15}},
		models: map[int64]string{11: fixtureModel, 12: fixtureModel, 13: fixtureModel, 14: fixtureModel, 15: fixtureModel},
		titles: map[int64][]string{
			11: {"item eleven"}, 12: {"item twelve"}, 13: {"item thirteen"},
			14: {"item fourteen"}, 15: {"item fifteen"},
		},
		titleErrs: map[int64]error{12: errors.New("pg blip")}, // transient — resumes clean
		revs:      map[int64]time.Time{11: fixtureRev, 12: fixtureRev, 13: fixtureRev, 14: fixtureRev, 15: fixtureRev},
	}
	rec := &recEmb{vecs: map[string][]float32{
		"passage: item eleven":   {1, 0, 0, 0},
		"passage: item twelve":   {0, 1, 0, 0},
		"passage: item thirteen": {0, 0, 1, 0},
		"passage: item fourteen": {0, 0, 0, 1},
		"passage: item fifteen":  {1, 1, 0, 0},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	emb := &hookEmb{inner: rec, fn: func() {
		calls++
		if calls == 2 {
			cancel() // SIGTERM lands inside group 13's embed request
		}
	}}
	stats, err := Reembed(ctx, st, emb, fixtureModel, 2, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel must surface to the command, got %v", err)
	}
	if stats.ResumeID != 11 {
		t.Fatalf("resume cursor must stop at the last completed group 11, got %d", stats.ResumeID)
	}
	if n := len(st.writesFor(13)); n != 0 {
		t.Fatalf("interrupted group must not be written: %d writes", n)
	}
	if _, err := Reembed(context.Background(), st, rec, fixtureModel, 2, stats.ResumeID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	for _, g := range st.groups {
		if len(st.writesFor(g.ID)) == 0 {
			t.Fatalf("group %d never re-embedded after resume", g.ID)
		}
	}
}
