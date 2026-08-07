ALTER TABLE sites ADD COLUMN max_in_flight INTEGER NOT NULL DEFAULT 4 CHECK (max_in_flight > 0);
