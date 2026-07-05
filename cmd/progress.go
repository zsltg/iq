package cmd

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

// Progress tuning. A scan gives no upfront total (Redis SCAN, Mongo cursor), so
// the meter is an indeterminate spinner plus a running "scanned" count, never a
// percentage bar.
const (
	// progressDelay is how long a scan must run before its spinner first
	// appears, so a fast query never flashes one.
	progressDelay = 750 * time.Millisecond
	// progressRefreshRate is the spinner's animation cadence.
	progressRefreshRate = 130 * time.Millisecond
	// progressQuietWindow suppresses a spinner repaint for this long after a
	// stdout write, so streamed result rows never collide with the spinner line.
	progressQuietWindow = 120 * time.Millisecond
)

// progressMeter renders a scan-progress spinner on stderr while a query walks a
// keyspace. It is driven by the core's per-page RunOptions.OnPage hook (see
// Tick), keeping the query core UI-agnostic. A nil *progressMeter is a working
// no-op: every method is nil-safe, so callers wire it unconditionally and the
// constructor returns nil when progress is off or stderr is not a terminal.
//
// Coordination with streamed stdout: the container uses manual refresh, and the
// animation ticker skips a repaint whenever stdout was written within
// progressQuietWindow. During a sparse streaming scan (the case a spinner helps
// most) stdout is quiet, so the spinner animates; during a dense burst the
// spinner holds still rather than interleaving with the rows.
type progressMeter struct {
	prog    *mpb.Progress
	refresh chan any
	out     io.Writer // stderr, also used to erase the spinner line on Stop

	delay time.Duration
	start time.Time

	mu    sync.Mutex // guards bar and shown
	bar   *mpb.Bar
	shown bool

	scanned  atomic.Int64 // total items scanned so far
	estimate atomic.Int64 // backend's cheap approximate total, 0 when unknown
	lastOut  atomic.Int64 // UnixNano of the last stdout write, for the quiet window

	stop chan struct{}
	wg   sync.WaitGroup
}

// newProgressMeter builds a meter that renders on errOut, or returns nil (a
// no-op) when disabled or when errOut is not a terminal — so piped or redirected
// runs never draw a spinner and never touch the output stream.
func newProgressMeter(errOut io.Writer, disabled bool) *progressMeter {
	if disabled || !isTerminalWriter(errOut) {
		return nil
	}
	return newMeter(errOut, progressDelay)
}

// newMeter starts the container and its animation ticker against out with the
// given show delay. It renders regardless of whether out is a terminal (the TTY
// gate lives in newProgressMeter), so tests can drive it with a buffer.
func newMeter(out io.Writer, delay time.Duration) *progressMeter {
	refresh := make(chan any)
	m := &progressMeter{
		prog:    mpb.New(mpb.WithOutput(out), mpb.WithManualRefresh(refresh)),
		refresh: refresh,
		out:     out,
		delay:   delay,
		start:   time.Now(),
		stop:    make(chan struct{}),
	}
	m.wg.Add(1)
	go m.animate()
	return m
}

// isTerminalWriter reports whether w is a real terminal, mirroring the color
// package's detection: a non-*os.File writer (a pipe, a buffer, a file) is never
// a terminal.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// animate drives the spinner: on each tick it requests a repaint, unless stdout
// was written within the quiet window (then it skips, so a row burst is not
// interleaved with the spinner). It exits when Stop closes m.stop.
func (m *progressMeter) animate() {
	defer m.wg.Done()
	t := time.NewTicker(progressRefreshRate)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			if !m.quiet(now) {
				continue
			}
			select {
			case m.refresh <- now:
			case <-m.stop:
				return
			}
		}
	}
}

// quiet reports whether stdout has been idle long enough to repaint the spinner:
// idle from the start, or last written at least a full quiet window ago.
func (m *progressMeter) quiet(now time.Time) bool {
	last := m.lastOut.Load()
	return last == 0 || now.UnixNano()-last >= int64(progressQuietWindow)
}

