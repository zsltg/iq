package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
)

// pprofModes are the profiles --debug.pprof can capture, mirroring sq's
// cli/pprofile mode set but implemented with the standard library
// (runtime/pprof and runtime/trace) so no dependency is added.
var pprofModes = []string{"cpu", "mem", "block", "mutex", "goroutine", "thread", "trace"}

// startProfile begins the profile named by mode, writing to a file in the
// current directory, and returns a stop func that finalizes and closes it while
// reporting the written path to msg. An empty mode is a no-op (nil stop, no
// error); an unknown mode errors before any file is created so a typo fails fast.
func startProfile(mode string, msg io.Writer) (func(), error) {
	switch mode {
	case "":
		return nil, nil
	case "cpu":
		f, err := createProfile(mode)
		if err != nil {
			return nil, err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("start cpu profile: %w", err)
		}
		return func() { pprof.StopCPUProfile(); finishProfile(f, mode, msg) }, nil
	case "trace":
		f, err := createProfile(mode)
		if err != nil {
			return nil, err
		}
		if err := trace.Start(f); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("start trace: %w", err)
		}
		return func() { trace.Stop(); finishProfile(f, mode, msg) }, nil
	case "mem", "block", "mutex", "goroutine", "thread":
		// These profiles are sampled over the run and written at stop. block and
		// mutex must be armed up front; heap is dumped after a GC for accuracy.
		switch mode {
		case "block":
			runtime.SetBlockProfileRate(1)
		case "mutex":
			runtime.SetMutexProfileFraction(1)
		}
		f, err := createProfile(mode)
		if err != nil {
			return nil, err
		}
		return func() {
			// GC before dumping so the heap profile reflects live memory; it is a
			// harmless no-op cost for the other sampled profiles.
			runtime.GC()
			if p := pprof.Lookup(lookupName(mode)); p != nil {
				_ = p.WriteTo(f, 0)
			}
			finishProfile(f, mode, msg)
		}, nil
	default:
		return nil, fmt.Errorf("invalid --debug.pprof %q: want one of %s", mode, strings.Join(pprofModes, ", "))
	}
}

// lookupName maps a --debug.pprof mode to the runtime/pprof profile it writes,
// where the flag name and the profile name differ.
func lookupName(mode string) string {
	switch mode {
	case "mem":
		return "heap"
	case "thread":
		return "threadcreate"
	default:
		return mode
	}
}

// createProfile creates the output file for mode in the current directory.
func createProfile(mode string) (*os.File, error) {
	f, err := os.Create(profileFilename(mode))
	if err != nil {
		return nil, fmt.Errorf("create profile file: %w", err)
	}
	return f, nil
}

// finishProfile closes the profile file and reports its absolute path to msg.
func finishProfile(f *os.File, mode string, msg io.Writer) {
	_ = f.Close()
	abs, err := filepath.Abs(f.Name())
	if err != nil {
		abs = f.Name()
	}
	_, _ = fmt.Fprintf(msg, "iq: wrote %s profile %s\n", mode, abs)
}

// profileFilename is the output name for a mode: trace uses the go-tool-trace
// convention trace.out, the rest use <mode>.pprof.
func profileFilename(mode string) string {
	if mode == "trace" {
		return "trace.out"
	}
	return mode + ".pprof"
}
