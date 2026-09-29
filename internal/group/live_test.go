package group

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func liveStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("PG_LIVE_DSN")
	if dsn == "" {
		t.Skip("PG_LIVE_DSN unset — live postgres smoke skipped")
	}
	st, err := NewStore(context.Background(), dsn, 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// TestLivePGGroupStore exercises the real pgvector path: schema apply,
// group create + exact-key claim, member upsert, centroid advance and
// cosine nearest-neighbour ordering.
func TestLivePGGroupStore(t *testing.T) {
	ctx := context.Background()
	st := liveStore(t)
	run := fmt.Sprintf("live-%d", time.Now().UnixNano()%1e9)

	gid, err := st.CreateGroup(ctx, run+" headphones", []float32{1, 0, 0, 0}, "livemodel",
		Member{URL: "https://a.test/" + run, Domain: "a.test", Title: run + " headphones", Match: "seed"},
		[]string{"gtin:" + run})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, err := st.GroupByKey(ctx, "gtin:"+run); err != nil || got != gid {
		t.Fatalf("key claim: gid=%d err=%v", got, err)
	}

	// A member carrying a vector advances the centroid toward it.
	if err := st.AddMember(ctx, gid,
		Member{URL: "https://b.test/" + run, Domain: "b.test", Title: run + " xm5", Match: "embed:0.95"},
		[]float32{0, 1, 0, 0}, nil); err != nil {
		t.Fatalf("add member: %v", err)
	}
	// Same URL again refreshes, never duplicates.
	if err := st.AddMember(ctx, gid,
		Member{URL: "https://b.test/" + run, Domain: "b.test", Title: run + " xm5", Match: "embed:0.95"},
		nil, nil); err != nil {
		t.Fatalf("re-add member: %v", err)
	}
	ms, err := st.MemberTexts(ctx, gid, 10)
	if err != nil || len(ms) != 2 {
		t.Fatalf("members: %v %d", err, len(ms))
	}

	cands, err := st.Nearest(ctx, []float32{0.6, 0.8, 0, 0}, "livemodel", 5)
	if err != nil {
		t.Fatalf("nearest: %v", err)
	}
	if len(cands) == 0 || cands[0].ID != gid {
		t.Fatalf("nearest must return the group first: %+v", cands)
	}
	// The folded centroid must sit between the two member vectors.
	if cands[0].Dist <= 0 || cands[0].Dist >= 1 {
		t.Fatalf("centroid cosine dist off: %v", cands[0].Dist)
	}
}

// TestLivePGGroupWatchReads exercises the watch-side reads (issue #98):
// member URLs list newest-first and the reverse URL→group lookup
// resolves a member back to its group, unknown URLs to 0.
func TestLivePGGroupWatchReads(t *testing.T) {
	ctx := context.Background()
	st := liveStore(t)
	run := fmt.Sprintf("live-%d", time.Now().UnixNano()%1e9)

	gid, err := st.CreateGroup(ctx, run+" earbuds", []float32{1, 0, 0, 0}, "livemodel",
		Member{URL: "https://w1.test/" + run, Domain: "w1.test", Title: run + " earbuds", Match: "seed"}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.AddMember(ctx, gid,
		Member{URL: "https://w2.test/" + run, Domain: "w2.test", Title: run + " buds", Match: "embed:0.95"},
		nil, nil); err != nil {
		t.Fatalf("add member: %v", err)
	}

	urls, err := st.MemberURLs(ctx, gid, 10)
	if err != nil || len(urls) != 2 {
		t.Fatalf("member urls: %v %d", err, len(urls))
	}
	if urls[0] != "https://w2.test/"+run {
		t.Fatalf("freshest member must lead: %v", urls)
	}
	if got, err := st.GroupByURL(ctx, "https://w1.test/"+run); err != nil || got != gid {
		t.Fatalf("group by url: %d %v", got, err)
	}
	if got, err := st.GroupByURL(ctx, "https://nobody.test/"+run); err != nil || got != 0 {
		t.Fatalf("unknown url must resolve 0: %d %v", got, err)
	}
}

// TestLivePGGroupModelSpace — a group must never surface through Nearest
// under a different model name; vectors live per model space.
func TestLivePGGroupModelSpace(t *testing.T) {
	ctx := context.Background()
	st := liveStore(t)
	run := fmt.Sprintf("live-%d", time.Now().UnixNano()%1e9)
	gid, err := st.CreateGroup(ctx, run, []float32{1, 0, 0, 0}, "livemodel",
		Member{URL: "https://m.test/" + run, Match: "seed"}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cands, err := st.Nearest(ctx, []float32{1, 0, 0, 0}, "othermodel", 5)
	if err != nil {
		t.Fatalf("nearest: %v", err)
	}
	for _, c := range cands {
		if c.ID == gid {
			t.Fatal("foreign-model group leaked into nearest")
		}
	}
}

// TestLivePGGroupDim verifies the dimension guard: a store opened with a
// dim different from the existing column must refuse to start.
func TestLivePGGroupDim(t *testing.T) {
	dsn := os.Getenv("PG_LIVE_DSN")
	if dsn == "" {
		t.Skip("PG_LIVE_DSN unset — live postgres smoke skipped")
	}
	ctx := context.Background()
	st, err := NewStore(ctx, dsn, 4)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	st.Close()
	if _, err := NewStore(ctx, dsn, 8); err == nil {
		t.Fatal("mismatched dim must refuse to open")
	}
}
