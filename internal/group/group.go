// Package group is the durable product-identity layer for issue #98's
// embedding tier. Exact identifiers (gtin/mpn/sku GroupKey) claim a
// persistent group unconditionally; products without identifiers embed
// their canonical name via go-kit/embed and join the nearest stored
// group only when the structured gates agree — discriminator tokens
// (digit-bearing model codes subset-compatible, tier words equal) plus
// per-row spec compatibility from match.SpecsCompatible.
//
// Vector similarity is recall; the gates are precision. Deliberately not
// used: transitive union-find clustering (chaining merges families into
// products) and title-string equality (the failure mode #98 banned).
// Every failure — no embedder, no store, embed error — degrades to
// exact-only ephemeral grouping: product_search never fails for this
// auxiliary path.
package group

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/match"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// groupAssign counts assignment decisions: which tier attached a result
// (exact/embed) and how (joined existing / created / skipped).
var groupAssign = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "quarryn",
	Name:      "group_assign_total",
	Help:      "product_search group assignment outcomes",
}, []string{"tier", "outcome"})

// groupStore is the persistence surface Assign needs — *Store implements
// it for pgvector; tests substitute an in-memory fake.
type groupStore interface {
	GroupByKey(ctx context.Context, exactKey string) (int64, error)
	GroupKeys(ctx context.Context, gid int64) ([]string, error)
	Nearest(ctx context.Context, vec []float32, model string, k int) ([]Candidate, error)
	MemberTexts(ctx context.Context, gid int64, limit int) ([]Member, error)
	CreateGroup(ctx context.Context, label string, vec []float32, model string, m Member, exactKeys []string) (int64, error)
	AddMember(ctx context.Context, gid int64, m Member, vec []float32, exactKeys []string) error
}

// memberTextsLimit caps how many member records the gate consults —
// beyond the head of the list extra evidence is redundant.
const memberTextsLimit = 8

// Assignment records which persistent group a result landed in.
type Assignment struct {
	GroupID int64   // 0 = unassigned
	Exact   bool    // joined via an exact-identifier claim
	Sim     float32 // cosine to the group representative (embed tier)
}

// Assigner resolves products to persistent group ids. Constructed when
// GROUPS_DATABASE_URL is set; the embedder may still be nil (exact tier
// persists without embeddings).
type Assigner struct {
	emb       embed.Embedder
	store     groupStore
	model     string
	thrStrong float32 // cosine floor when model codes are present on both sides
	thrWeak   float32 // bar for weak-identity pairs (digit tokens missing one side)
	topK      int
}

// NewAssigner wires the tiers: store gives exact keys a durable home;
// emb additionally enables the embedding tier for keyless products.
func NewAssigner(store groupStore, emb embed.Embedder, model string, thrStrong, thrWeak float32, topK int) *Assigner {
	return &Assigner{emb: emb, store: store, model: model, thrStrong: thrStrong, thrWeak: thrWeak, topK: topK}
}

// Assign resolves every product to a group id, index-aligned with the
// input slice. It never fails the caller — per-item and whole-tier errors
// degrade to unassigned/zero rather than propagating.
func (a *Assigner) Assign(ctx context.Context, products []extract.Product) []Assignment {
	out := make([]Assignment, len(products))
	if a == nil || a.store == nil || len(products) == 0 {
		return out
	}
	texts := make([]string, len(products))
	rows := make([][]string, len(products))
	keys := make([]string, len(products))
	for i := range products {
		texts[i] = embedText(products[i])
		rows[i] = evidenceRows(products[i])
		keys[i] = products[i].GroupKey()
	}

	var vecs [][]float32
	if a.emb != nil {
		vecs = a.embedAll(ctx, texts)
	}

	var pending []*pendingCluster
	for i := range products {
		out[i] = a.assignOne(ctx, i, products[i], texts[i], rows[i], keys[i], vecsAt(vecs, i), &pending)
	}

	a.flush(ctx, pending, out)
	for i := range out {
		if out[i].GroupID < 0 {
			out[i].GroupID = 0 // pending cluster whose create failed
		}
	}
	return out
}

