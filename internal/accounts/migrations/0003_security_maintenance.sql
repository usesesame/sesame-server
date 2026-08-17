CREATE TABLE IF NOT EXISTS sesame_rate_limits (
  key TEXT PRIMARY KEY,
  window_started_at TIMESTAMPTZ NOT NULL,
  attempts INTEGER NOT NULL CHECK (attempts > 0),
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS sesame_rate_limits_updated_idx
  ON sesame_rate_limits(updated_at);
