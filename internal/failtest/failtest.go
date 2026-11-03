// Package failtest records the failures a test helper reports, for tests that check when a helper
// fails the test it is given.
package failtest

import (
	"fmt"
	"runtime"
	"strings"
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
	var wg sync.WaitGroup

	wg.Go(fn)
	wg.Wait()
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

// Messages returns every message logged or reported through s, in order. An empty message is
// recorded like any other, as testing.T logs an empty line; Fail and FailNow record no message.
func (s *TB) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.messages...)
}

// Helper marks nothing: the recorded calls report no position.
func (*TB) Helper() {}

// Log records its arguments as one message, joined as testing.T joins them. Log with no arguments,
// or with only an empty string, records an empty message.
func (s *TB) Log(args ...any) { s.logMessage(joinArgs(args)) }

// Logf records the formatted message.
func (s *TB) Logf(format string, args ...any) { s.logMessage(fmt.Sprintf(format, args...)) }

// Fail records a failure and no message.
func (s *TB) Fail() { s.fail(false) }

// Error records its arguments as one message, as Log does, and a failure. Error with no arguments
// records an empty message.
func (s *TB) Error(args ...any) {
	s.Log(args...)
	s.Fail()
}

// Errorf records the formatted message and a failure. A format that yields no text records an empty
// message.
func (s *TB) Errorf(format string, args ...any) {
	s.Logf(format, args...)
	s.Fail()
}

// FailNow records a fatal failure and no message, and ends the calling goroutine.
func (s *TB) FailNow() {
	s.fail(true)

	runtime.Goexit()
}

// Fatal records its arguments as one message, as Log does, and a fatal failure, and ends the
// calling goroutine. Fatal with no arguments records an empty message.
func (s *TB) Fatal(args ...any) {
	s.Log(args...)
	s.FailNow()
}

// Fatalf records the formatted message and a fatal failure, and ends the calling goroutine.
func (s *TB) Fatalf(format string, args ...any) {
	s.Logf(format, args...)
	s.FailNow()
}

func (s *TB) logMessage(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, message)
}

func (s *TB) fail(stopped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failed = true
	s.stopped = s.stopped || stopped
}

// joinArgs formats args as testing.T does: with a space between every pair of operands.
func joinArgs(args []any) string {
	return strings.TrimSuffix(fmt.Sprintln(args...), "\n")
}
