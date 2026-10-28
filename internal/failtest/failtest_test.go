package failtest_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
)

func TestTB_WhenAReportIsNotFatal_RecordsItWithoutEndingTheGoroutine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		report     func(spy *failtest.TB)
		name       string
		messages   []string
		wantFailed bool
	}{
		{name: "Log", report: func(spy *failtest.TB) { spy.Log("logged") }, messages: []string{"logged"}},
		{
			name:     "Logf",
			report:   func(spy *failtest.TB) { spy.Logf("logged %d", 2) },
			messages: []string{"logged 2"},
		},
		{
			name:       "Error",
			report:     func(spy *failtest.TB) { spy.Error("failed") },
			messages:   []string{"failed"},
			wantFailed: true,
		},
		{
			name:       "Errorf",
			report:     func(spy *failtest.TB) { spy.Errorf("failed %d", 2) },
			messages:   []string{"failed 2"},
			wantFailed: true,
		},
		{name: "Fail", report: func(spy *failtest.TB) { spy.Fail() }, wantFailed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spy := failtest.New(t)
			returned := false

			spy.Run(func() {
				tt.report(spy)
				returned = true
			})

			assert.Check(t, returned, "%s ended the goroutine", tt.name)
			assert.Check(t, !spy.Stopped())
			assert.Check(t, is.Equal(spy.Failed(), tt.wantFailed))
			assert.Check(t, is.DeepEqual(spy.Messages(), tt.messages))
		})
	}
}

func TestTB_WhenAFailureIsFatal_EndsTheGoroutineThatReportedIt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fatal func(spy *failtest.TB)
		name  string
	}{
		{name: "Fatalf", fatal: func(spy *failtest.TB) { spy.Fatalf("fatal %s", "now") }},
		{name: "Fatal", fatal: func(spy *failtest.TB) { spy.Fatal("fatal now") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spy := failtest.New(t)
			returned := false

			spy.Run(func() {
				spy.Logf("logged %s", "before")
				tt.fatal(spy)
				returned = true
			})

			assert.Check(t, !returned, "%s did not end the goroutine", tt.name)
			assert.Check(t, spy.Failed())
			assert.Check(t, spy.Stopped())
			assert.Check(t, is.DeepEqual(spy.Messages(), []string{"logged before", "fatal now"}))
		})
	}
}
