-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS snapshots (
  id           TEXT PRIMARY KEY,
  file_id      INTEGER REFERENCES tracked_files(id) ON DELETE SET NULL,
  path         TEXT NOT NULL,
  hash         TEXT NOT NULL,
  size         INTEGER NOT NULL,
  reason       TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  storage_path TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_snapshots_path       ON snapshots(path);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_snapshots_hash       ON snapshots(hash);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_snapshots_file_id    ON snapshots(file_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_snapshots_created_at ON snapshots(created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_snapshots_created_at;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_snapshots_file_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_snapshots_hash;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_snapshots_path;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS snapshots;
-- +goose StatementEnd
