package group

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// Store is the pgvector-backed product-group registry: persistent group
// rows carry the running centroid that keyless offers are matched
// against, exact-identifier claims map gtin:/mpn:/sku: keys onto groups,
// and member rows record every offer that proved membership (URL, domain,
// the title embedded, and which evidence tier attached it).
type Store struct {
	pool *pgxpool.Pool
	dim  int
}

// Member is one offer's membership evidence in a group.
type Member struct {
	URL    string
	Domain string
	Title  string
	// Evidence is the row set the spec gate evaluated (name + name+variant
	// rows, capped) — persisted so later offers can be gated against real
	// member evidence, not only the label.
	Evidence []string
	// Match records the attaching tier: "exact" for identifier claims,
	// "embed:<cos>" for a gated vector match, "seed" for the creating row.
	Match string
}

// Candidate is a stored group returned by a nearest-centroid lookup.
type Candidate struct {
	ID      int64
	Label   string
	Members int
	Vector  []float32
	Dist    float64 // cosine distance — similarity = 1 - Dist
}

// NewStore connects to the groups database, applies schema.sql once and
// verifies the vector column matches the configured embedding dimension —
// a model/dim change leaves the old vectors incompatible, and refusing to
// start beats silently matching in the wrong space.
func NewStore(ctx context.Context, dsn string, dim int) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("group store connect: %w", err)
	}
	s := &Store{pool: pool, dim: dim}
	// The DDL is idempotent (IF NOT EXISTS throughout), so it applies on
	// every connect — a transient failure must not permanently skip
	// provisioning the way a package-level sync.Once would.
	if err := s.applySchema(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("group store schema: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS keeps a pre-existing column's dimension —
	// verify it actually matches the configured model or every fold would
	// silently no-op against a foreign vector space.
	var colDim int
	err = pool.QueryRow(ctx, `
		SELECT atttypmod FROM pg_attribute
		 WHERE attrelid = 'product_groups'::regclass AND attname = 'embedding'`).Scan(&colDim)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("group store dim probe: %w", err)
	}
	if colDim != dim {
		pool.Close()
		return nil, fmt.Errorf("group store: embedding column is vector(%d), configured EMBED_DIM=%d — recreate the table or fix the dim", colDim, dim)
	}
	return s, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

func (s *Store) applySchema(ctx context.Context) error {
	ddl := strings.ReplaceAll(schemaSQL, "__DIM__", strconv.Itoa(s.dim))
	if _, err := s.pool.Exec(ctx, ddl); err != nil {
		return err
	}
	return nil
}

// vectorLit renders a float32 slice as a pgvector literal for the
// `::vector` cast — the same format learnings' store uses.
func vectorLit(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// GroupByKey resolves an exact-identifier claim to its group id, or 0.
func (s *Store) GroupByKey(ctx context.Context, exactKey string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT group_id FROM product_group_keys WHERE exact_key = $1`, exactKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// GroupKeys lists the exact-identifier claims a group holds — the
// keyspace-conflict gate consults it before letting a keyed offer join
// through embedding.
func (s *Store) GroupKeys(ctx context.Context, gid int64) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT exact_key FROM product_group_keys WHERE group_id = $1`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Nearest returns the k groups whose centroids sit closest to vec under
// cosine distance, restricted to rows embedded by the current model —
// vectors from a different model live in a different space and must never
// join.
func (s *Store) Nearest(ctx context.Context, vec []float32, model string, k int) ([]Candidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, label, members, embedding::text,
		       embedding <=> $1::vector AS dist
		  FROM product_groups
		 WHERE model = $2 AND embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $3`, vectorLit(vec), model, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		var emb string
		if err := rows.Scan(&c.ID, &c.Label, &c.Members, &emb, &c.Dist); err != nil {
			return nil, err
		}
		c.Vector = parseVector(emb)
		out = append(out, c)
	}
	return out, rows.Err()
}

// parseVector converts pgvector's '[1,2,3]' text form back to float32 —
// the centroid is read so a joined member can advance it.
func parseVector(s string) []float32 {
	s = strings.Trim(s, "[]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	v := make([]float32, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil
		}
		v = append(v, float32(f))
	}
	return v
}

// MemberURLs lists a group's member offer URLs, freshest first —
// group-kind watches re-read the head of the list each check.
func (s *Store) MemberURLs(ctx context.Context, gid int64, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT url FROM product_group_members
		  WHERE group_id = $1
		  ORDER BY last_seen DESC LIMIT $2`, gid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GroupByURL resolves a canonical offer URL to its group id — the
// reverse member lookup price-history joins use (issue #98), 0 when the
// URL never proved membership.
func (s *Store) GroupByURL(ctx context.Context, url string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT group_id FROM product_group_members WHERE url = $1
		  ORDER BY last_seen DESC LIMIT 1`, url).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// MemberTexts returns up to limit member records of a group — the gate
// checks a new offer against real member evidence, not only the label.
func (s *Store) MemberTexts(ctx context.Context, gid int64, limit int) ([]Member, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT title, evidence FROM product_group_members
		  WHERE group_id = $1 AND title <> ''
		  ORDER BY first_seen LIMIT $2`, gid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.Title, &m.Evidence); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CreateGroup inserts a new group with its seed member and exact-key
// claims in one transaction. A key another group claimed concurrently
// (ON CONFLICT hit) resolves the whole attempt to that winner instead:
// the new row is rolled back and the member lands on the group the key
// points at — otherwise the loser group would persist as an unreachable
// duplicate with a stranded member. The returned id is always the group
// the member actually joined.
func (s *Store) CreateGroup(ctx context.Context, label string, vec []float32, model string, m Member, exactKeys []string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	gid, winner, err := seedGroup(ctx, tx, label, vec, model, m, exactKeys)
	if err != nil {
		return 0, err
	}
	if winner == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		committed = true
		return gid, nil
	}
	if err := tx.Rollback(ctx); err != nil {
		return 0, err
	}
	// Retry all claims on the winner — the conflicting ones DO NOTHING,
	// the rest (a different keyspace the same product legitimately
	// carries) attach there instead of being dropped with our rollback.
	if err := s.AddMember(ctx, winner, m, vec, exactKeys); err != nil {
		return 0, fmt.Errorf("group store: claim redirect to %d: %w", winner, err)
	}
	return winner, nil
}

// seedGroup performs the group+member+claim inserts inside tx and then
// audits the claims: a key another group won under us (its claim
// committed while our INSERT waited on the unique index) reports the
// winner's group id so the caller can roll back and redirect.
func seedGroup(ctx context.Context, tx pgx.Tx, label string, vec []float32, model string, m Member, exactKeys []string) (gid, winner int64, err error) {
	var vecArg any
	members := 0 // centroid sample count — vectors folded so far
	if len(vec) > 0 {
		vecArg = vectorLit(vec)
		members = 1
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO product_groups (label, embedding, model, members)
		VALUES ($1, $2::vector, $3, $4) RETURNING id`,
		label, vecArg, model, members).Scan(&gid)
	if err != nil {
		return 0, 0, err
	}
	if _, err := insertMember(ctx, tx, gid, m); err != nil {
		return 0, 0, err
	}
	for _, k := range exactKeys {
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_group_keys (exact_key, group_id)
			VALUES ($1, $2) ON CONFLICT (exact_key) DO NOTHING`, k, gid); err != nil {
			return 0, 0, err
		}
	}
	for _, k := range exactKeys {
		var owner int64
		err := tx.QueryRow(ctx,
			`SELECT group_id FROM product_group_keys WHERE exact_key = $1`, k).Scan(&owner)
		if err != nil {
			return 0, 0, err
		}
		if owner != gid && (winner == 0 || owner < winner) {
			winner = owner
		}
	}
	return gid, winner, nil
}

// AddMember records an offer's membership and, when the member is new and
// carries vec, advances the group centroid by a running mean — e5 vectors
// are L2-normalised so the renormalised mean stays a valid centroid. A
// re-seen URL refreshes last_seen but never re-folds: frequently searched
// offers must not skew the representative.
func (s *Store) AddMember(ctx context.Context, gid int64, m Member, vec []float32, exactKeys []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inserted, err := insertMember(ctx, tx, gid, m)
	if err != nil {
		return err
	}
	if inserted && len(vec) > 0 {
		var emb string
		var n int
		err := tx.QueryRow(ctx,
			`SELECT COALESCE(embedding::text, ''), members FROM product_groups WHERE id = $1 FOR UPDATE`, gid).
			Scan(&emb, &n)
		if err != nil {
			return err
		}
		switch cur := parseVector(emb); {
		case len(cur) == len(vec):
			// Running mean over the n vectors folded so far.
			next := make([]float32, len(cur))
			for i := range cur {
				next[i] = (cur[i]*float32(n) + vec[i]) / float32(n+1)
			}
			l2norm(next)
			if _, err := tx.Exec(ctx,
				`UPDATE product_groups SET embedding = $1::vector, members = $2, updated_at = now() WHERE id = $3`,
				vectorLit(next), n+1, gid); err != nil {
				return err
			}
		case len(cur) == 0:
			// First vector for a group seeded without one.
			if _, err := tx.Exec(ctx,
				`UPDATE product_groups SET embedding = $1::vector, members = 1, updated_at = now() WHERE id = $2`,
				vectorLit(vec), gid); err != nil {
				return err
			}
		}
		// A stored vector of a different dimension belongs to another
		// model space — leave the centroid untouched.
	}
	for _, k := range exactKeys {
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_group_keys (exact_key, group_id)
			VALUES ($1, $2) ON CONFLICT (exact_key) DO NOTHING`, k, gid); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// insertMember inserts the offer row, reporting whether it was new. A
// re-seen URL only refreshes last_seen; the first-seen match provenance
// stands — re-attaching through a different tier does not rewrite how
// the member originally joined.
func insertMember(ctx context.Context, tx pgx.Tx, gid int64, m Member) (bool, error) {
	var inserted bool
	err := tx.QueryRow(ctx, `
		INSERT INTO product_group_members (group_id, url, domain, title, evidence, match)
		VALUES ($1, $2, $3, $4, COALESCE($5, '{}'::text[]), $6)
		ON CONFLICT (group_id, url) DO NOTHING
		RETURNING true`,
		gid, m.URL, m.Domain, m.Title, m.Evidence, m.Match).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		_, uerr := tx.Exec(ctx,
			`UPDATE product_group_members SET last_seen = now() WHERE group_id = $1 AND url = $2`,
			gid, m.URL)
		return false, uerr
	}
	return inserted, err
}

// l2norm normalises in place — a shared helper so the centroid write path
// cannot drift from the e5 convention.
func l2norm(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}
