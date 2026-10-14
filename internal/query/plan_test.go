package query

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStageStateMarshal(t *testing.T) {
	tests := []struct {
		name  string
		state StageState
		want  string
	}{
		{"none", StageNone, `"none"`},
		{"off", StageOff, `"off"`},
		{"on", StageOn, `"on"`},
		{"conditional", StageConditional, `"conditional"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.state)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))
			require.Equal(t, tt.want, `"`+tt.state.String()+`"`)
		})
	}
}

func TestStageStateOutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		state StageState
	}{
		{"below", StageState(-1)},
		{"above", StageConditional + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := json.Marshal(tt.state)
			require.Error(t, err)
			_, err = tt.state.MarshalText()
			require.Error(t, err)
			require.Contains(t, tt.state.String(), "StageState(")
		})
	}
}

func TestStagePlanZeroValue(t *testing.T) {
	require.Equal(t, StageNone, StagePlan{}.State)
	got, err := json.Marshal(StagePlan{State: StageConditional, Scope: "mongo find", Note: "x"})
	require.NoError(t, err)
	require.JSONEq(t, `{"state":"conditional","scope":"mongo find","note":"x"}`, string(got))
	got, err = json.Marshal(StagePlan{State: StageOn, Scope: "mongo find"})
	require.NoError(t, err)
	require.JSONEq(t, `{"state":"on","scope":"mongo find"}`, string(got))
}
