// Package conversation keeps what a reviewer sent an agent from the report
// page and what the agent said back. It lives in the session directory as
// conversation.json, so the report server, `redline reply` and `redline post`
// all read the same record.
//
// The reviewer's queued comments otherwise live only in their browser's
// localStorage, where nothing outside the page can read them. Saving what was
// sent is what lets the page show the agent's replies and lets post carry the
// exchange to the pull request.
package conversation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File is the record's name inside the session directory.
const File = "conversation.json"

// Item is one comment the reviewer sent, with the agent's reply once it has
// written one. The fields mirror what the report page queues.
type Item struct {
	ID     string `json:"id"`
	Action string `json:"action,omitempty"` // comment, explain or apply
	On     string `json:"on,omitempty"`     // "finding" for a comment on a finding card
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Side   string `json:"side,omitempty"` // "new" or "old"
	Rule   string `json:"rule,omitempty"`
	Code   string `json:"code,omitempty"`
	Text   string `json:"text,omitempty"`
	Fix    string `json:"fix,omitempty"`

	SentAt    time.Time `json:"sentAt"`
	Reply     string    `json:"reply,omitempty"`
	RepliedAt time.Time `json:"repliedAt,omitzero"`
}

// Record is the whole file.
type Record struct {
	Items []Item `json:"items"`
}

// Load reads the record in dir. A missing file is an empty record.
func Load(dir string) (Record, error) {
	buf, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, os.ErrNotExist) {
		return Record{Items: []Item{}}, nil
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(buf, &r); err != nil {
		return Record{}, fmt.Errorf("%s: %w", File, err)
	}
	if r.Items == nil {
		r.Items = []Item{}
	}
	return r, nil
}

// save writes through a temporary file and a rename, so the page polling the
// record never reads half of it.
func save(dir string, r Record) error {
	buf, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, File+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(buf, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, File))
}

// AddSent records items as sent at now. An item whose ID is already there is
// replaced, keeping any reply it has, so sending the same comment twice does
// not post it twice.
func AddSent(dir string, items []Item, now time.Time) error {
	r, err := Load(dir)
	if err != nil {
		return err
	}
	at := map[string]int{}
	for i, it := range r.Items {
		at[it.ID] = i
	}
	for _, it := range items {
		if it.ID == "" {
			return errors.New("a sent item has no id")
		}
		it.SentAt = now
		if i, ok := at[it.ID]; ok {
			it.Reply, it.RepliedAt = r.Items[i].Reply, r.Items[i].RepliedAt
			r.Items[i] = it
			continue
		}
		at[it.ID] = len(r.Items)
		r.Items = append(r.Items, it)
	}
	return save(dir, r)
}

// SetReply stores the agent's reply to the item with id. A second reply
// replaces the first: the agent may revise what it said after re-running.
func SetReply(dir, id, reply string, now time.Time) error {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return errors.New("the reply is empty")
	}
	r, err := Load(dir)
	if err != nil {
		return err
	}
	for i := range r.Items {
		if r.Items[i].ID == id {
			r.Items[i].Reply, r.Items[i].RepliedAt = reply, now
			return save(dir, r)
		}
	}
	return fmt.Errorf("no sent comment with id %q in %s", id, filepath.Join(dir, File))
}
