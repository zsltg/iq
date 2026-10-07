package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runCompleteDir runs the hidden completion command of a fresh command tree and
// returns the candidate lines and the directive trailer, such as ":6".
func runCompleteDir(t *testing.T, name string, args ...string) ([]string, string) {
	t.Helper()
	root, _ := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{name}, args...))
	require.NoError(t, root.Execute())
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	require.True(t, strings.HasPrefix(last, ":"), "no directive line in %q", out.String())
	return lines[:len(lines)-1], last
}

// noDesc drops the description of each candidate line, as __completeNoDesc does.
func noDesc(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i], _, _ = strings.Cut(l, "\t")
	}
	return out
}

const (
	// dirPartial is NoSpace | NoFileComp.
	dirPartial = ":6"
	// dirDone is NoFileComp.
	dirDone = ":4"
)

// secretPassword is a synthetic password. No completion output may hold it.
const secretPassword = "dummysecret"

func TestCompleteURI(t *testing.T) {
	t.Setenv("IQ_CONFIG", t.TempDir()+"/absent.toml")
	tests := []struct {
		name string
		word string
		want []string
		dir  string
	}{
		// Scheme stage.
		{"empty word offers every scheme", "", nil, dirPartial},
		{"scheme prefix", "mon", []string{
			"mongodb://\tMongoDB document store",
			"mongodb+srv://\tMongoDB document store",
		}, dirPartial},
		{"scheme with a half separator", "redis:/", []string{"redis://\tRedis key-value store"}, dirPartial},
		{"scheme prefix that matches nothing", "zzz", nil, dirDone},
		{"unknown scheme with a separator", "zzz://h/?", nil, dirDone},
		{"file scheme", "fi", []string{"file://\tLocal dump file, read-only"}, dirPartial},

		// Host and path stage.
		{"host stage offers nothing", "cassandra://h", nil, dirDone},
		{"path stage offers nothing", "cassandra://h/ks", nil, dirDone},
		{"empty authority offers nothing", "hbase://", nil, dirDone},

		// Option names.
		{"option names after the question mark", "cassandra://h/ks?", []string{
			"cassandra://h/ks?table=\tdefault table",
			"cassandra://h/ks?consistency=\tread consistency level, quorum by default",
		}, dirPartial},
		{"option name prefix", "cassandra://h/ks?con", []string{
			"cassandra://h/ks?consistency=\tread consistency level, quorum by default",
		}, dirPartial},
		{"option name after an ampersand", "hbase://host/?table=books&keyt", []string{
			"hbase://host/?table=books&keytype=\tencoding of the row key",
		}, dirPartial},
		{"all option names after an ampersand", "dynamodb://us-east-1/?table=t&", []string{
			"dynamodb://us-east-1/?table=t&table=\tdefault table",
			"dynamodb://us-east-1/?table=t&endpoint=\tendpoint override, for DynamoDB Local",
		}, dirPartial},
		{"percent-encoded option name is decoded and replaced", "cassandra://h/ks?con%73", []string{
			"cassandra://h/ks?consistency=\tread consistency level, quorum by default",
		}, dirPartial},
		{"option name the driver does not read", "cassandra://h/ks?authSource", nil, dirDone},
		{"driver with no options", "redis://h/0?", nil, dirDone},
		{"incomplete percent escape in the name", "cassandra://h/ks?con%7", nil, dirDone},

		// Closed values.
		{"closed value prefix", "cassandra://h/ks?consistency=lo", []string{
			"cassandra://h/ks?consistency=local_quorum",
			"cassandra://h/ks?consistency=local_one",
		}, dirDone},
		{"closed value, all", "hbase://host/?keytype=", []string{
			"hbase://host/?keytype=text",
			"hbase://host/?keytype=bytes",
			"hbase://host/?keytype=int",
			"hbase://host/?keytype=long",
			"hbase://host/?keytype=double",
			"hbase://host/?keytype=bool",
		}, dirDone},
		{"closed value that matches nothing", "hbase://host/?keytype=zz", nil, dirDone},
		{"free value offers nothing", "hbase://host/?table=", nil, dirDone},
		{"closed value after an ampersand", "file:///d.dump?key=id&format=ya", []string{
			"file:///d.dump?key=id&format=yaml",
		}, dirDone},
		{"unknown option value offers nothing", "cassandra://h/ks?authSource=a", nil, dirDone},

		// Replacement keeps the typed bytes.
		{"earlier percent-encoded value stays", "hbase://host/?types=f%3Aq%3Dint&keytype=bo", []string{
			"hbase://host/?types=f%3Aq%3Dint&keytype=bool",
		}, dirDone},
		{"earlier unknown option stays", "cassandra://h/ks?authSource=x&consistency=on", []string{
			"cassandra://h/ks?authSource=x&consistency=one",
		}, dirDone},

		// Query boundary.
		{"question mark inside a value", "dynamodb://us-east-1/?endpoint=https://example.test/?", nil, dirDone},
		{"question mark inside a value, then an option", "dynamodb://us-east-1/?endpoint=https://example.test/?a=b&end", []string{
			"dynamodb://us-east-1/?endpoint=https://example.test/?a=b&endpoint=\tendpoint override, for DynamoDB Local",
		}, dirPartial},
		{"escaped ampersand inside a value", "hbase://host/?znode=a%26keyt", nil, dirDone},
		{"escaped ampersand then a real option", "hbase://host/?znode=a%26b&keyt", []string{
			"hbase://host/?znode=a%26b&keytype=\tencoding of the row key",
		}, dirPartial},
		{"incomplete percent escape in a value", "hbase://host/?znode=a%2&keyt", []string{
			"hbase://host/?znode=a%2&keytype=\tencoding of the row key",
		}, dirPartial},
		{"incomplete percent escape at the end of a value", "hbase://host/?keytype=%2", nil, dirDone},
		{"fragment after the authority", "hbase://host/?keyt#frag", nil, dirDone},
		{"fragment before the query", "hbase://host/#f?keyt", nil, dirDone},
		{"multi-host cassandra authority", "cassandra://h1:9042,h2:9042/ks?cons", []string{
			"cassandra://h1:9042,h2:9042/ks?consistency=\tread consistency level, quorum by default",
		}, dirPartial},
		{"multi-host hbase authority", "hbase://h1:2181,h2:2181/?keytype=in", []string{
			"hbase://h1:2181,h2:2181/?keytype=int",
		}, dirDone},
		{"user name without a password still completes", "mongodb://user@host/db?col", []string{
			"mongodb://user@host/db?collection=\tdefault collection",
		}, dirPartial},
		{"at sign in a value without a port colon", "mongodb://host/db?collection=a@b&col", []string{
			"mongodb://host/db?collection=a@b&collection=\tdefault collection",
		}, dirPartial},
	}
	// The empty word row lists every scheme. Take the expected set from the
	// registry so the row cannot drift from it.
	var all []string
	for _, d := range drivers {
		for _, s := range d.schemes {
			all = append(all, s+"://\t"+d.desc)
		}
	}
	tests[0].want = all
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dir := runCompleteDir(t, "__complete", "add", tt.word)
			require.Equal(t, orEmpty(tt.want), got)
			require.Equal(t, tt.dir, dir)
			got, dir = runCompleteDir(t, "__completeNoDesc", "add", tt.word)
			require.Equal(t, orEmpty(noDesc(tt.want)), got)
			require.Equal(t, tt.dir, dir)
		})
	}
}

