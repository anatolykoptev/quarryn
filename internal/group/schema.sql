-- Product-group registry for embedding-tier offer grouping (issue #98).
-- Self-applied by internal/group.NewStore — the store lives on the
-- pgvector-capable host, outside the goose migration chain that owns the
-- watches/orders database.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS product_groups (
    id         BIGSERIAL PRIMARY KEY,
    label      TEXT NOT NULL,             -- canonical name: first member's embedded text
    embedding  vector(__DIM__),           -- running centroid; NULL until a member embeds
    model      TEXT NOT NULL DEFAULT '',  -- embed model — vectors only match inside their own model space
    members    INT NOT NULL DEFAULT 0,    -- centroid sample count: vectors folded so far (running-mean divisor)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS product_groups_embedding_hnsw
    ON product_groups USING hnsw (embedding vector_cosine_ops);

-- Exact-identifier claims: gtin:/mpn:/sku: GroupKey → group. One claim
-- belongs to one group; a concurrent second group offering the same key
-- forfeits the claim (its members still record membership by URL).
CREATE TABLE IF NOT EXISTS product_group_keys (
    exact_key TEXT PRIMARY KEY,
    group_id  BIGINT NOT NULL REFERENCES product_groups(id) ON DELETE CASCADE
);

-- Membership evidence: every offer that joined a group, how it joined,
-- and when it was last seen. URL is canonicalised by the caller.
CREATE TABLE IF NOT EXISTS product_group_members (
    group_id   BIGINT NOT NULL REFERENCES product_groups(id) ON DELETE CASCADE,
    url        TEXT NOT NULL,
    domain     TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    evidence   TEXT[] NOT NULL DEFAULT '{}', -- spec-gate rows: name + name+variant titles
    match      TEXT NOT NULL,           -- seed | exact | embed:<cosine>
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, url)
);

CREATE INDEX IF NOT EXISTS product_group_members_url_idx
    ON product_group_members (url);
