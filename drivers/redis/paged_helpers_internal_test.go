package redis

import (
	"context"

	goredis "github.com/redis/go-redis/v9"
)

// onePage returns the shared part of a reader whose key has one element and so
// needs no follow-up read.
func onePage(ctx context.Context) pagedRead {
	size := goredis.NewIntCmd(ctx)
	size.SetVal(1)
	return pagedRead{ctx: ctx, key: "k", typ: "test", size: size, limits: defaultLimits()}
}

// scanReply returns the reply of a SCAN-family command that holds flat and ends
// the cursor walk.
func scanReply(ctx context.Context, flat ...string) *goredis.ScanCmd {
	c := goredis.NewScanCmd(ctx, nil)
	c.SetVal(flat, 0)
	return c
}
