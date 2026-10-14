package query

import "fmt"

// StageState is the planned use of one filter stage for a query.
type StageState int

// The stage states. StageNone is the zero value.
const (
	StageNone        StageState = iota // the store does not have the stage
	StageOff                           // the store has it, but it does not run for this predicate
	StageOn                            // it runs
	StageConditional                   // it runs only if a condition that the planner cannot check holds
)

// String returns the name of the state: none, off, on, or conditional.
func (s StageState) String() string {
	switch s {
	case StageNone:
		return "none"
	case StageOff:
		return "off"
	case StageOn:
		return "on"
	case StageConditional:
		return "conditional"
	default:
		return fmt.Sprintf("StageState(%d)", int(s))
	}
}

// MarshalText makes JSON and slog write the state as its name. It returns an
// error for a value outside the four states.
func (s StageState) MarshalText() ([]byte, error) {
	if s < StageNone || s > StageConditional {
		return nil, fmt.Errorf("invalid stage state %d", int(s))
	}
	return []byte(s.String()), nil
}

// StagePlan is the planned use of one filter stage.
type StagePlan struct {
	// State is the planned use of the stage.
	State StageState `json:"state"`
	// Scope says what the stage evaluates and how, for on and conditional. For
	// example "mongo find", or "rawpred on the raw _source of each hit, before
	// decode". It is empty for none and off.
	Scope string `json:"scope,omitempty"`
	// Note gives the reason for none and off, and the condition for conditional.
	// It is empty for on.
	Note string `json:"note,omitempty"`
}

// AccessPlan describes the backend calls a source will make for one query. A CLI
// adapter uses it to show a query plan without executing the query. It is a plain type
// that crosses the driver boundary: a driver builds one from the route classification
// and the pushed predicate, and the CLI renders it.
//
// Ops holds the human-readable planned operations in execution order. Filter is the
// server filter only, as a JSON-ready value (a MongoDB find filter, for example). It
// is nil when no server filter is planned. It never holds a client-side prefilter.
//
// Server and Prefilter describe the two filter stages of a scan route. A bounded plan
// leaves both at StageNone.
type AccessPlan struct {
	Ops       []string
	Filter    map[string]any
	Server    StagePlan
	Prefilter StagePlan
}

// NoPushNote is the note of a server stage that is off because no conjunct pushes.
const NoPushNote = "no conjunct pushes"

// ServerStage returns the server stage of a store whose push needs no unknown fact:
// on with the scope if a fragment exists, else off.
func ServerStage(scope string, pushes bool) StagePlan {
	if !pushes {
		return StagePlan{State: StageOff, Note: NoPushNote}
	}
	return StagePlan{State: StageOn, Scope: scope}
}
