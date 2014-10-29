/*
   Copyright 2014 CoreOS, Inc.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/coreos/etcd/etcdserver/etcdhttp/httptypes"
)

var (
	DefaultV2MembersPrefix = "/v2/admin/members"
)

func NewMembersAPI(tr *http.Transport, ep string, to time.Duration) (MembersAPI, error) {
	u, err := url.Parse(ep)
	if err != nil {
		return nil, err
	}

	u.Path = path.Join(u.Path, DefaultV2MembersPrefix)

	c := &httpClient{
		transport: tr,
		endpoint:  *u,
		timeout:   to,
	}

	mAPI := httpMembersAPI{
		client: c,
	}

	return &mAPI, nil
}

type MembersAPI interface {
	List() ([]httptypes.Member, error)
}

type httpMembersAPI struct {
	client *httpClient
}

func (m *httpMembersAPI) List() ([]httptypes.Member, error) {
	code, body, err := m.client.doWithTimeout(&membersAPIActionList{})
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("unrecognized status code %d", code)
	}

	var mCollection httptypes.MemberCollection
	if err := json.Unmarshal(body, &mCollection); err != nil {
		return nil, err
	}

	return []httptypes.Member(mCollection), nil
}

type membersAPIActionList struct{}

func (l *membersAPIActionList) httpRequest(ep url.URL) *http.Request {
	req, _ := http.NewRequest("GET", ep.String(), nil)
	return req
}
