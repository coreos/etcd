// Copyright 2026 The etcd Authors
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

package traffic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKubernetesTrafficProgressNotify(t *testing.T) {
	tcs := []struct {
		name    string
		traffic Traffic
		want    bool
	}{
		{name: "Kubernetes", traffic: Kubernetes, want: true},
		{name: "KubernetesCreateDelete", traffic: KubernetesCreateDelete, want: true},
		{name: "KubernetesEvents", traffic: KubernetesEvents, want: false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			kt, ok := tc.traffic.(kubernetesTraffic)
			require.Truef(t, ok, "%s is not a kubernetesTraffic", tc.name)
			assert.Equal(t, tc.want, kt.progressNotify())
			assert.Equal(t, tc.want, kt.watchLoopConfig().progressNotify)
		})
	}
}
