package failtest_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/failtest"
)

// returns runs report against spy on spy's goroutine and reports whether report returned.
func returns(spy *failtest.TB, report func(spy *failtest.TB)) bool {
	returned := false

	spy.Run(func() {
		report(spy)
		returned = true
	})

	return returned
}

func TestTB_WhenAReportIsNotFatal_RecordsItWithoutEndingTheGoroutine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		report       func(spy *failtest.TB)
		name         string
		wantMessages []string
		wantFailed   bool
	}{
		{name: "Log", report: func(spy *failtest.TB) { spy.Log("logged") }, wantMessages: []string{"logged"}},
		{
			name:         "Error",
			report:       func(spy *failtest.TB) { spy.Error("failed") },
			wantMessages: []string{"failed"},
			wantFailed:   true,
		},
		{
			name:         "Errorf",
			report:       func(spy *failtest.TB) { spy.Errorf("failed %d", 2) },
			wantMessages: []string{"failed 2"},
			wantFailed:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spy := failtest.New(t)

			returned := returns(spy, tt.report)

			assert.Check(t, returned, "%s ended the goroutine", tt.name)
			assert.Check(t, !spy.Stopped())
			assert.Check(t, is.Equal(spy.Failed(), tt.wantFailed))
			assert.Check(t, is.DeepEqual(spy.Messages(), tt.wantMessages))
		})
	}
}

func TestTB_WhenAFailureIsFatal_RecordsItBeforeEndingTheGoroutine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fatal        func(spy *failtest.TB)
		name         string
		wantMessages []string
	}{
		{
			name:         "Fatalf",
			fatal:        func(spy *failtest.TB) { spy.Fatalf("fatal %s", "now") },
			wantMessages: []string{"logged before", "fatal now"},
		},
		{
			name:         "Fatal",
			fatal:        func(spy *failtest.TB) { spy.Fatal("fatal now") },
			wantMessages: []string{"logged before", "fatal now"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spy := failtest.New(t)
			spy.Logf("logged %s", "before")

			returned := returns(spy, tt.fatal)

			assert.Check(t, !returned, "%s did not end the goroutine", tt.name)
			assert.Check(t, spy.Failed())
			assert.Check(t, spy.Stopped())
			assert.Check(t, is.DeepEqual(spy.Messages(), tt.wantMessages))
		})
	}
}
