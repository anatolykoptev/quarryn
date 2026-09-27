-- +goose Up
-- ADR-10 outcome sink: one row per picked listing — joins the jeff_gate
-- log events on request_id. Previously feedback.jsonl on the data volume;
-- Postgres is the queryable home going forward.
CREATE TABLE IF NOT EXISTS feedback (
    id         bigserial PRIMARY KEY,
    ts         timestamptz NOT NULL DEFAULT now(),
    request_id text NOT NULL,
    picked_url text NOT NULL,
    verdict    text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS feedback_request_id_idx ON feedback (request_id);

-- +goose Down
DROP TABLE IF EXISTS feedback;
