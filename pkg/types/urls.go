// Copyright 2015 CoreOS, Inc.
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

package types

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

type URLs []url.URL

func NewURLs(strs []string) (URLs, error) {
	if len(strs) == 0 {
		return nil, errors.New("no valid URLs given")
	}

	all := make([]url.URL, len(strs))
	for i, in := range strs {
		in = strings.TrimSpace(in)
		u, err := url.Parse(in)
		if err != nil {
			return nil, err
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("URL scheme must be http or https: %s", in)
		}
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			return nil, fmt.Errorf(`URL address does not have the form "host:port": %s`, in)
		}
		if u.Path != "" {
			return nil, fmt.Errorf("URL must not contain a path: %s", in)
		}
		all[i] = *u
	}
	us := URLs(all)
	us.Sort()

	return us, nil
}

func (us URLs) String() string {
	return strings.Join(us.StringSlice(), ",")
}

func (us *URLs) Sort() {
	sort.Sort(us)
}
func (us URLs) Len() int           { return len(us) }
func (us URLs) Less(i, j int) bool { return us[i].String() < us[j].String() }
func (us URLs) Swap(i, j int)      { us[i], us[j] = us[j], us[i] }

func (us URLs) StringSlice() []string {
	out := make([]string, len(us))
	for i := range us {
		out[i] = us[i].String()
	}

	return out
}
