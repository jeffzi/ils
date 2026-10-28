// Package failtest records the failures a test helper reports, for tests that check when a helper
// fails the test it is given.
package failtest

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// TB is a testing.TB that records each failure and message reported through it instead of passing
// it on. A failure that is not fatal is recorded and the caller goes on; a fatal one also ends the
// goroutine that reported it, as it ends a test, so a helper that fails fatally is called through
// Run. Every call TB does not record reaches the testing.TB it was made from.
type TB struct {
	testing.TB

	messages []string
	mu       sync.Mutex
	failed   bool
	stopped  bool
}

// New returns a TB that records the failures reported through it and passes every other call on
// to tb.
func New(tb testing.TB) *TB {
	return &TB{TB: tb}
}

// Run calls fn in a goroutine of its own and waits for it to return or to end by a fatal failure.
func (s *TB) Run(fn func()) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		fn()
	}()
	<-done
}

// Failed reports whether a failure was reported, fatal or not.
func (s *TB) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.failed
}

// Stopped reports whether a fatal failure was reported.
func (s *TB) Stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.stopped
}

// Messages returns every message logged or reported through s, in order.
func (s *TB) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.messages...)
}

// Helper marks nothing: the recorded calls report no position.
func (*TB) Helper() {}

// Log records its arguments as a message.
func (s *TB) Log(args ...any) { s.record(fmt.Sprint(args...), false) }

// Logf records the formatted message.
func (s *TB) Logf(format string, args ...any) { s.record(fmt.Sprintf(format, args...), false) }

// Fail records a failure.
func (s *TB) Fail() { s.record("", true) }

// Error records its arguments as a message, and a failure.
func (s *TB) Error(args ...any) { s.record(fmt.Sprint(args...), true) }

// Errorf records the formatted message and a failure.
func (s *TB) Errorf(format string, args ...any) { s.record(fmt.Sprintf(format, args...), true) }

// FailNow records a fatal failure and ends the calling goroutine.
func (s *TB) FailNow() {
	s.mu.Lock()
	s.failed = true
	s.stopped = true
	s.mu.Unlock()

	runtime.Goexit()
}

// Fatal records its arguments as a message and a fatal failure, and ends the calling goroutine.
func (s *TB) Fatal(args ...any) {
	s.record(fmt.Sprint(args...), false)
	s.FailNow()
}

// Fatalf records the formatted message and a fatal failure, and ends the calling goroutine.
func (s *TB) Fatalf(format string, args ...any) {
	s.record(fmt.Sprintf(format, args...), false)
	s.FailNow()
}

// record keeps message, when there is one, and marks a failure when failed is set.
func (s *TB) record(message string, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if message != "" {
		s.messages = append(s.messages, message)
	}

	s.failed = s.failed || failed
}
