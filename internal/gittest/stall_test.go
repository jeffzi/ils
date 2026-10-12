package gittest_test

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/gittest"
)

const (
	quiet    = 50 * time.Millisecond
	patience = 10 * time.Second
)

func TestSilentListener_WhenClientConnects_SendsNothing(t *testing.T) {
	t.Parallel()

	addr, connected := gittest.SilentListener(t)

	var dialer net.Dialer

	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.Check(t, conn.Close()) })

	select {
	case <-connected:
	case <-time.After(patience):
		t.Fatal("connected stayed open after a client connected, want it closed")
	}

	assert.NilError(t, conn.SetReadDeadline(time.Now().Add(quiet)))
	_, err = conn.Read(make([]byte, 1))
	assert.Check(t, is.ErrorIs(err, os.ErrDeadlineExceeded))
}

func TestEnding_WhenDoneIsOpen_HasNotEnded(t *testing.T) {
	t.Parallel()

	ctx := gittest.Ending(t.Context(), make(chan struct{}), context.DeadlineExceeded)

	err := ctx.Err()

	assert.Check(t, err)
}

func TestEnding_WhenDoneCloses_EndsWithTheError(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	ctx := gittest.Ending(t.Context(), done, context.DeadlineExceeded)
	close(done)

	err := ctx.Err()

	assert.Check(t, is.ErrorIs(err, context.DeadlineExceeded))

	select {
	case <-ctx.Done():
	default:
		t.Error("Done() is open after done closed, want it closed")
	}
}
