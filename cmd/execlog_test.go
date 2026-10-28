package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecLogParts(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantVerb     string
		wantOperands int
	}{
		{name: "no arguments", args: nil, wantVerb: "", wantOperands: 0},
		{name: "Redis verb in capitals", args: []string{"SET", "k", "v"}, wantVerb: "SET", wantOperands: 2},
		{name: "HBase verb in lowercase", args: []string{"get", "books", "42"}, wantVerb: "get", wantOperands: 2},
		{name: "verb with a dot", args: []string{"JSON.GET", "doc"}, wantVerb: "JSON.GET", wantOperands: 1},
		{name: "Mongo JSON command", args: []string{`{"find":"u","filter":{"pw":"s3cret"}}`}, wantVerb: execStatementLabel, wantOperands: 0},
		{name: "CQL statement", args: []string{"SELECT * FROM u WHERE pw = 's3cret'"}, wantVerb: execStatementLabel, wantOperands: 0},
		{name: "Cypher statement", args: []string{"MATCH (n {pw:'s3cret'}) RETURN n", `{"min":1}`}, wantVerb: execStatementLabel, wantOperands: 1},
		{name: "empty string", args: []string{""}, wantVerb: execStatementLabel, wantOperands: 0},
		{name: "word of 40 characters", args: []string{strings.Repeat("a", 40)}, wantVerb: execStatementLabel, wantOperands: 0},
		{name: "word of 32 characters", args: []string{strings.Repeat("a", 32)}, wantVerb: strings.Repeat("a", 32), wantOperands: 0},
		{name: "word that starts with a digit", args: []string{"1abc"}, wantVerb: execStatementLabel, wantOperands: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verb, operands := execLogParts(tt.args)
			require.Equal(t, tt.wantVerb, verb)
			require.Equal(t, tt.wantOperands, operands)
		})
	}
}

func TestExecCommandLogHasNoStatement(t *testing.T) {
	const secret = "s3cretToken"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "statement is not logged", args: []string{`{"insert":"u","documents":[{"pw":"` + secret + `"}]}`}, want: `"verb":"statement"`},
		{name: "bare verb is logged", args: []string{"SET", "k", secret}, want: `"verb":"SET"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newSeed()
			require.NoError(t, c.Add("bad", "redis://localhost:1/0"))
			seedConfig(t, c)

			root, _ := newRootCmd()
			args := append([]string{"--src", "bad", "--timeout", "1s", "--log.file=stderr", "--log.format=json", "exec"}, tt.args...)
			out, _ := runCmd(t, root, args...)
			require.Contains(t, out, `"exec forward"`)
			require.Contains(t, out, tt.want)
			require.NotContains(t, out, secret)
		})
	}
}

func TestMaskPassword(t *testing.T) {
	const raw = "mongodb://u:p%40ss%2Fword@h/db"
	tests := []struct {
		name   string
		msg    string
		rawURL string
		want   string
	}{
		{name: "bare password", msg: "bad host p@ss/word", rawURL: raw, want: "bad host xxxxx"},
		{name: "path-escaped form", msg: "bad host p%40ss%2Fword", rawURL: raw, want: "bad host xxxxx"},
		{name: "query-escaped form", msg: "bad host p%40ss%2Fword and p%40ss%2Fword", rawURL: "mongodb://u:p%40ss%2Fword@h/db", want: "bad host xxxxx and xxxxx"},
		{name: "password with a space has two escapes", msg: "a pa ss b pa%20ss c pa+ss", rawURL: "x://u:pa%20ss@h", want: "a xxxxx b xxxxx c xxxxx"},
		{name: "short password stays", msg: "bad host abc", rawURL: "redis://u:abc@h", want: "bad host abc"},
		{name: "short password in a credential shape", msg: "authentication failed for u:abc@host", rawURL: "redis://u:abc@h", want: "authentication failed for u:xxxxx@host"},
		{name: "short password bare elsewhere stays", msg: "abc failed for u:abc@host and abc", rawURL: "redis://u:abc@h", want: "abc failed for u:xxxxx@host and abc"},
		{name: "one byte password in a credential shape", msg: "bad u:x@h", rawURL: "redis://u:x@h", want: "bad u:xxxxx@h"},
		{name: "short path-escaped password in a credential shape", msg: "bad u:a%2Fb@h", rawURL: "redis://u:a%2Fb@h", want: "bad u:xxxxx@h"},
		{name: "short query-escaped password in a credential shape", msg: "bad u:a%2Fb@h and u:a%2Fb@h", rawURL: "redis://u:a%2Fb@h", want: "bad u:xxxxx@h and u:xxxxx@h"},
		{name: "short password with a space, plus form", msg: "bad u:a+b@h and u:a%20b@h", rawURL: "redis://u:a%20b@h", want: "bad u:xxxxx@h and u:xxxxx@h"},
		{name: "four bytes is masked", msg: "bad host abcd", rawURL: "redis://u:abcd@h", want: "bad host xxxxx"},
		{name: "no password", msg: "bad host abcd", rawURL: "redis://u@h", want: "bad host abcd"},
		{name: "empty URL", msg: "bad host abcd", rawURL: "", want: "bad host abcd"},
		{name: "unparseable URL", msg: "bad host abcd", rawURL: "redis://u:abcd@h:x", want: "bad host abcd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, maskPassword(tt.msg, tt.rawURL))
		})
	}
}

func TestPasswordMaskMatchesRedactURL(t *testing.T) {
	require.Contains(t, redactURL("redis://u:abcd@h"), ":"+passwordMask+"@")
}

func TestRedactErrMasksBarePassword(t *testing.T) {
	sentinel := errors.New("boom")
	const rawURL = "hbase://zkuser:pw%20secret@zk1,,zk2/?table=t"
	err := fmt.Errorf("bad host list pw secret and pw%%20secret: %w", sentinel)
	got := redactErr(err, rawURL)
	require.EqualError(t, got, "bad host list xxxxx and xxxxx: boom")

	short := redactErr(fmt.Errorf("saw pw: %w", sentinel), "hbase://u:pw@h/")
	require.ErrorIs(t, short, sentinel, "a short password leaves the error unchanged")
}

func TestRedactErrMasksShortPasswordInPartialURI(t *testing.T) {
	sentinel := errors.New("boom")
	err := fmt.Errorf("authentication failed for u:abc@host: %w", sentinel)
	got := redactErr(err, "redis://u:abc@host/0")
	require.EqualError(t, got, "authentication failed for u:xxxxx@host: boom")
}

func TestLogFailureMasksShortPassword(t *testing.T) {
	var stderr bytes.Buffer
	opts := logOptions{enable: true, file: "stderr", level: slog.LevelDebug, format: "text"}
	logger, _, err := opts.build(&stderr, io.Discard)
	require.NoError(t, err)
	cfg := &config{logger: logger, url: "redis://u:abc@host/0"}

	logFailure(cfg, errors.New("authentication failed for u:abc@host"))

	require.Contains(t, stderr.String(), "iq failed")
	require.NotContains(t, stderr.String(), "abc")
}

func TestRenderErrorTextMasksBarePassword(t *testing.T) {
	var b strings.Builder
	cfg := &config{url: "redis://u:hunter22@h/0"}
	renderErrorText(&b, cfg, errors.New("lookup hunter22: no such host"))
	require.Equal(t, "iq: lookup xxxxx: no such host\n", b.String())
}
