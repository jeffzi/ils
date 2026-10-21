package gittest

import (
	"context"
	"net"
	"sync"
	"testing"
)

// SilentListener returns the address of a local server that accepts one client, never answers it,
// and holds the connection until the test ends, and a channel that closes once the client has connected.
func SilentListener(tb testing.TB) (addr string, connected <-chan struct{}) {
	tb.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("gittest: listen on a local port: %v", err)
	}

	var accepting sync.WaitGroup

	reached := make(chan struct{})
	released := make(chan struct{})

	tb.Cleanup(func() {
		close(released)

		if err := listener.Close(); err != nil {
			tb.Errorf("gittest: close the listener: %v", err)
		}

		accepting.Wait()
	})

	accepting.Go(func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		close(reached)
		<-released

		// The test's cleanup is still running here, waiting on accepting, so it can still report.
		if err := conn.Close(); err != nil {
			tb.Errorf("gittest: close the silent connection: %v", err)
		}
	})

	return listener.Addr().String(), reached
}

// ending is a context that ends with err once done closes.
type ending struct {
	context.Context

	done <-chan struct{}
	err  error
}

// Ending returns a context over parent that ends with err once done closes, and not before: a test
// closes done, or hands in a channel that closes, at the moment the call under test must be cut.
func Ending(parent context.Context, done <-chan struct{}, err error) context.Context {
	return ending{Context: parent, done: done, err: err}
}

func (e ending) Done() <-chan struct{} { return e.done }

// Err returns the context's error once done closed, and nil before.
func (e ending) Err() error {
	select {
	case <-e.done:
		return e.err
	default:
		return nil
	}
}
