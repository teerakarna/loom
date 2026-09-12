package ledger

import (
	"encoding/json"
	"time"
)

// InsertEvent appends one row to the events table. events is append-only by
// convention (see the schema comment in ledger.go) — there is no update or
// delete method, deliberately. sessionID may be empty when the event isn't
// tied to a particular session (e.g. an outcome recorded via the MCP server
// outside any Claude Code session).
func (d *DB) InsertEvent(ts time.Time, sessionID, kind string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO events (ts, session_id, kind, payload) VALUES (?, ?, ?, ?)`,
		formatTime(ts), sessionID, kind, string(body))
	return err
}

// EventRow is one row of the events table as read back. Payload is left as
// raw JSON — callers that know a specific event kind's shape can unmarshal
// it themselves; the ledger package has no opinion on payload schemas.
type EventRow struct {
	ID        int64
	Timestamp string
	SessionID string
	Kind      string
	Payload   json.RawMessage
}

// ListEvents returns every event row, most recent first.
func (d *DB) ListEvents() ([]EventRow, error) {
	rows, err := d.sql.Query(`SELECT id, ts, COALESCE(session_id, ''), kind, payload FROM events ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		var r EventRow
		var payload string
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.SessionID, &r.Kind, &payload); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload)
		out = append(out, r)
	}
	return out, rows.Err()
}
