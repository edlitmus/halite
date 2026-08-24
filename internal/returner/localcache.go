package returner

import (
	"context"
	"fmt"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

func init() {
	register("local_cache", false, func(opts Options) (Returner, error) {
		if opts.Post == nil {
			return nil, fmt.Errorf("the local_cache returner needs a hub to post to; " +
				"a node with no hub has no job cache to write into")
		}
		return &localCacheReturner{opts: opts}, nil
	})
}

// localCacheReturner files a return in the hub's job cache.
//
// Which is where a job's return goes anyway, so this returner exists
// for the one case that is not a job: a scheduled run, which the hub
// never dispatched. The hub refuses a return for a job it has no record
// of — correctly, since that is how a replay is caught — so this posts
// the run as its own job first.
//
// It carries no events. `event_return` to `local_cache` would be the
// bus writing into the cache of the hub that owns the bus, which is a
// loop rather than a destination.
type localCacheReturner struct{ opts Options }

func (r *localCacheReturner) Name() string { return "local_cache" }

func (r *localCacheReturner) Return(ctx context.Context, ret *job.Return) error {
	return r.opts.Post(ctx, ret)
}

func (r *localCacheReturner) Event(ctx context.Context, e *eventbus.Event) error {
	return ErrNoEvents
}

func (r *localCacheReturner) Close() error { return nil }
