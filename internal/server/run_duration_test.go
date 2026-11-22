package server

import (
	"context"
	"testing"
)

func TestInteractiveRunHasNoFixedDeadlineAndCancelsWithServer(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	ctx, cancel := channelRunContext(parent, "")
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("interactive run unexpectedly has a fixed deadline")
	}
	stop()
	if ctx.Err() != context.Canceled {
		t.Fatalf("shutdown did not cancel run: %v", ctx.Err())
	}
	channel, done := channelRunContext(context.Background(), "feishu")
	defer done()
	if _, ok := channel.Deadline(); !ok {
		t.Fatal("channel request lost its existing deadline")
	}
}
