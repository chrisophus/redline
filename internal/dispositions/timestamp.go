package dispositions

import (
	"encoding/json"
	"time"
)

// Timestamp marshals as ClickHouse's DateTime64(3) text form, UTC, which JSONEachRow accepts without a
// date_time_input_format override.
type Timestamp time.Time

const clickHouseTime = "2006-01-02 15:04:05.000"

// MarshalJSON writes the timestamp in UTC. The zero time writes the epoch, which a DateTime64 column holds.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	tm := time.Time(t).UTC()
	if tm.IsZero() {
		tm = time.Unix(0, 0).UTC()
	}
	return json.Marshal(tm.Format(clickHouseTime))
}

// Ts converts a time.Time.
func Ts(t time.Time) Timestamp { return Timestamp(t) }

// TsPtr converts a time, returning nil for the zero time so a Nullable column stores NULL.
func TsPtr(t time.Time) *Timestamp {
	if t.IsZero() {
		return nil
	}
	v := Timestamp(t)
	return &v
}
