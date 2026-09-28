package cmd

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNewProgressMeterGating asserts the meter is a no-op (nil) whenever it
// should not draw: when disabled, and when stderr is not a terminal (a buffer,
// a pipe, a redirected file). A nil meter's methods must stay safe.
func TestNewProgressMeterGating(t *testing.T) {
	tests := []struct {
		name     string
		disabled bool
	}{
		{name: "disabled by flag", disabled: true},
		{name: "not a terminal", disabled: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer // never a terminal
			m := newProgressMeter(&buf, tc.disabled)
			require.Nil(t, m, "a buffer is not a terminal, and disabled forces off")

			// Every method must be safe on the nil meter.
			m.Tick(5)
			m.Stop()
			require.Same(t, &buf, m.wrapStdout(&buf), "nil meter must not wrap stdout")
			require.Zero(t, buf.Len(), "a no-op meter writes nothing")
		})
	}
}

// TestNewProgressMeterOnATerminal is the other half of the gate: a terminal gets
// a meter, but --no-progress still turns it off. A pty master is the terminal, so
// the test needs no real console; it skips where none can be opened.
func TestNewProgressMeterOnATerminal(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS does not report a pty master as a terminal")
	}
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no pty available to act as a terminal")
	}
	t.Cleanup(func() { _ = f.Close() })
	require.True(t, isTerminalWriter(f), "a pty master reports as a terminal")

	require.Nil(t, newProgressMeter(f, true), "--no-progress turns the meter off even on a terminal")

	m := newProgressMeter(f, false)
	require.NotNil(t, m, "a terminal with progress on gets a meter")
	m.Stop()
}

// TestProgressMeterDelaySuppressesSpinner asserts a scan that finishes before
// the show delay never draws a spinner and never emits the clean-up escape.
func TestProgressMeterDelaySuppressesSpinner(t *testing.T) {
	var buf bytes.Buffer
	m := newMeter(&buf, time.Hour) // effectively never shows

	m.Tick(10)
	m.Tick(10)
	m.Stop()

	require.Zero(t, buf.Len(), "no spinner and no clear line before the delay elapses")
}

// TestProgressMeterShownClearsLine asserts that once the spinner has been shown,
// Stop erases its single line so nothing is left on the terminal.
func TestProgressMeterShownClearsLine(t *testing.T) {
	var buf bytes.Buffer
	m := newMeter(&buf, 0) // show immediately on first tick

	m.Tick(42)
	m.Stop()

	require.Contains(t, buf.String(), "\x1b[2K", "a shown spinner's line is erased on Stop")
}

// TestProgressMeterSpinnerNeverCompletes pins the spinner as indeterminate: even
// after a large scanned count is set, the bar must not complete, because its
// total is negative ("unknown"). This kills any mutation that flips the total to
// a non-negative value (which would turn the spinner into a completing bar).
func TestProgressMeterSpinnerNeverCompletes(t *testing.T) {
	var buf bytes.Buffer
	m := newMeter(&buf, 0) // show on the first tick
	defer m.Stop()

	m.Tick(1_000_000)

	require.NotNil(t, m.bar, "a tick past the delay creates the spinner")
	require.False(t, m.bar.Completed(), "an indeterminate spinner never completes, whatever the count")
	require.Equal(t, int64(1_000_000), m.bar.Current(), "the bar tracks the scanned count")
}

// TestProgressMeterLabel pins the spinner's trailing text: the running count
// alone when the backend gave no estimate, and "(~N est)" appended when it did.
func TestProgressMeterLabel(t *testing.T) {
	m := newMeter(&bytes.Buffer{}, time.Hour)
	defer m.Stop()

	m.Tick(3)
	require.Equal(t, "3 scanned", m.label(), "no estimate: count only")

	m.SetEstimate(50)
	require.Equal(t, "3 scanned (~50 est)", m.label(), "an estimate is shown as an approximate total")
}

// TestProgressMeterSetEstimate asserts a non-positive estimate is ignored: it
// neither sets a total from nothing nor clears one already set, so the boundary
// of the n<=0 guard is pinned exactly. A nil meter is safe.
func TestProgressMeterSetEstimate(t *testing.T) {
	t.Run("non-positive estimate is ignored", func(t *testing.T) {
		m := newMeter(&bytes.Buffer{}, time.Hour)
		defer m.Stop()

		m.SetEstimate(0)
		m.SetEstimate(-5)
		m.Tick(2)
		require.Equal(t, "2 scanned", m.label(), "a zero or negative estimate leaves the count bare")
	})

	t.Run("zero never clears an estimate already set", func(t *testing.T) {
		m := newMeter(&bytes.Buffer{}, time.Hour)
		defer m.Stop()

		m.SetEstimate(50)
		m.SetEstimate(0) // exactly the guard boundary: must not overwrite 50
		m.Tick(2)
		require.Equal(t, "2 scanned (~50 est)", m.label(), "a zero estimate must not wipe a real total")
	})

	t.Run("an estimate of one is kept", func(t *testing.T) {
		// The smallest positive estimate is on the useful side of the guard: one
		// item is still a total the label must show.
		m := newMeter(&bytes.Buffer{}, time.Hour)
		defer m.Stop()

		m.SetEstimate(1)
		m.Tick(1)
		require.Equal(t, "1 scanned (~1 est)", m.label(), "an estimate of one is a real total")
	})

	t.Run("nil meter is safe", func(t *testing.T) {
		var m *progressMeter
		require.NotPanics(t, func() { m.SetEstimate(10) }, "SetEstimate on a nil meter is a no-op")
	})
}

// TestMeterWriterPassThrough asserts the stdout wrapper writes bytes unchanged
// (its only side effect is timestamping the write for the quiet window).
func TestMeterWriterPassThrough(t *testing.T) {
	var buf bytes.Buffer
	m := newMeter(&bytes.Buffer{}, time.Hour)
	defer m.Stop()

	w := m.wrapStdout(&buf)
	payload := strings.Repeat("row\n", 100)
	n, err := w.Write([]byte(payload))

	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Equal(t, payload, buf.String())
	require.NotZero(t, m.lastOut.Load(), "a stdout write marks activity for the quiet window")
}

// TestProgressMeterQuietWindow asserts the animation only repaints once stdout
// has been idle past the quiet window, so a row burst is not interleaved.
func TestProgressMeterQuietWindow(t *testing.T) {
	m := newMeter(&bytes.Buffer{}, time.Hour)
	defer m.Stop()

	now := time.Now()
	require.True(t, m.quiet(now), "idle from the start is quiet")

	m.lastOut.Store(now.UnixNano())
	require.False(t, m.quiet(now), "a write just now is not quiet")
	require.False(t, m.quiet(now.Add(progressQuietWindow-time.Nanosecond)), "just under the window is not quiet")
	require.True(t, m.quiet(now.Add(progressQuietWindow)), "exactly the window is quiet")
	require.True(t, m.quiet(now.Add(2*progressQuietWindow)), "quiet again after the window")
}

// TestProgressMeterElapsedBoundary pins the show-delay comparison at its exact
// edge, so a boundary mutation of the delay check cannot survive.
func TestProgressMeterElapsedBoundary(t *testing.T) {
	m := newMeter(&bytes.Buffer{}, 500*time.Millisecond)
	defer m.Stop()

	require.False(t, m.elapsed(m.start.Add(m.delay-time.Nanosecond)), "just before the delay: not yet")
	require.True(t, m.elapsed(m.start.Add(m.delay)), "exactly at the delay: shown")
	require.True(t, m.elapsed(m.start.Add(m.delay+time.Nanosecond)), "past the delay: shown")
}
