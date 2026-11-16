package group

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-kit/embed"
)

// ReembedStats reports what a Reembed pass did — the operator-facing
// progress counters the reembed-groups command prints.
type ReembedStats struct {
	Scanned   int   // centroid-carrying groups visited
	Updated   int   // centroids recomputed and written
	Skipped   int   // groups left untouched (nothing embeddable to recompute from)
	Failed    int   // per-group failures — counted, never aborts the pass
	Conflicts int   // rows that moved mid-read — deferred to the next pass, never dropped
	ResumeID  int64 // -from-id value a resume continues from without skipping unfinished groups
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
	// afterID — the pagination cursor. Only rows in model's own vector
	// space qualify: foreign-model centroids must never be rewritten.
	embeddingGroups(ctx context.Context, model string, afterID int64, limit int) ([]embeddingGroup, error)
	// foldableTitles lists the member titles whose vectors folded into
	// the group centroid (non-exact provenance, non-empty titles), plus
	// the row's revision at read time — the token setEmbedding compares.
	foldableTitles(ctx context.Context, gid int64) ([]string, time.Time, error)
	// setEmbedding writes the recomputed centroid and its sample count,
	// but only while the row still carries seenRev — an AddMember that
	// committed mid-read moved the revision, the write touches no row,
	// and applied comes back false.
	setEmbedding(ctx context.Context, gid int64, model string, vec []float32, members int, seenRev time.Time) (applied bool, err error)
}

// Reembed rewrites every stored group centroid in the passage-prefixed
// e5 space — the one-off migration the reembed-groups command runs after
// the assigner switched to role-prefixed embeddings. Only rows tagged
// with the configured model are touched: centroids of other models live
// in their own spaces. Each centroid is rebuilt from the member titles
// that originally folded it, never from the stored vector, so a re-run
// converges to the same value: the pass is idempotent, and -from-id
// resumes an interrupted one. Per-group failures are counted and
// skipped, never abort the sweep; a group whose row moved while its
// titles were being read is deferred to the next pass rather than
// overwritten, and stats.ResumeID always names the -from-id that retries
// every unfinished group — it advances only past groups that completed.
func Reembed(ctx context.Context, store reembedStore, emb embed.Embedder, model string, batchSize int, afterID int64) (ReembedStats, error) {
	if emb == nil {
		return ReembedStats{}, errors.New("group reembed: embedder required")
	}
	// Stored vectors are documents — passage role only. A lookup-shaped
	// vector in a centroid row would sit in the wrong retrieval space.
	demb := embed.Prefixed(emb, embed.E5PassagePrefix)
	var stats ReembedStats
	stats.ResumeID = afterID
	prefixDone := true // every group up to ResumeID completed — the cursor may advance
	scan := afterID    // pagination cursor — unlike ResumeID it also passes failed groups
	for {
		batch, err := store.embeddingGroups(ctx, model, scan, batchSize)
		if err != nil {
			return stats.abort(err)
		}
		if len(batch) == 0 {
			return stats, nil
		}
		for _, g := range batch {
			if err := ctx.Err(); err != nil {
				return stats.abort(err)
			}
			stats.Scanned++
			scan = g.ID
			outcome := reembedOne(ctx, demb, store, model, g)
			if err := ctx.Err(); err != nil {
				// Cancelled inside the group — it was not written; the
				// cursor stays before it so a resume retries it.
				return stats.abort(err)
			}
			stats.count(outcome)
			done := outcome == reembedUpdated || outcome == reembedSkipped
			if done && prefixDone {
				stats.ResumeID = g.ID
			}
			// An unfinished group ends the resumable prefix — advancing
			// past it would silently skip it on -from-id.
			prefixDone = prefixDone && done
		}
		slog.Info("reembed: batch done",
			slog.Int64("after_id", stats.ResumeID),
			slog.Int("scanned", stats.Scanned),
			slog.Int("updated", stats.Updated),
			slog.Int("skipped", stats.Skipped),
			slog.Int("failed", stats.Failed),
			slog.Int("conflicts", stats.Conflicts))
	}
}

// count folds a group's outcome into the pass counters.
func (stats *ReembedStats) count(o reembedOutcome) {
	switch o {
	case reembedUpdated:
		stats.Updated++
	case reembedSkipped:
		stats.Skipped++
	case reembedConflict:
		stats.Conflicts++
	default:
		stats.Failed++
	}
}

// abort reports an early stop — error or cancellation — and always logs
// the -from-id that resumes without skipping an unfinished group.
func (stats ReembedStats) abort(err error) (ReembedStats, error) {
	slog.Warn("reembed: stopped — resume with -from-id",
		slog.Int64("after_id", stats.ResumeID), slog.Any("error", err))
	return stats, err
}

type reembedOutcome int

const (
	reembedUpdated reembedOutcome = iota
	reembedSkipped
	reembedFailed
	reembedConflict
)

// reembedOne recomputes and writes a single group's centroid. Errors are
// logged and counted by the caller — a bad row must not stall the sweep.
// A group that changed since its titles were read (a member folded in by
// a live search, or any membership change bumping the revision) reports
// reembedConflict: the conditional write touched no row and the next
// pass recomputes it.
func reembedOne(ctx context.Context, demb embed.Embedder, store reembedStore, model string, g embeddingGroup) reembedOutcome {
	titles, rev, err := store.foldableTitles(ctx, g.ID)
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
	applied, err := store.setEmbedding(ctx, g.ID, model, c, members, rev)
	if err != nil {
		slog.Warn("reembed: write failed", slog.Int64("group", g.ID), slog.Any("error", err))
		return reembedFailed
	}
	if !applied {
		slog.Warn("reembed: group changed mid-read — left for the next pass", slog.Int64("group", g.ID))
		return reembedConflict
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
