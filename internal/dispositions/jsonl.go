package dispositions

import (
	"encoding/json"
	"io"
)

// Files the dispositions command writes, one JSON object per line. Each is one
// table's rows, so loading them into a database is a straight copy.
const (
	FileFindings     = "findings.jsonl"
	FileDispositions = "dispositions.jsonl"
	FilePRs          = "prs.jsonl"
)

// WriteJSONL writes rows as newline-delimited JSON.
func WriteJSONL[T any](w io.Writer, rows []T) error {
	enc := json.NewEncoder(w)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
