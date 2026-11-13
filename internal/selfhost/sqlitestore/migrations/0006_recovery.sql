CREATE TABLE recovery (
	singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
	pending INTEGER NOT NULL CHECK (pending IN (0, 1)),
	generation TEXT NOT NULL CHECK (
		generation = '' OR (
			length(generation) = 32
			AND generation NOT GLOB '*[^0-9a-f]*'
		)
	),
	started_at INTEGER NOT NULL CHECK (started_at >= 0),
	completed_at INTEGER,
	CHECK (
		(pending = 0 AND generation = '' AND started_at = 0 AND completed_at IS NULL)
		OR (
			pending = 0
			AND length(generation) = 32
			AND started_at > 0
			AND completed_at IS NOT NULL
			AND completed_at >= started_at
		)
		OR (
			pending = 1
			AND length(generation) = 32
			AND started_at > 0
			AND completed_at IS NULL
		)
	)
) STRICT;

INSERT INTO recovery (singleton, pending, generation, started_at, completed_at)
VALUES (1, 0, '', 0, NULL);
