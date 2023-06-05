package client

import (
	"context"
	"time"
)

func defaultContextTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		defaultTimeout := 600 * time.Second
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
	}

	return ctx, cancel
}
