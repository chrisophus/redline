package scout

import "github.com/chrisophus/redline/internal/envelope"

// The scout's account of itself, for the reader afterwards.
//
// A run is a model deciding what to look at, and the envelope it produces
// says only what it settled on. That is the right thing to put in front of
// the reviewer and the wrong thing to put in front of someone asking why a
// finding came back unverifiable: the record that was never filed, the grep
// that matched nothing, the path it mistyped and was corrected on, none of
// those reach the envelope. They are what says whether the search failed or
// the repository genuinely had no answer.
//
// So every tool call is kept as it was dispatched, bounded to a line, and
// handed back on the Spend. It costs a few kilobytes per run and nothing per
// turn: the strings are the ones the debug log already formats.

// Call is one tool call the scout made.
type Call struct {
	// Turn is which model turn dispatched it, counting from one.
	Turn int    `json:"turn"`
	Tool string `json:"tool"`
	// Args is the call's input, bounded to one line. It is the model's own
	// JSON with the whitespace collapsed, so a grep reads as its pattern and
	// a record reads as the range it filed.
	Args string `json:"args,omitempty"`
	// Result is what came back: the byte count on success, the error text on
	// a refusal. A refusal is the interesting half. It is a correction the
	// scout was given and either acted on or ran out of turns to act on.
	Result string `json:"result,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

// Filed is one record the scout accepted, before the resolver read a line of
// it. It is what the model asked for, which is not always what the reviewer
// got: a record whose file could not be read is filed here and missing from
// the envelope, and the difference is the whole point of keeping both.
type Filed struct {
	Role      envelope.Role `json:"role"`
	File      string        `json:"file"`
	StartLine int           `json:"startLine,omitempty"`
	EndLine   int           `json:"endLine,omitempty"`
	Symbol    string        `json:"symbol,omitempty"`
	FoundVia  string        `json:"foundVia,omitempty"`
	// Answers is the question id this record was filed against, when the run
	// had questions. Empty on an exploring run, and empty on an answering run
	// is the scout filing something it could not attach to anything.
	Answers string `json:"answers,omitempty"`
}

// Log is the whole run: every call in order, every record filed, and the
// notes that reached the envelope.
type Log struct {
	Calls []Call   `json:"calls,omitempty"`
	Filed []Filed  `json:"filed,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// logOf assembles the log once the loop is over. The notes are the
// envelope's, not the toolset's, because the resolver adds its own while it
// reads and those are exactly the ones that say a record went missing.
func logOf(ts *toolset, env *envelope.Envelope) Log {
	log := Log{Calls: ts.log}
	for _, rec := range ts.records {
		// A conversion, because Filed is record's fields with json tags on
		// them: what the scout asked for, in the shape a reader can be handed.
		// Adding a field to one and not the other stops this compiling, which
		// is the point of writing it this way rather than field by field.
		log.Filed = append(log.Filed, Filed(rec))
	}
	if env != nil {
		log.Notes = env.Notes
	}
	return log
}
