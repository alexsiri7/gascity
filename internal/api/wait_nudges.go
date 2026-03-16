package api

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func withdrawQueuedWaitNudges(store beads.Store, cityPath string, ids []string) error {
	return nudgequeue.WithdrawWaitNudges(store, cityPath, ids)
}
