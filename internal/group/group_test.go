package group

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/sources"
)

// fakeStore is an in-memory groupStore — exercises the assigner without
// postgres while preserving the same call contract.
type fakeStore struct {
	groups   map[int64]*fakeGroup
	byKey    map[string]int64
	nextID   int64
	failNext bool
	creates  int
	joins    int
}

type fakeGroup struct {
	label   string
	vec     []float32
	model   string
	keys    []string
	members []Member
}

func newFakeStore() *fakeStore {
	return &fakeStore{groups: map[int64]*fakeGroup{}, byKey: map[string]int64{}, nextID: 1}
}

func (f *fakeStore) GroupByKey(_ context.Context, key string) (int64, error) {
	return f.byKey[key], nil
}

func (f *fakeStore) GroupKeys(_ context.Context, gid int64) ([]string, error) {
	if g := f.groups[gid]; g != nil {
		return g.keys, nil
	}
	return nil, nil
}

func (f *fakeStore) Nearest(_ context.Context, vec []float32, model string, k int) ([]Candidate, error) {
	if f.failNext {
		return nil, errors.New("pg down")
	}
	var out []Candidate
	for id, g := range f.groups {
		if g.model != model || g.vec == nil {
			continue
		}
		out = append(out, Candidate{ID: id, Label: g.label, Members: len(g.members),
			Vector: g.vec, Dist: float64(1 - cosine(vec, g.vec))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dist < out[j].Dist })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func (f *fakeStore) MemberTexts(_ context.Context, gid int64, limit int) ([]Member, error) {
	g := f.groups[gid]
	if g == nil {
		return nil, nil
	}
	if len(g.members) > limit {
		return g.members[:limit], nil
	}
	return g.members, nil
}

func (f *fakeStore) CreateGroup(_ context.Context, label string, vec []float32, model string, m Member, keys []string) (int64, error) {
	if f.failNext {
		return 0, errors.New("pg down")
	}
	// Mirror the real store's lost-claim redirect: a key already owned by
	// another group resolves the whole create to that owner.
	for _, k := range keys {
		if owner, taken := f.byKey[k]; taken {
			return owner, f.AddMember(context.Background(), owner, m, vec, nil)
		}
	}
	id := f.nextID
	f.nextID++
	f.groups[id] = &fakeGroup{label: label, vec: vec, model: model, keys: keys, members: []Member{m}}
	for _, k := range keys {
		f.byKey[k] = id
	}
	f.creates++
	return id, nil
}

func (f *fakeStore) AddMember(_ context.Context, gid int64, m Member, _ []float32, keys []string) error {
	if f.failNext {
		return errors.New("pg down")
	}
	g := f.groups[gid]
	for _, e := range g.members {
		if e.URL == m.URL {
			return nil // re-seen URL refreshes, never duplicates
		}
	}
	g.members = append(g.members, m)
	for _, k := range keys {
		if _, taken := f.byKey[k]; !taken {
			f.byKey[k] = gid
			g.keys = append(g.keys, k)
		}
	}
	f.joins++
	return nil
}

// fakeEmb maps a text to a fixed vector — tests control similarity by
// constructing orthogonal vs identical vectors directly.
type fakeEmb struct {
	vecs map[string][]float32
	err  error
}

func (f fakeEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.vecs[t]
	}
	return out, nil
}

func (f fakeEmb) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	v, err := f.Embed(ctx, []string{text})
	if err != nil || len(v) == 0 {
		return nil, err
	}
	return v[0], nil
}

func (f fakeEmb) Dimension() int { return 4 }
func (f fakeEmb) Close() error   { return nil }

// cosine over the tiny test vectors (embed.Cosine equivalent kept local
// so tests stay independent of the kit's exact float semantics).
func cosine(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (sqrtD(na) * sqrtD(nb)))
}

