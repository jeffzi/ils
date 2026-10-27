package failtest_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
)

func TestTB_WhenAFailureIsNotFatal_GoesOn(t *testing.T) {
	t.Parallel()
	spy := failtest.New(t)
	returned := false

	spy.Run(func() {
		spy.Errorf("second %d", 2)
		spy.Fail()
		returned = true
	})

	assert.Check(t, returned, "a failure that is not fatal ended the goroutine")
	assert.Check(t, !spy.Stopped())
}

func TestTB_WhenAFailureIsNotFatal_RecordsIt(t *testing.T) {
	t.Parallel()
	spy := failtest.New(t)

	spy.Run(func() {
		spy.Log("first")
		spy.Errorf("second %d", 2)
		spy.Fail()
	})

	assert.Check(t, spy.Failed())
	assert.Check(t, is.DeepEqual(spy.Messages(), []string{"first", "second 2"}))
}

func TestTB_WhenAFailureIsFatal_EndsTheGoroutineThatReportedIt(t *testing.T) {
	t.Parallel()
	spy := failtest.New(t)
	returned := false

	spy.Run(func() {
		spy.Logf("logged %s", "before")
		spy.Fatalf("fatal %s", "now")
		returned = true
	})

	assert.Check(t, !returned, "Fatalf did not end the goroutine")
	assert.Check(t, spy.Failed())
	assert.Check(t, spy.Stopped())
	assert.Check(t, is.DeepEqual(spy.Messages(), []string{"logged before", "fatal now"}))
}
