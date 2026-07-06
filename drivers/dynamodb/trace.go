package dynamodb

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"

	smithymw "github.com/aws/smithy-go/middleware"
)

// traceMiddleware returns an AWS SDK API option that logs each executed DynamoDB
// operation to w, backing the CLI's --verbose command trace — the analogue of the
// Cassandra query observer. Only the operation name and the target table are written;
// the request parameters (which carry item data and filter values) are redacted, so no
// data or credential reaches the log. Writes are serialized against tearing, as the
// SDK invokes the middleware from multiple goroutines.
func traceMiddleware(w io.Writer) func(*smithymw.Stack) error {
	var mu sync.Mutex
	mw := smithymw.InitializeMiddlewareFunc("iqTrace",
		func(ctx context.Context, in smithymw.InitializeInput, next smithymw.InitializeHandler) (smithymw.InitializeOutput, smithymw.Metadata, error) {
			op := smithymw.GetOperationName(ctx)
			table := tableOf(in.Parameters)
			mu.Lock()
			if table != "" {
				_, _ = fmt.Fprintf(w, "ddb> %s %s\n", op, table)
			} else {
				_, _ = fmt.Fprintf(w, "ddb> %s\n", op)
			}
			mu.Unlock()
			return next.HandleInitialize(ctx, in)
		})
	return func(stack *smithymw.Stack) error {
		return stack.Initialize.Add(mw, smithymw.After)
	}
}

// tableOf extracts the TableName from an operation's input parameters when it carries
// one, so the trace names the table an operation touched. It reflects over the input
// struct rather than type-switching every operation, since only the common TableName
// field is of interest and it appears on most inputs.
func tableOf(params any) string {
	rv := reflect.ValueOf(params)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	f := rv.FieldByName("TableName")
	if f.IsValid() && f.Kind() == reflect.Pointer && !f.IsNil() {
		return f.Elem().String()
	}
	return ""
}
