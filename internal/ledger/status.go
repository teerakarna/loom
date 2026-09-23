package ledger

// LedgerStatus is what `loom status` reports about the ledger itself, as
// opposed to what the ledger says about your usage. Read-only: nothing here
// ingests, so running it can never change the numbers it is reporting on.
// That distinction is the whole point of the command, since `loom report`
// re-ingests as a side effect and a user checking freshness with it would be
// changing the thing they are checking.
type LedgerStatus struct {
	Runs        int
	SessionRuns int
	AgentRuns   int
	Assets      int
	StaleAsset  int
	Events      int
	Policies    int

	// EarliestRun and LatestRun are run timestamps, not ingest timestamps.
	// The ledger does not record when a row was written - see the note in
	// status.go's caller about that gap.
	EarliestRun string
	LatestRun   string

	// AgentTypes counts distinct attributed agent types, and Unattributed
	// counts agent runs whose .meta.json could not be read. A high
	// unattributed count means per-agent-type policy is working on partial
	// data, which the user should be told rather than left to infer.
	AgentTypes   int
	Unattributed int

	// Lanes counts distinct project directories runs came from, and
	// UnattributedLanes counts runs that could not be placed in one (a
	// transcript outside the projects root). Same reasoning as agent types:
	// a filter is only as trustworthy as the proportion of data it can see.
	Lanes             int
	UnattributedLanes int
}

// Status gathers the ledger's own state in one pass. Every count is a plain
// aggregate; nothing here is derived or estimated, so constraint 11's
// evidence-versus-assumption distinction does not arise.
func (d *DB) Status() (LedgerStatus, error) {
	var s LedgerStatus
	row := d.sql.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN kind = 'session' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind = 'agent' THEN 1 ELSE 0 END), 0),
			COALESCE(MIN(started_at), ''),
			COALESCE(MAX(ended_at), ''),
			COALESCE(SUM(CASE WHEN kind = 'agent' AND agent_type = '' THEN 1 ELSE 0 END), 0)
		FROM runs`)
	if err := row.Scan(&s.Runs, &s.SessionRuns, &s.AgentRuns, &s.EarliestRun, &s.LatestRun, &s.Unattributed); err != nil {
		return s, err
	}

	counts := []struct {
		query string
		into  *int
	}{
		{`SELECT COUNT(*) FROM assets`, &s.Assets},
		{`SELECT COUNT(*) FROM assets WHERE status = 'stale'`, &s.StaleAsset},
		{`SELECT COUNT(*) FROM events`, &s.Events},
		{`SELECT COUNT(*) FROM policies`, &s.Policies},
		{`SELECT COUNT(DISTINCT agent_type) FROM runs WHERE kind = 'agent' AND agent_type != ''`, &s.AgentTypes},
		{`SELECT COUNT(DISTINCT lane) FROM runs WHERE lane != ''`, &s.Lanes},
		{`SELECT COUNT(*) FROM runs WHERE lane = ''`, &s.UnattributedLanes},
	}
	for _, c := range counts {
		if err := d.sql.QueryRow(c.query).Scan(c.into); err != nil {
			return s, err
		}
	}
	return s, nil
}

// KnownRun is one transcript the ledger has already read, with the size it was
// read at. Used to compare the ledger against what is currently on disk
// without ingesting anything.
type KnownRun struct {
	Path      string
	SizeBytes int64
}

// KnownRuns returns every transcript path the ledger has ingested, with the
// size recorded at the time. The caller stats the files itself: keeping the
// filesystem out of the ledger package means this stays a pure query, and the
// freshness comparison lives where the discovery logic already is.
func (d *DB) KnownRuns() ([]KnownRun, error) {
	rows, err := d.sql.Query(`SELECT path, size_bytes FROM runs`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []KnownRun
	for rows.Next() {
		var k KnownRun
		if err := rows.Scan(&k.Path, &k.SizeBytes); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
