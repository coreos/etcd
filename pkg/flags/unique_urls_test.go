// Copyright 2018 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package flags

import (
	"flag"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewUniqueURLsWithExceptions(t *testing.T) {
	tests := []struct {
		s         string
		exp       map[string]struct{}
		rs        string
		exception string
	}{
		{ // non-URL but allowed by exception
			s:         "*",
			exp:       map[string]struct{}{"*": {}},
			rs:        "*",
			exception: "*",
		},
		{
			s:         "",
			exp:       map[string]struct{}{},
			rs:        "",
			exception: "*",
		},
		{
			s:         "https://1.2.3.4:8080",
			exp:       map[string]struct{}{"https://1.2.3.4:8080": {}},
			rs:        "https://1.2.3.4:8080",
			exception: "*",
		},
		{
			s:         "https://1.2.3.4:8080,https://1.2.3.4:8080",
			exp:       map[string]struct{}{"https://1.2.3.4:8080": {}},
			rs:        "https://1.2.3.4:8080",
			exception: "*",
		},
		{
			s:         "http://10.1.1.1:80",
			exp:       map[string]struct{}{"http://10.1.1.1:80": {}},
			rs:        "http://10.1.1.1:80",
			exception: "*",
		},
		{
			s:         "http://localhost:80",
			exp:       map[string]struct{}{"http://localhost:80": {}},
			rs:        "http://localhost:80",
			exception: "*",
		},
		{
			s:         "http://:80",
			exp:       map[string]struct{}{"http://:80": {}},
			rs:        "http://:80",
			exception: "*",
		},
		{
			s:         "https://localhost:5,https://localhost:3",
			exp:       map[string]struct{}{"https://localhost:3": {}, "https://localhost:5": {}},
			rs:        "https://localhost:3,https://localhost:5",
			exception: "*",
		},
		{
			s:         "http://localhost:5,https://localhost:3",
			exp:       map[string]struct{}{"https://localhost:3": {}, "http://localhost:5": {}},
			rs:        "http://localhost:5,https://localhost:3",
			exception: "*",
		},
	}
	for i := range tests {
		uv := NewUniqueURLsWithExceptions(tests[i].s, tests[i].exception)
		require.Equal(t, tests[i].exp, uv.Values)
		require.Equal(t, tests[i].rs, uv.String())
	}
}

func TestUniqueURLsSetReplacesPreviousValue(t *testing.T) {
	const name = "test"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Var(NewUniqueURLsWithExceptions("https://1.2.3.4:1"), name, "usage")
	require.NoError(t, fs.Parse([]string{
		"--" + name, "https://1.2.3.4:1,https://1.2.3.4:2",
		"--" + name, "https://1.2.3.4:1",
	}))

	require.Equal(t, map[string]struct{}{"https://1.2.3.4:1": {}}, UniqueURLsMapFromFlag(fs, name))
	uss := UniqueURLsFromFlag(fs, name)
	require.Len(t, uss, 1)
	require.Equal(t, "https://1.2.3.4:1", uss[0].String())
}

func TestUniqueURLsFromFlag(t *testing.T) {
	const name = "test"
	urls := []string{
		"https://1.2.3.4:1",
		"https://1.2.3.4:2",
		"https://1.2.3.4:3",
		"https://1.2.3.4:1",
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	u := NewUniqueURLsWithExceptions(strings.Join(urls, ","))
	fs.Var(u, name, "usage")
	uss := UniqueURLsFromFlag(fs, name)

	require.Len(t, uss, len(u.Values))

	um := make(map[string]struct{})
	for _, x := range uss {
		um[x.String()] = struct{}{}
	}
	require.Equal(t, u.Values, um)
}
