-- Children are copied out and dropped before their parent, and every table is
-- recreated under its final name with no RENAME, so the rebuild doesn't depend
-- on the foreign_keys setting of the connection goose runs it on.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE file_id_map AS
  SELECT id AS int_id, lower(hex(randomblob(16))) AS text_id FROM tracked_files;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE snapshots_old AS SELECT * FROM snapshots;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE suggestions_old AS SELECT * FROM suggestions;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tracked_files_old AS SELECT * FROM tracked_files;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE snapshots;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE suggestions;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE tracked_files;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tracked_files (
  id            TEXT PRIMARY KEY NOT NULL,
  path          TEXT UNIQUE NOT NULL,
  display_path  TEXT NOT NULL,
  added_at      TEXT NOT NULL,
  last_hash     TEXT,
  last_synced   TEXT
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO tracked_files (id, path, display_path, added_at, last_hash, last_synced)
  SELECT m.text_id, t.path, t.display_path, t.added_at, t.last_hash, t.last_synced
  FROM tracked_files_old t JOIN file_id_map m ON m.int_id = t.id;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE suggestions (
  id            TEXT PRIMARY KEY,
  file_id       TEXT REFERENCES tracked_files(id) ON DELETE SET NULL,
  provider      TEXT NOT NULL,
  prompt        TEXT NOT NULL,
  diff          TEXT NOT NULL,
  status        TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  decided_at    TEXT
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at, decided_at)
  SELECT s.id, m.text_id, s.provider, s.prompt, s.diff, s.status, s.created_at, s.decided_at
  FROM suggestions_old s LEFT JOIN file_id_map m ON m.int_id = s.file_id;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE snapshots (
  id           TEXT PRIMARY KEY,
  file_id      TEXT REFERENCES tracked_files(id) ON DELETE SET NULL,
  path         TEXT NOT NULL,
  hash         TEXT NOT NULL,
  size         INTEGER NOT NULL,
  reason       TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  storage_path TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
  SELECT s.id, m.text_id, s.path, s.hash, s.size, s.reason, s.created_at, s.storage_path
  FROM snapshots_old s LEFT JOIN file_id_map m ON m.int_id = s.file_id;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_path       ON snapshots(path);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_hash       ON snapshots(hash);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_file_id    ON snapshots(file_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_created_at ON snapshots(created_at);
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE snapshots_old;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE suggestions_old;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE tracked_files_old;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE file_id_map;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TABLE file_id_map AS
  SELECT row_number() OVER (ORDER BY rowid) AS int_id, id AS text_id FROM tracked_files;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE snapshots_old AS SELECT * FROM snapshots;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE suggestions_old AS SELECT * FROM suggestions;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tracked_files_old AS SELECT * FROM tracked_files;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE snapshots;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE suggestions;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE tracked_files;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tracked_files (
  id            INTEGER PRIMARY KEY,
  path          TEXT UNIQUE NOT NULL,
  display_path  TEXT NOT NULL,
  added_at      TEXT NOT NULL,
  last_hash     TEXT,
  last_synced   TEXT
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO tracked_files (id, path, display_path, added_at, last_hash, last_synced)
  SELECT m.int_id, t.path, t.display_path, t.added_at, t.last_hash, t.last_synced
  FROM tracked_files_old t JOIN file_id_map m ON m.text_id = t.id;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE suggestions (
  id            TEXT PRIMARY KEY,
  file_id       INTEGER REFERENCES tracked_files(id),
  provider      TEXT NOT NULL,
  prompt        TEXT NOT NULL,
  diff          TEXT NOT NULL,
  status        TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  decided_at    TEXT
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO suggestions (id, file_id, provider, prompt, diff, status, created_at, decided_at)
  SELECT s.id, m.int_id, s.provider, s.prompt, s.diff, s.status, s.created_at, s.decided_at
  FROM suggestions_old s LEFT JOIN file_id_map m ON m.text_id = s.file_id;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE snapshots (
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
INSERT INTO snapshots (id, file_id, path, hash, size, reason, created_at, storage_path)
  SELECT s.id, m.int_id, s.path, s.hash, s.size, s.reason, s.created_at, s.storage_path
  FROM snapshots_old s LEFT JOIN file_id_map m ON m.text_id = s.file_id;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_path       ON snapshots(path);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_hash       ON snapshots(hash);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_file_id    ON snapshots(file_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_snapshots_created_at ON snapshots(created_at);
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE snapshots_old;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE suggestions_old;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE tracked_files_old;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE file_id_map;
-- +goose StatementEnd
