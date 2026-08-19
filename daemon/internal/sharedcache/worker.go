package sharedcache

import (
	"context"
	"time"

	"github.com/johnny/dualsub-next/daemon/internal/cache"
)

func SyncOutboxOnce(ctx context.Context, local *cache.Cache, remote *Client) (int, error) {
	if _, err := local.QueueHistoricalTranslationsForSync(ctx); err != nil {
		return 0, err
	}
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

func drainOutbox(ctx context.Context, local *cache.Cache, remote *Client) (int, error) {
	total := 0
	for {
		uploaded, err := SyncOutboxOnce(ctx, local, remote)
		total += uploaded
		if err != nil || uploaded == 0 {
			return total, err
		}
	}
}

func RunOutbox(ctx context.Context, local *cache.Cache, remote *Client, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	_, _ = drainOutbox(ctx, local, remote)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = drainOutbox(ctx, local, remote)
		}
	}
}
