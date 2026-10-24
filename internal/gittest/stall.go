package gittest

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
)

// Serve returns the address of a local server that hands each client that connects to handle, one
// at a time, until the test ends.
//
// The server stops accepting when the test's cleanup closes it. Any other accept failure fails the
// test: a client left in the backlog with nobody to serve it would block until the test's timeout.
// handle runs on the server's goroutine, which the cleanup waits on, so it can still report through
// tb while the cleanup runs.
func Serve(tb testing.TB, handle func(conn net.Conn)) string {
	tb.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("gittest: listen on a local port: %v", err)
	}

	var accepting sync.WaitGroup

	tb.Cleanup(func() {
		if err := listener.Close(); err != nil {
			tb.Errorf("gittest: close the listener: %v", err)
		}

		accepting.Wait()
	})

	accepting.Go(func() {
		for {
			conn, acceptErr := listener.Accept()
			if errors.Is(acceptErr, net.ErrClosed) {
				return
			}

			if acceptErr != nil {
				tb.Errorf("gittest: accept a client: %v", acceptErr)

				return
			}

			handle(conn)
		}
	})

	return listener.Addr().String()
}

// SilentListener returns the address of a local server that accepts one client, never answers it,
// and holds the connection until the test ends, and a channel that closes once the client has connected.
func SilentListener(tb testing.TB) (addr string, connected <-chan struct{}) {
	tb.Helper()

	reached := make(chan struct{})
	released := make(chan struct{})

	var once sync.Once

	addr = Serve(tb, func(conn net.Conn) {
		once.Do(func() { close(reached) })
		<-released

		if err := conn.Close(); err != nil {
			tb.Errorf("gittest: close the silent connection: %v", err)
		}
	})

	// Registered after Serve's, so it runs first: the held connection is let go before the server
	// closes and waits on it.
	tb.Cleanup(func() { close(released) })

	return addr, reached
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

// Done returns the channel handed to Ending, which closes at the moment the call must be cut.
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
