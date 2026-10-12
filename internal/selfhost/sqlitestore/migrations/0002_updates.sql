CREATE TABLE update_settings (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	enabled TEXT NOT NULL DEFAULT 'unset' CHECK (enabled IN ('unset', 'on', 'off')),
	channel TEXT NOT NULL DEFAULT 'stable' CHECK (length(channel) BETWEEN 1 AND 32),
	last_feed TEXT NOT NULL DEFAULT '',
	checked_at INTEGER,
	last_error TEXT NOT NULL DEFAULT '' CHECK (length(last_error) <= 32)
) STRICT;

INSERT INTO update_settings (singleton) VALUES (1);

CREATE TABLE update_sequences (
	key_id TEXT PRIMARY KEY CHECK (length(key_id) BETWEEN 1 AND 64),
	sequence INTEGER NOT NULL CHECK (sequence >= 1)
) STRICT;
