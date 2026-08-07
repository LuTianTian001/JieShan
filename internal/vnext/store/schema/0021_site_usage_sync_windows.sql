ALTER TABLE site_account_connections
  ADD COLUMN usage_sync_through_at INTEGER
  CHECK (usage_sync_through_at IS NULL OR usage_sync_through_at > 0);

-- Scheduled usage imports are frozen into durable windows. The opaque cursor
-- only advances in the same transaction that stores the corresponding page.
CREATE TABLE site_usage_sync_windows (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_account_connection_id INTEGER NOT NULL,
  site_id INTEGER NOT NULL,
  window_from_at INTEGER NOT NULL CHECK (window_from_at > 0),
  window_to_at INTEGER NOT NULL CHECK (window_to_at >= window_from_at),
  cursor TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY(site_id, site_account_connection_id)
    REFERENCES site_account_connections(site_id, id) ON DELETE CASCADE
);
CREATE INDEX site_usage_sync_windows_site_order_idx
  ON site_usage_sync_windows(site_id, id);
