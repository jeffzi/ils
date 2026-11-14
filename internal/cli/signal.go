package cli

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// endSignals is the one listener of a run for the interrupt, the termination and the quit of the
// process, from the start of Run to its end, so that every one of these signals is counted alike
// whenever it lands.
type endSignals struct {
	// leave takes the spinner line off stderr; nil while no display runs.
	leave func()
	mu    sync.Mutex
}

// onSignal listens through Notify, for nothing when Notify is nil, for the interrupt, the
// termination and the quit of the process until the returned stop is called. The first interrupt
// or termination calls interrupt, which ends the run, so that ils prints the result of the step it
// stopped. Every other of these signals calls the leave of the display that runs, if one does,
// then ends the process as the signal would have had ils never listened.
//
// A process that started with the interrupt ignored, as a background job of a non-interactive
// shell does, is not listened to for it: Notify would take the ignore back, so the interrupt is
// left out and goes on being ignored.
func (a App) onSignal(interrupt func()) (signals *endSignals, stop func()) {
	signals = &endSignals{}
	if a.Notify == nil {
		return signals, func() {}
	}

	landed := make(chan os.Signal, 1)
	done := make(chan struct{})

	listened := []os.Signal{syscall.SIGTERM, syscall.SIGQUIT}
	if !signal.Ignored(os.Interrupt) {
		listened = append(listened, os.Interrupt)
	}

	a.Notify(landed, listened...)

	var wg sync.WaitGroup
	wg.Go(func() {
		sig, ok := nextSignal(landed, done)
		if ok && sig != syscall.SIGQUIT {
			interrupt()

			sig, ok = nextSignal(landed, done)
		}

		if !ok {
			return
		}

		signals.leaveDisplay()
		signal.Stop(landed)
		die(sig)
	})

	return signals, func() {
		signal.Stop(landed)
		close(done)
		wg.Wait()
	}
}

// during makes leave the call that takes the display off stderr before a signal ends the process,
// until the returned done is called.
func (s *endSignals) during(leave func()) (done func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.leave = leave

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.leave = nil
	}
}

// leaveDisplay takes the display that runs, if one does, off stderr.
func (s *endSignals) leaveDisplay() {
	s.mu.Lock()
	leave := s.leave
	s.mu.Unlock()

	if leave != nil {
		leave()
	}
}

// nextSignal returns the next signal on signals, or false when done is closed first and no signal
// is left.
func nextSignal(signals <-chan os.Signal, done <-chan struct{}) (os.Signal, bool) {
	select {
	case sig := <-signals:
		return sig, true
	case <-done:
		// stop detached the channel before closing done, so a signal that reached it before
		// stop was called is already buffered; select picks at random when both are ready.
		select {
		case sig := <-signals:
			return sig, true
		default:
			return nil, false
		}
	}
}

// die sends sig, which no channel is notified of any longer, to the process, so that it ends the
// process as it would have had ils never listened for it. When sig cannot be sent, the process is
// killed.
//
// It panics when the process can be neither signaled nor killed.
func die(sig os.Signal) {
	self, err := os.FindProcess(os.Getpid())
	if err == nil && self.Signal(sig) != nil {
		err = self.Kill()
	}
	if err != nil {
		panic(fmt.Sprintf("end ils on %v: %v", sig, err))
	}
}
