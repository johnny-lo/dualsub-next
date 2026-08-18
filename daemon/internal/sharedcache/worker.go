package sharedcache

import (
	"context"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
)

func SyncOutboxOnce(ctx context.Context, local *cache.Cache, remote *Client) (int, error) {
	entries, err := local.PendingSyncEntries(ctx, 200)
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	acknowledged, err := remote.Import(ctx, entries)
	if err != nil {
		return 0, err
	}
	if err := local.AcknowledgeSyncEntries(ctx, acknowledged); err != nil {
		return 0, err
	}
	return len(acknowledged), nil
}

func RunOutbox(ctx context.Context, local *cache.Cache, remote *Client, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	_, _ = SyncOutboxOnce(ctx, local, remote)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = SyncOutboxOnce(ctx, local, remote)
		}
	}
}
