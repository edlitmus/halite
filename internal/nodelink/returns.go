package nodelink

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// Poster is the one method of transport.Client that PostReturn uses, so
// that a test can put anything behind it.
type Poster interface {
	Return(ctx context.Context, ret any) error
}

// ReturnTries is how many times a return is posted before it is given up.
const ReturnTries = 5

// ErrGaveUp is PostReturn's error when every try failed with something a
// retry might have fixed. It wraps the last of them.
var ErrGaveUp = errors.New("giving up on a return")

// retryDelay is the wait after the failed try numbered attempt, from 0: a
// second more each time. A variable so that a test need not wait 15 s.
var retryDelay = func(attempt int) time.Duration {
	return time.Duration(attempt+1) * time.Second
}

// PostReturn posts one return, retrying a failure that may succeed on a
// retry: a return lost because the connection blinked is a job that looks
// unanswered for ever.
//
// It answers nil when the hub took the return; the hub's own error when it
// refused it (transport.Permanent), which is not retried, because the hub
// understood and said no and a retry is a node arguing with it; ctx's
// error when ctx ended first; and ErrGaveUp after ReturnTries failures.
// failed is told of each failure that will be retried, for the caller to
// log.
//
// It waits after the last failure as after the others, before giving up.
// That is what halite-node did when this was its own code, and this is
// the node's behaviour moved, not changed.
func PostReturn(ctx context.Context, c Poster, ret any, failed func(err error)) error {
	var last error
	for attempt := 0; attempt < ReturnTries; attempt++ {
		err := c.Return(ctx, ret)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if transport.Permanent(err) {
			return err
		}
		last = err
		if failed != nil {
			failed(err)
		}
		select {
		case <-time.After(retryDelay(attempt)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("%w: %w", ErrGaveUp, last)
}