// elapsed reports whether the show delay has passed by now, so the spinner may
// appear. Pulled out of Tick so the boundary is exercisable in a test.
func (m *progressMeter) elapsed(now time.Time) bool {
	return now.Sub(m.start) >= m.delay
}

// Tick records n newly scanned items and, once the show delay has elapsed,
// lazily creates the spinner. It is the RunOptions.OnPage callback, invoked once
// per scanned page. Nil-safe.
func (m *progressMeter) Tick(n int) {
	if m == nil {
		return
	}
	total := m.scanned.Add(int64(n))
	if !m.elapsed(time.Now()) {
		return
	}
	m.mu.Lock()
	if m.bar == nil {
		m.bar = m.prog.AddSpinner(
			spinnerIndeterminate,
			mpb.BarFillerTrim(),
			mpb.PrependDecorators(decor.Name("scanning ")),
			mpb.AppendDecorators(decor.Any(func(decor.Statistics) string {
				return m.label()
			})),
		)
		m.shown = true
	}
	bar := m.bar
	m.mu.Unlock()
	// Advance the bar's current. With an indeterminate (negative) total it never
	// completes, so it keeps spinning; the count is displayed by the decorator.
	bar.SetCurrent(total)
}

// SetEstimate records a cheap, approximate total the backend supplied before the
// scan, rendered next to the running count as "(~N est)". It is a hint only: the
// estimate may be stale and the actual scan can exceed it, so it never drives a
// percentage or completes the spinner. A zero or negative value is ignored,
// leaving the label the bare count and never clearing a total already set. It is
// the RunOptions.OnEstimate callback, invoked at most once per run. Nil-safe.
func (m *progressMeter) SetEstimate(n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.estimate.Store(n)
}

// label is the spinner's trailing text: the running scanned count, plus the
// backend's approximate total as "(~N est)" when one was supplied.
func (m *progressMeter) label() string {
	scanned := m.scanned.Load()
	if est := m.estimate.Load(); est > 0 {
		return fmt.Sprintf("%d scanned (~%d est)", scanned, est)
	}
	return fmt.Sprintf("%d scanned", scanned)
}

// spinnerIndeterminate is the mpb total for an unbounded spinner: a negative
// total means "no known total", so the bar never triggers completion and stays
// a spinner rather than a percentage bar.
const spinnerIndeterminate = -1

// wrapStdout wraps w so each write marks stdout active for the quiet window,
// keeping streamed rows from colliding with the spinner. A nil meter returns w
// unchanged.
func (m *progressMeter) wrapStdout(w io.Writer) io.Writer {
	if m == nil {
		return w
	}
	return &meterWriter{w: w, m: m}
}

// meterWriter is a pass-through writer that timestamps each stdout write so the
// animation ticker can suppress a colliding spinner repaint.
type meterWriter struct {
	w io.Writer
	m *progressMeter
}

func (mw *meterWriter) Write(p []byte) (int, error) {
	mw.m.lastOut.Store(time.Now().UnixNano())
	return mw.w.Write(p)
}

// Stop halts the animation, removes the spinner, and shuts the container down.
// A manual-refresh container does not erase itself on shutdown, so if the
// spinner was ever shown its single line is cleared explicitly. Nil-safe, and
// safe on both success and error paths. It clears nothing when no spinner ever
// appeared (a fast query under the delay).
func (m *progressMeter) Stop() {
	if m == nil {
		return
	}
	close(m.stop)
	m.wg.Wait()
	m.mu.Lock()
	bar, shown := m.bar, m.shown
	m.mu.Unlock()
	if bar != nil {
		// Abort with drop so the container's bar wait group can complete and
		// Wait returns instead of blocking on a spinner that never finishes.
		bar.Abort(true)
	}
	m.prog.Wait()
	if shown {
		// Carriage return + erase the whole line: remove the leftover spinner.
		_, _ = io.WriteString(m.out, "\r\x1b[2K")
	}
}
