CREATE TABLE config_revisions (
  revision INTEGER PRIMARY KEY AUTOINCREMENT,
  reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 256),
  created_at INTEGER NOT NULL CHECK (created_at >= 0)
);

CREATE TABLE config_outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  revision INTEGER NOT NULL UNIQUE REFERENCES config_revisions(revision) ON DELETE RESTRICT,
  topic TEXT NOT NULL DEFAULT 'runtime_config_changed'
    CHECK (topic = 'runtime_config_changed'),
  created_at INTEGER NOT NULL CHECK (created_at >= 0)
);
CREATE INDEX config_outbox_revision_idx ON config_outbox(revision, id);

INSERT INTO config_revisions(revision, reason, created_at)
VALUES (1, 'schema_bootstrap', 0);
INSERT INTO config_outbox(id, revision, topic, created_at)
VALUES (1, 1, 'runtime_config_changed', 0);
