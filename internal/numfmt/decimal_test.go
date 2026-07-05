package numfmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDecimalMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    DecimalMode
		wantErr bool
	}{
		{name: "auto", in: "auto", want: DecimalAuto},
		{name: "number", in: "number", want: DecimalNumber},
		{name: "string", in: "string", want: DecimalString},
		{name: "case-insensitive", in: "NUMBER", want: DecimalNumber},
		{name: "trimmed", in: "  string  ", want: DecimalString},
		{name: "empty errors", in: "", wantErr: true},
		{name: "unknown errors", in: "float", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseDecimalMode(tt.in)
			if tt.wantErr {
				require.ErrorContains(t, err, "invalid --format.decimal")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