func sqrtD(x float64) float64 {
	// Newton iteration suffices for test vectors.
	if x == 0 {
		return 0
	}
	z := x
	for i := 0; i < 50; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

func prod(name, url, source string) extract.Product {
	return extract.Product{Name: name, URL: url, Source: source}
}

func assign(t *testing.T, a *Assigner, ps []extract.Product) []Assignment {
	t.Helper()
	out := a.Assign(context.Background(), ps)
	if len(out) != len(ps) {
		t.Fatalf("assignments len %d, want %d", len(out), len(ps))
	}
	return out
}

func TestGateDigitTokens(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Sony WH-1000XM5 headphones", "WH1000XM5 wireless black", true},
		{"WH 1000 XM5", "WH-1000XM5", true},           // fragmented model code joins equal
		{"Sony WH-1000XM5", "Sony WH-1000XM4", false}, // different generation
		{"iPhone 16 256GB", "iPhone 16", true},        // subset: extra spec on one side
		{"USB-C cable 2m", "USB-C cable 1m", false},   // different length = different product
	}
	for _, c := range cases {
		got, _ := discsCompatible(c.a, c.b)
		if got != c.want {
			t.Errorf("discsCompatible(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestGateTierWords(t *testing.T) {
	ok, _ := discsCompatible("iPhone 16 Pro", "iPhone 16")
	if ok {
		t.Error("pro tier asymmetry must block")
	}
	ok, _ = discsCompatible("MacBook Air M2", "MacBook Air M2 13 inch")
	if !ok {
		t.Error("same tier set must pass")
	}
}

func TestGateWeakFlag(t *testing.T) {
	if _, weak := discsCompatible("leather wallet brown", "leather wallet large brown"); !weak {
		t.Error("no digit tokens either side = weak identity")
	}
	if _, weak := discsCompatible("WH-1000XM5", "Sony wireless headphones"); !weak {
		t.Error("digit tokens only one side = weak identity")
	}
	if _, weak := discsCompatible("WH-1000XM5", "WH1000XM5 black"); weak {
		t.Error("digit tokens both sides = strong identity")
	}
}

func TestAssignExactClaim(t *testing.T) {
	st := newFakeStore()
	a := NewAssigner(st, nil, "test", 0.90, 0.94, 5)
	p := prod("Sony WH-1000XM5", "https://a.com/p/1?utm=x", "a.com")
	p.GTIN = "0196473012345" // check digit: computed below via helper
	// use a known-valid GTIN: 4006381333931 (EAN-13 valid)
	p.GTIN = "4006381333931"
	out := assign(t, a, []extract.Product{p})
	if out[0].GroupID == 0 || !out[0].Exact {
		t.Fatalf("exact-key product must create+claim a group, got %+v", out[0])
	}
	// second search with the same key → joins the same group
	p2 := prod("WH1000XM5", "https://b.com/x", "b.com")
	p2.GTIN = "4006381333931"
	out = assign(t, a, []extract.Product{p2})
	if out[0].GroupID != 1 || !out[0].Exact {
		t.Fatalf("claimed key must join existing group, got %+v", out[0])
	}
	if st.groups[1] == nil || len(st.groups[1].members) != 2 {
		t.Fatalf("expected 2 members in group 1, got %+v", st.groups[1])
	}
}

func TestAssignEmbedSameProduct(t *testing.T) {
	st := newFakeStore()
	v := []float32{1, 0, 0, 0}
	emb := fakeEmb{vecs: map[string][]float32{
		"Sony WH-1000XM5 Wireless Headphones":   v,
		"WH1000XM5 Noise Cancelling Headphones": {0.99, 0.1, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	out := assign(t, a, []extract.Product{
		prod("Sony WH-1000XM5 Wireless Headphones", "https://a.com/1", "a.com"),
		prod("WH1000XM5 Noise Cancelling Headphones", "https://b.com/2", "b.com"),
	})
	if out[0].GroupID == 0 || out[1].GroupID != out[0].GroupID {
		t.Fatalf("same product across stores must share a group: %+v", out)
	}
}

func TestAssignEmbedDifferentModelRejected(t *testing.T) {
	st := newFakeStore()
	// near-identical vectors — cosine recall alone would merge
	emb := fakeEmb{vecs: map[string][]float32{
		"Sony WH-1000XM5": {1, 0, 0, 0},
		"Sony WH-1000XM4": {0.999, 0.001, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	out := assign(t, a, []extract.Product{
		prod("Sony WH-1000XM5", "https://a.com/1", "a.com"),
		prod("Sony WH-1000XM4", "https://b.com/2", "b.com"),
	})
	if out[0].GroupID == 0 || out[1].GroupID == 0 {
		t.Fatalf("each product gets its own group: %+v", out)
	}
	if out[0].GroupID == out[1].GroupID {
		t.Fatalf("XM5 and XM4 must not merge: %+v", out)
	}
}

func TestAssignVariantMismatchRejected(t *testing.T) {
	st := newFakeStore()
	emb := fakeEmb{vecs: map[string][]float32{
		"MacBook Pro 64GB RAM 1TB": {1, 0, 0, 0},
		"MacBook Pro 24GB RAM 2TB": {0.99, 0.05, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	out := assign(t, a, []extract.Product{
		prod("MacBook Pro 64GB RAM 1TB", "https://a.com/1", "a.com"),
		prod("MacBook Pro 24GB RAM 2TB", "https://b.com/2", "b.com"),
	})
	if out[0].GroupID == out[1].GroupID && out[0].GroupID != 0 {
		t.Fatalf("different RAM/storage configs must not merge: %+v", out)
	}
}

func TestAssignVariantRowsCompatible(t *testing.T) {
	st := newFakeStore()
	emb := fakeEmb{vecs: map[string][]float32{
		"MacBook Pro M5":            {1, 0, 0, 0},
		"Apple MacBook Pro M5 24GB": {0.98, 0.1, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	multi := prod("MacBook Pro M5", "https://a.com/1", "a.com")
	multi.Variants = []sources.Variant{
		{Title: "24GB RAM / 1TB SSD"},
		{Title: "64GB RAM / 2TB SSD"},
	}
	single := prod("Apple MacBook Pro M5 24GB", "https://b.com/2", "b.com")
	out := assign(t, a, []extract.Product{multi, single})
	if out[0].GroupID == 0 || out[1].GroupID != out[0].GroupID {
		t.Fatalf("single-config listing matching one variant row must join: %+v", out)
	}
}

func TestAssignWeakIdentityHigherBar(t *testing.T) {
	st := newFakeStore()
	emb := fakeEmb{vecs: map[string][]float32{
		"handmade leather wallet": {1, 0, 0, 0},
		"leather wallet handmade": {0.92, 0.39, 0, 0}, // ~0.92 cos — under weak bar
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	out := assign(t, a, []extract.Product{
		prod("handmade leather wallet", "https://a.com/1", "a.com"),
		prod("leather wallet handmade", "https://b.com/2", "b.com"),
	})
	if out[0].GroupID != 0 && out[0].GroupID == out[1].GroupID {
		t.Fatalf("weak-identity pair below weak bar must not merge: %+v", out)
	}
}

func TestAssignEmbedErrorDegrades(t *testing.T) {
	st := newFakeStore()
	a := NewAssigner(st, fakeEmb{err: errors.New("embed-server down")}, "test", 0.90, 0.94, 5)
	p := prod("Sony WH-1000XM5", "https://a.com/1", "a.com")
	p.GTIN = "4006381333931"
	out := assign(t, a, []extract.Product{p, prod("keyless item", "https://b.com/2", "b.com")})
	if out[0].GroupID == 0 || !out[0].Exact {
		t.Fatalf("embed outage must not lose exact grouping: %+v", out)
	}
	if out[1].GroupID != 0 {
		t.Fatalf("keyless item unassigned on embed outage: %+v", out)
	}
}

func TestAssignExactKeyClaimsEmbedGroup(t *testing.T) {
	st := newFakeStore()
	emb := fakeEmb{vecs: map[string][]float32{
		"Sony WH-1000XM5": {1, 0, 0, 0},
		"WH1000XM5 black": {0.99, 0.05, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	// first: a keyless offer creates the group by embedding
	assign(t, a, []extract.Product{prod("Sony WH-1000XM5", "https://a.com/1", "a.com")})
	// second: an exact-keyed offer embeds close and attaches its key claim
	p := prod("WH1000XM5 black", "https://b.com/2", "b.com")
	p.GTIN = "4006381333931"
	out := assign(t, a, []extract.Product{p})
	if out[0].GroupID != 1 {
		t.Fatalf("exact-keyed offer should join the embedding-created group, got %+v", out[0])
	}
	if st.byKey["gtin:4006381333931"] != 1 {
		t.Fatalf("key claim must land on the joined group, byKey=%v", st.byKey)
	}
}

func TestAssignNoChains(t *testing.T) {
	st := newFakeStore()
	// A~B and B~C but A≁C in the DISCRIMINATOR dimension: XM5 vs XM4 —
	// B is a generic "WH-1000XM" naming both could chain through.
	emb := fakeEmb{vecs: map[string][]float32{
		"Sony WH-1000XM5": {1, 0, 0, 0},
		"Sony WH-1000XM":  {0.98, 0.1, 0, 0},
		"Sony WH-1000XM4": {0.97, 0.12, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	out := assign(t, a, []extract.Product{
		prod("Sony WH-1000XM5", "https://a.com/1", "a.com"),
		prod("Sony WH-1000XM", "https://b.com/2", "b.com"),
		prod("Sony WH-1000XM4", "https://c.com/3", "c.com"),
	})
	if out[0].GroupID == out[2].GroupID && out[0].GroupID != 0 {
		t.Fatalf("chaining must not merge XM5 and XM4 through the generic name: %+v", out)
	}
}

func TestAssignDifferentIdentifiersNoMerge(t *testing.T) {
	st := newFakeStore()
	// Same product name, near-identical vectors — but the two listings
	// carry DIFFERENT GTINs (e.g. color SKUs). Embeddings must never fold
	// two identifiers into one group.
	emb := fakeEmb{vecs: map[string][]float32{
		"Sony WH-1000XM5 black":  {1, 0, 0, 0},
		"Sony WH-1000XM5 silver": {0.99, 0.1, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	p1 := prod("Sony WH-1000XM5 black", "https://a.com/1", "a.com")
	p1.GTIN = "4006381333931"
	p2 := prod("Sony WH-1000XM5 silver", "https://b.com/2", "b.com")
	p2.GTIN = "5901234123457"
	out := assign(t, a, []extract.Product{p1, p2})
	if out[0].GroupID == 0 || out[1].GroupID == 0 {
		t.Fatalf("each keyed product gets a group: %+v", out)
	}
	if out[0].GroupID == out[1].GroupID {
		t.Fatalf("distinct GTINs must never merge via embedding: %+v", out)
	}
	g1 := out[0].GroupID
	// Stored-level path: a later search with yet another GTIN but the
	// same product name must not join the persisted group either — the
	// claimed keyspace blocks it at matchStored.
	emb.vecs["Sony WH-1000XM5 white"] = []float32{0.98, 0.15, 0, 0}
	p3 := prod("Sony WH-1000XM5 white", "https://c.com/3", "c.com")
	p3.GTIN = "0887273790193" // valid UPC-A check digit
	out = assign(t, a, []extract.Product{p3})
	if out[0].GroupID == 0 {
		t.Fatalf("new identifier must still create a group: %+v", out[0])
	}
	if out[0].GroupID == g1 {
		t.Fatal("conflicting GTIN joined the claimed group")
	}
}

func TestAssignCrossKeyspaceMerges(t *testing.T) {
	st := newFakeStore()
	// A GTIN-carrying offer and an MPN-carrying offer of one product:
	// different keyspaces do NOT conflict — a product legitimately has
	// several identifier types, and the group claims both.
	emb := fakeEmb{vecs: map[string][]float32{
		"Sony WH-1000XM5 headphones":  {1, 0, 0, 0},
		"Sony WH-1000XM5 headphones.": {0.99, 0.1, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	p1 := prod("Sony WH-1000XM5 headphones", "https://a.com/1", "a.com")
	p1.GTIN = "4006381333931"
	p2 := prod("Sony WH-1000XM5 headphones.", "https://b.com/2", "b.com")
	p2.MPN = "WH1000XM5"
	out := assign(t, a, []extract.Product{p1, p2})
	if out[0].GroupID == 0 || out[1].GroupID != out[0].GroupID {
		t.Fatalf("cross-keyspace identifiers of one product must merge: %+v", out)
	}
	gid := out[0].GroupID
	if st.byKey["gtin:4006381333931"] != gid || st.byKey["mpn:wh1000xm5"] != gid {
		t.Fatalf("group must claim both keys: %v", st.byKey)
	}
}

func TestAssignVariantRowsContradict(t *testing.T) {
	st := newFakeStore()
	// Two listings of the same model line disclosing DISJOINT config
	// sets — the bare name must not launder the contradiction.
	emb := fakeEmb{vecs: map[string][]float32{
		"MacBook Pro M5":  {1, 0, 0, 0},
		"MacBook Pro M5 ": {0.99, 0.1, 0, 0},
	}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	p1 := prod("MacBook Pro M5", "https://a.com/1", "a.com")
	p1.Variants = []sources.Variant{{Title: "24GB RAM / 1TB SSD"}}
	p2 := prod("MacBook Pro M5 ", "https://b.com/2", "b.com")
	p2.Variants = []sources.Variant{{Title: "64GB RAM / 2TB SSD"}}
	out := assign(t, a, []extract.Product{p1, p2})
	if out[0].GroupID == out[1].GroupID && out[0].GroupID != 0 {
		t.Fatalf("disjoint config sets must not merge via the bare name: %+v", out)
	}
}

func TestAssignRepeatMemberNoDup(t *testing.T) {
	st := newFakeStore()
	emb := fakeEmb{vecs: map[string][]float32{"Sony WH-1000XM5": {1, 0, 0, 0}}}
	a := NewAssigner(st, emb, "test", 0.90, 0.94, 5)
	p := prod("Sony WH-1000XM5", "https://a.com/1", "a.com")
	out1 := assign(t, a, []extract.Product{p})
	out2 := assign(t, a, []extract.Product{p})
	if out2[0].GroupID != out1[0].GroupID {
		t.Fatalf("repeat search must re-resolve the same group: %v vs %v", out1[0], out2[0])
	}
	if n := len(st.groups[out1[0].GroupID].members); n != 1 {
		t.Fatalf("re-seen URL must not duplicate membership: %d members", n)
	}
}

func TestKeyConflicts(t *testing.T) {
	if !keyConflicts("gtin:a", []string{"gtin:b"}) {
		t.Error("same keyspace different value must conflict")
	}
	if keyConflicts("gtin:a", []string{"mpn:b"}) {
		t.Error("different keyspace is no conflict")
	}
	if keyConflicts("gtin:a", []string{"gtin:a"}) {
		t.Error("identical key is no conflict")
	}
	if keyConflicts("", []string{"gtin:b"}) || keyConflicts("gtin:a", nil) {
		t.Error("empty side never conflicts")
	}
}

func TestAssignThinNameSkipped(t *testing.T) {
	st := newFakeStore()
	a := NewAssigner(st, fakeEmb{vecs: map[string][]float32{"SALE": {1, 0, 0, 0}}}, "test", 0.90, 0.94, 5)
	out := assign(t, a, []extract.Product{prod("SALE", "https://a.com/1", "a.com")})
	if out[0].GroupID != 0 {
		t.Fatalf("one-token name must stay unassigned, got %+v", out[0])
	}
	if st.creates != 0 {
		t.Fatalf("thin names must not create dead group rows, creates=%d", st.creates)
	}
}