// assignOne resolves one product: exact claim → stored vector match →
// pending cluster → new pending cluster → unassigned.
func (a *Assigner) assignOne(ctx context.Context, i int, p extract.Product, text string, rows []string, key string, vec []float32, pending *[]*pendingCluster) Assignment {
	m := Member{URL: extract.CanonicalURL(p.URL), Domain: p.Source, Title: text, Evidence: rows}

	// Exact tier: a claimed key resolves immediately — embeddings never
	// override an identifier. An unclaimed key still tries the embedding
	// tier first so the claim lands on an existing group.
	if key != "" {
		if gid, err := a.store.GroupByKey(ctx, key); err == nil && gid != 0 {
			m.Match = "exact"
			// vec is deliberately not folded: the group may carry a
			// centroid from a different model space, and the key claim
			// already anchors identity.
			a.join(ctx, gid, m, nil, []string{key})
			groupAssign.WithLabelValues("exact", "claimed").Inc()
			return Assignment{GroupID: gid, Exact: true}
		}
	}
	// Embedding tier: stored groups then in-set pending clusters. A
	// product carrying an exact key may only join groups that do not
	// claim a conflicting key of the same keyspace — embeddings must
	// never merge two distinct identifiers into one group.
	if vec != nil {
		if gid, sim, ok := a.matchStored(ctx, text, rows, vec, key); ok {
			m.Match = matchLabel(sim)
			a.join(ctx, gid, m, vec, exactKeys(key))
			groupAssign.WithLabelValues("embed", "joined").Inc()
			return Assignment{GroupID: gid, Exact: key != "", Sim: sim}
		}
		if c, sim := matchPending(*pending, text, rows, vec, key, a.thrStrong, a.thrWeak); c != nil {
			m.Match = matchLabel(sim) // same provenance form as stored joins
			c.add(i, vec, m, key)
			return Assignment{GroupID: -1, Exact: key != "", Sim: sim} // resolved at flush
		}
	}
	// A keyless, unembeddable name has nothing a later offer could match
	// on — leave it unassigned rather than write a dead group.
	if vec == nil && key == "" {
		return Assignment{}
	}
	// No match anywhere: start a pending cluster — flushed to a new group
	// row after the pass so siblings later in the list can join.
	c := &pendingCluster{repText: text, repRows: rows}
	c.add(i, vec, m, key)
	*pending = append(*pending, c)
	return Assignment{GroupID: -1, Exact: key != ""}
}

// pendingCluster is a not-yet-persisted group forming inside one search:
// members arrived without identifiers and matched each other on the same
// recall+gate bar stored groups face.
type pendingCluster struct {
	repText string
	repRows []string
	entries []pendingEntry
	keys    []string
}

// pendingEntry keeps a member's index, vector and evidence aligned —
// members without vectors interleave freely.
type pendingEntry struct {
	idx   int
	vec   []float32
	m     Member
	exact bool // the entry carried an exact-identifier key
}

func (c *pendingCluster) add(i int, vec []float32, m Member, key string) {
	c.entries = append(c.entries, pendingEntry{idx: i, vec: vec, m: m, exact: key != ""})
	if key != "" {
		c.keys = append(c.keys, key)
	}
}

// centroid is the member-vector mean, L2-normalised — same convention the
// store applies when advancing a group centroid.
func (c *pendingCluster) centroid() []float32 {
	dim := 0
	for _, e := range c.entries {
		if e.vec != nil {
			dim = len(e.vec)
			break
		}
	}
	if dim == 0 {
		return nil
	}
	out := make([]float32, dim)
	var n float32
	for _, e := range c.entries {
		if e.vec == nil {
			continue
		}
		n++
		for j := range out {
			out[j] += e.vec[j]
		}
	}
	if n == 0 {
		return nil
	}
	for j := range out {
		out[j] /= n
	}
	l2norm(out)
	return out
}

// matchStored walks nearest-centroid candidates closest-first and accepts
// the first group passing all gates. The label anchors the discriminator
// check (single-linkage drift bound); at least one stored member must
// additionally agree on both discriminators and specs — a group is only
// as permissive as its own members' evidence.
func (a *Assigner) matchStored(ctx context.Context, text string, rows []string, vec []float32, key string) (int64, float32, bool) {
	cands, err := a.store.Nearest(ctx, vec, a.model, a.topK)
	if err != nil {
		slog.Warn("group: nearest lookup failed", slog.Any("error", err))
		groupAssign.WithLabelValues("embed", "store_error").Inc()
		return 0, 0, false
	}
	for _, c := range cands {
		sim := float32(1.0) - float32(c.Dist)
		ok, weak := discsCompatible(text, c.Label)
		if !ok {
			groupAssign.WithLabelValues("embed", "gate_rejected").Inc()
			continue
		}
		thr := a.thrStrong
		if weak {
			thr = a.thrWeak
		}
		if sim < thr {
			groupAssign.WithLabelValues("embed", "below_threshold").Inc()
			continue
		}
		if !a.memberAgrees(ctx, c.ID, text, rows) {
			groupAssign.WithLabelValues("embed", "gate_rejected").Inc()
			continue
		}
		if a.claimConflicts(ctx, c.ID, key) {
			groupAssign.WithLabelValues("embed", "gate_rejected").Inc()
			continue
		}
		return c.ID, sim, true
	}
	return 0, 0, false
}

// claimConflicts refuses the merge when the target group already claims
// a different identifier of the same keyspace — vector similarity must
// never fold two GTINs into one product.
func (a *Assigner) claimConflicts(ctx context.Context, gid int64, key string) bool {
	if key == "" {
		return false
	}
	keys, err := a.store.GroupKeys(ctx, gid)
	if err != nil {
		slog.Warn("group: keyset fetch failed", slog.Int64("group", gid), slog.Any("error", err))
		return true // fail closed: cannot rule out a conflicting claim
	}
	return keyConflicts(key, keys)
}

