-- +goose Up
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
CREATE TABLE actions (
  id            INTEGER PRIMARY KEY,
  ts            TEXT NOT NULL,
  action        TEXT NOT NULL,
  payload_json  TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE actions;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE suggestions;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE tracked_files;
-- +goose StatementEnd
