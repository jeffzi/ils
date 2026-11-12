package guard_test

import (
	"strings"
	"testing"

	"github.com/jeffzi/ils/internal/guard"
)

func TestWriteDoubleQuoted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text stays", in: "a b", want: "a b"},
		{name: "backslash before dollar drops", in: `\$HOME`, want: "$HOME"},
		{name: "backslash before backtick drops", in: "\\`", want: "`"},
		{name: "backslash before double quote drops", in: `\"`, want: `"`},
		{name: "backslash before backslash drops", in: `\\`, want: `\`},
		{name: "backslash before newline drops with it", in: "a\\\nb", want: "ab"},
		{name: "backslash before other character stays", in: `\n`, want: `\n`},
		{name: "trailing backslash stays", in: `a\`, want: `a\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder

			guard.WriteDoubleQuoted(&b, tt.in)

			if got := b.String(); got != tt.want {
				t.Errorf("WriteDoubleQuoted(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