// orEmpty maps nil to an empty slice, which is what the line split returns.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func TestCompleteURIGuards(t *testing.T) {
	t.Setenv("IQ_CONFIG", t.TempDir()+"/absent.toml")
	tests := []struct {
		name string
		word string
	}{
		{"password", "mongodb://u:" + secretPassword + "@h/db?col"},
		{"password with an unescaped slash", "mongodb://u:" + secretPassword + "/secret@host/db?col"},
		{"password with an unescaped question mark", "mongodb://u:" + secretPassword + "?x@host/db?col"},
		{"password with an unescaped hash", "mongodb://u:" + secretPassword + "#x@host/db?col"},
		{"password with a malformed percent escape", "mongodb://u:" + secretPassword + "%zz@host/db?col"},
		{"password with an at sign", "mongodb://u:" + secretPassword + "@x@host/db?col"},
		{"empty user with a password", "mongodb://:" + secretPassword + "@host/db?col"},
		{"password and a closed value", "cassandra://u:" + secretPassword + "@h/ks?consistency=lo"},
		{"password and a complete option", "mongodb://u:" + secretPassword + "@h/db?collection=a&"},
		{"port colon with an at sign in a value", "mongodb://host:27017/db?collection=a@b&col"},
		{"literal tab", "hbase://host/?keyt\t"},
		{"literal tab in the scheme", "mon\tgo"},
		{"literal newline", "hbase://host/?\nkeyt"},
		{"escape character", "hbase://host/?\x1b[2J"},
		{"delete character", "hbase://host/?keyt\x7f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range []string{"__complete", "__completeNoDesc"} {
				got, dir := runCompleteDir(t, name, "add", tt.word)
				require.Empty(t, got)
				require.Equal(t, dirDone, dir)
			}
		})
	}
}

// TestCompleteURINeverEchoesPassword feeds URIs that hold a password through
// the real command and checks that the password is in no output line.
func TestCompleteURINeverEchoesPassword(t *testing.T) {
	t.Setenv("IQ_CONFIG", t.TempDir()+"/absent.toml")
	tests := []struct {
		name string
		word string
	}{
		{"empty query", "mongodb://u:" + secretPassword + "@h/db?"},
		{"slash in the password", "mongodb://u:" + secretPassword + "/secret@host/db?col"},
		{"question mark in the password", "mongodb://u:" + secretPassword + "?x@host/db?col"},
		{"closed value", "cassandra://u:" + secretPassword + "@h/ks?consistency="},
		{"user name only", "mongodb://u@h/db?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range []string{"__complete", "__completeNoDesc"} {
				got, _ := runCompleteDir(t, name, "add", tt.word)
				require.NotContains(t, strings.Join(got, "\n"), secretPassword)
			}
		})
	}
}

func TestCompleteURIEncodedTabStaysUnchanged(t *testing.T) {
	t.Setenv("IQ_CONFIG", t.TempDir()+"/absent.toml")
	got, dir := runCompleteDir(t, "__completeNoDesc", "add", "hbase://host/?znode=a%09b&keyt")
	require.Equal(t, []string{"hbase://host/?znode=a%09b&keytype="}, got)
	require.Equal(t, dirPartial, dir)
}

func TestCompleteURISecondArgumentGivesNothing(t *testing.T) {
	t.Setenv("IQ_CONFIG", t.TempDir()+"/absent.toml")
	for _, name := range []string{"__complete", "__completeNoDesc"} {
		got, dir := runCompleteDir(t, name, "add", "mongodb://h/db", "mon")
		require.Empty(t, got)
		require.Equal(t, dirDone, dir)
	}
}
