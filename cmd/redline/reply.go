package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chrisophus/redline/internal/conversation"
)

// cmdReply records an agent's reply to one comment the reviewer sent from the
// report page. The page shows it under that comment, and `redline post`
// carries both to the pull request.
func cmdReply(o opts) error {
	if o.replyID == "" {
		return errors.New("--id is required: the id of the comment you are answering")
	}
	text := o.replyText
	if text == "" {
		buf, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return err
		}
		text = string(buf)
	}
	if err := conversation.SetReply(o.out, o.replyID, text, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "redline: recorded the reply to %s\n", o.replyID)
	return nil
}