// memberAgrees requires one stored member to pass both gates against the
// incoming offer — group membership is only as consistent as the evidence
// that built it.
func (a *Assigner) memberAgrees(ctx context.Context, gid int64, text string, rows []string) bool {
	ms, err := a.store.MemberTexts(ctx, gid, memberTextsLimit)
	if err != nil {
		slog.Warn("group: member fetch failed", slog.Int64("group", gid), slog.Any("error", err))
		return false
	}
	for _, m := range ms {
		ok, _ := discsCompatible(text, m.Title)
		if !ok {
			continue
		}
		ev := m.Evidence
		if len(ev) == 0 {
			ev = []string{m.Title}
		}
		if match.SpecsCompatible(rows, ev) {
			return true
		}
	}
	return false
}

// matchPending applies the same recall+gate bar against clusters forming
// in this result set, so two keyless offers of one product group together
// even when neither has been seen before.
func matchPending(pending []*pendingCluster, text string, rows []string, vec []float32, key string, thrStrong, thrWeak float32) (*pendingCluster, float32) {
	var best *pendingCluster
	var bestSim float32
	for _, p := range pending {
		if keyConflicts(key, p.keys) {
			continue
		}
		cv := p.centroid()
		if cv == nil {
			continue
		}
		sim := embed.Cosine(vec, cv)
		ok, weak := discsCompatible(text, p.repText)
		if !ok || !match.SpecsCompatible(rows, p.repRows) {
			continue
		}
		thr := thrStrong
		if weak {
			thr = thrWeak
		}
		if sim >= thr && (best == nil || sim > bestSim) {
			best, bestSim = p, sim
		}
	}
	return best, bestSim
}

// join persists membership in an existing group; store errors are logged
// and swallowed — a durable write is best-effort beside the response.
func (a *Assigner) join(ctx context.Context, gid int64, m Member, vec []float32, keys []string) {
	if err := a.store.AddMember(ctx, gid, m, vec, keys); err != nil {
		slog.Warn("group: add member failed", slog.Int64("group", gid), slog.Any("error", err))
	}
}

// flush persists pending clusters as new groups and resolves their
// members' assignments from the -1 placeholder to the real id.
func (a *Assigner) flush(ctx context.Context, pending []*pendingCluster, out []Assignment) {
	for _, p := range pending {
		seed := p.entries[0]
		seed.m.Match = "seed"
		// Seed with the first member's vector only — remaining members fold
		// in via AddMember's running mean, which lands on the same centroid
		// as a batch mean but keeps the sample count correct.
		gid, err := a.store.CreateGroup(ctx, p.repText, seed.vec, a.model, seed.m, p.keys)
		if err != nil {
			slog.Warn("group: create failed", slog.String("label", p.repText), slog.Any("error", err))
			groupAssign.WithLabelValues("embed", "store_error").Inc()
			continue
		}
		for _, e := range p.entries {
			out[e.idx].GroupID = gid
			out[e.idx].Exact = out[e.idx].Exact || e.exact
		}
		tier := "embed"
		if len(p.keys) > 0 {
			tier = "exact" // identity came from an identifier claim
		}
		groupAssign.WithLabelValues(tier, "created").Inc()
		for _, e := range p.entries[1:] {
			mm := e.m
			if mm.Match == "" {
				mm.Match = "embed"
			}
			_ = a.store.AddMember(ctx, gid, mm, e.vec, nil) // best-effort; group already exists
		}
	}
}

// embedAll batch-embeds the embeddable texts in one call; a failed call
// degrades the whole tier for this request rather than retrying per item.
func (a *Assigner) embedAll(ctx context.Context, texts []string) [][]float32 {
	var idxs []int
	var in []string
	for i, t := range texts {
		if embeddable(t) {
			idxs = append(idxs, i)
			in = append(in, t)
		}
	}
	if len(in) == 0 {
		return nil
	}
	vecs, err := a.emb.Embed(ctx, in)
	if err != nil {
		slog.Warn("group: embed batch failed", slog.Int("texts", len(in)), slog.Any("error", err))
		groupAssign.WithLabelValues("embed", "embed_error").Inc()
		return nil
	}
	out := make([][]float32, len(texts))
	for j, v := range vecs {
		if j >= len(idxs) {
			break // a misbehaving server returning extras must not panic
		}
		out[idxs[j]] = v
	}
	return out
}

func vecsAt(vecs [][]float32, i int) []float32 {
	if vecs == nil || i >= len(vecs) {
		return nil
	}
	return vecs[i]
}

func exactKeys(key string) []string {
	if key == "" {
		return nil
	}
	return []string{key}
}

// matchLabel renders the members-table tier label for an embed join.
func matchLabel(sim float32) string {
	return "embed:" + strconv.FormatFloat(float64(sim), 'f', 3, 32)
}
