package group

import (
	"context"
	"errors"
	"log/slog"

	"github.com/anatolykoptev/go-kit/embed"
)

// ReembedStats reports what a Reembed pass did — the operator-facing
// progress counters the reembed-groups command prints.
type ReembedStats struct {
	Scanned int // centroid-carrying groups visited
	Updated int // centroids recomputed and written
	Skipped int // groups left untouched (nothing embeddable to recompute from)
	Failed  int // per-group failures — counted, never aborts the pass
}

// embeddingGroup is the product_groups row subset a re-embed needs.
type embeddingGroup struct {
	ID    int64
	Label string
}

// reembedStore is the persistence surface Reembed needs — *Store
// implements it for pgvector; tests substitute a fake.
type reembedStore interface {
	// embeddingGroups lists centroid-carrying groups id-ascending after
	// afterID — the resumable cursor.
	embeddingGroups(ctx context.Context, afterID int64, limit int) ([]embeddingGroup, error)
	// foldableTitles lists the member titles whose vectors folded into
	// the group centroid (non-exact provenance, non-empty titles).
	foldableTitles(ctx context.Context, gid int64) ([]string, error)
	// setEmbedding writes the recomputed centroid and its sample count.
	setEmbedding(ctx context.Context, gid int64, vec []float32, members int) error
}

// Reembed rewrites every stored group centroid in the passage-prefixed
// e5 space — the one-off migration the reembed-groups command runs after
// the assigner switched to role-prefixed embeddings. Each centroid is
// rebuilt from the member titles that originally folded it, never from
// the stored vector, so a re-run converges to the same value: the pass
// is idempotent, and -from-id resumes an interrupted one. Per-group
// failures are counted and skipped, never abort the sweep.
func Reembed(ctx context.Context, store reembedStore, emb embed.Embedder, batchSize int, afterID int64) (ReembedStats, error) {
	if emb == nil {
		return ReembedStats{}, errors.New("group reembed: embedder required")
	}
	// Stored vectors are documents — passage role only. A lookup-shaped
	// vector in a centroid row would sit in the wrong retrieval space.
	demb := embed.Prefixed(emb, embed.E5PassagePrefix)
	var stats ReembedStats
	cursor := afterID
	for {
		batch, err := store.embeddingGroups(ctx, cursor, batchSize)
		if err != nil {
			return stats, err
		}
		if len(batch) == 0 {
			return stats, nil
		}
		for _, g := range batch {
			stats.Scanned++
			cursor = g.ID
			switch reembedOne(ctx, demb, store, g) {
			case reembedUpdated:
				stats.Updated++
			case reembedSkipped:
				stats.Skipped++
			default:
				stats.Failed++
			}
		}
		slog.Info("reembed: batch done",
			slog.Int64("after_id", cursor),
			slog.Int("scanned", stats.Scanned),
			slog.Int("updated", stats.Updated),
			slog.Int("skipped", stats.Skipped),
			slog.Int("failed", stats.Failed))
	}
}

type reembedOutcome int

const (
	reembedUpdated reembedOutcome = iota
	reembedSkipped
	reembedFailed
)

// reembedOne recomputes and writes a single group's centroid. Errors are
// logged and counted by the caller — a bad row must not stall the sweep.
func reembedOne(ctx context.Context, demb embed.Embedder, store reembedStore, g embeddingGroup) reembedOutcome {
	titles, err := store.foldableTitles(ctx, g.ID)
	if err != nil {
		slog.Warn("reembed: member scan failed", slog.Int64("group", g.ID), slog.Any("error", err))
		return reembedFailed
	}
	fold := foldSet(titles, g.Label)
	if len(fold) == 0 {
		return reembedSkipped
	}
	vecs, err := demb.Embed(ctx, fold)
	if err != nil {
		slog.Warn("reembed: embed failed", slog.Int64("group", g.ID), slog.Int("texts", len(fold)), slog.Any("error", err))
		return reembedFailed
	}
	c := meanCentroid(vecs)
	if c == nil {
		return reembedSkipped
	}
	members := 0
	for _, v := range vecs {
		if v != nil {
			members++
		}
	}
	if err := store.setEmbedding(ctx, g.ID, c, members); err != nil {
		slog.Warn("reembed: write failed", slog.Int64("group", g.ID), slog.Any("error", err))
		return reembedFailed
	}
	return reembedUpdated
}

// foldSet picks the texts a group's centroid is rebuilt from: the
// embeddable member titles that folded a vector, else the group label —
// the seed's own embedded text, the closest surviving stand-in. nil =
// nothing left to recompute from.
func foldSet(titles []string, label string) []string {
	var fold []string
	for _, t := range titles {
		if embeddable(t) {
			fold = append(fold, t)
		}
	}
	if len(fold) == 0 && embeddable(label) {
		fold = []string{label}
	}
	return fold
}
