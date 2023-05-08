package metalbond

import (
	"context"
	"time"
)

func PollImmediateWithContext(ctx context.Context, interval time.Duration, pollingFunc func(context.Context) (bool, error)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			ok, err := pollingFunc(ctx)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
			time.Sleep(interval)
		}
	}
}
