// Copyright 2016 The etcd Authors
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

package naming_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	etcd "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/naming/endpoints"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestEndpointManager(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	em, err := endpoints.NewManager(clus.RandClient(), "foo")
	if err != nil {
		t.Fatal("failed to create EndpointManager", err)
	}
	ctx, watchCancel := context.WithCancel(t.Context())
	defer watchCancel()
	w, err := em.NewWatchChannel(ctx)
	if err != nil {
		t.Fatal("failed to establish watch", err)
	}

	e1 := endpoints.Endpoint{Addr: "127.0.0.1", Metadata: "metadata"}
	err = em.AddEndpoint(t.Context(), "foo/a1", e1)
	if err != nil {
		t.Fatal("failed to add foo", err)
	}

	us := <-w

	if us == nil {
		t.Fatal("failed to get update")
	}

	wu := &endpoints.Update{
		Op:       endpoints.Add,
		Key:      "foo/a1",
		Endpoint: e1,
	}

	require.Truef(t, reflect.DeepEqual(us[0], wu), "up = %#v, want %#v", us[0], wu)

	err = em.DeleteEndpoint(t.Context(), "foo/a1")
	require.NoErrorf(t, err, "failed to udpate %v", err)

	us = <-w
	if us == nil {
		t.Fatal("failed to get udpate")
	}

	wu = &endpoints.Update{
		Op:  endpoints.Delete,
		Key: "foo/a1",
	}

	require.Truef(t, reflect.DeepEqual(us[0], wu), "up = %#v, want %#v", us[0], wu)
}

// TestEndpointManagerAtomicity ensures the resolver will initialize
// correctly with multiple hosts and correctly receive multiple
// updates in a single revision.
func TestEndpointManagerAtomicity(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	c := clus.RandClient()
	em, err := endpoints.NewManager(c, "foo")
	if err != nil {
		t.Fatal("failed to create EndpointManager", err)
	}

	err = em.Update(t.Context(), []*endpoints.UpdateWithOpts{
		endpoints.NewAddUpdateOpts("foo/host", endpoints.Endpoint{Addr: "127.0.0.1:2000"}),
		endpoints.NewAddUpdateOpts("foo/host2", endpoints.Endpoint{Addr: "127.0.0.1:2001"}),
	})
	require.NoError(t, err)

	ctx, watchCancel := context.WithCancel(t.Context())
	defer watchCancel()
	w, err := em.NewWatchChannel(ctx)
	require.NoError(t, err)

	updates := <-w
	require.Lenf(t, updates, 2, "expected two updates, got %+v", updates)

	_, err = c.Txn(t.Context()).Then(etcd.OpDelete("foo/host"), etcd.OpDelete("foo/host2")).Commit()
	require.NoError(t, err)

	updates = <-w
	if len(updates) != 2 || (updates[0].Op != endpoints.Delete && updates[1].Op != endpoints.Delete) {
		t.Fatalf("expected two delete updates, got %+v", updates)
	}
}

func TestEndpointManagerDeleteEndpointRejectsRangeOptions(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	c := clus.RandClient()
	em, err := endpoints.NewManager(c, "foo")
	require.NoError(t, err)
	emOther, err := endpoints.NewManager(c, "foo_other")
	require.NoError(t, err)

	require.NoError(t, em.AddEndpoint(t.Context(), "foo/a", endpoints.Endpoint{Addr: "127.0.0.1:2000"}))
	require.NoError(t, em.AddEndpoint(t.Context(), "foo/b", endpoints.Endpoint{Addr: "127.0.0.1:2001"}))
	require.NoError(t, emOther.AddEndpoint(t.Context(), "foo_other/a", endpoints.Endpoint{Addr: "127.0.0.1:2002"}))

	err = em.DeleteEndpoint(t.Context(), "foo/a", etcd.WithFromKey())
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	for _, key := range []string{"foo/a", "foo/b", "foo_other/a"} {
		resp, err := c.Get(t.Context(), key)
		require.NoError(t, err)
		require.Len(t, resp.Kvs, 1, "endpoint %q should remain after rejected range delete", key)
	}
}

func TestEndpointManagerUpdateRejectsRangeDeleteAtomically(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	c := clus.RandClient()
	em, err := endpoints.NewManager(c, "foo")
	require.NoError(t, err)
	require.NoError(t, em.AddEndpoint(t.Context(), "foo/existing", endpoints.Endpoint{Addr: "127.0.0.1:2000"}))

	err = em.Update(t.Context(), []*endpoints.UpdateWithOpts{
		endpoints.NewAddUpdateOpts("foo/new", endpoints.Endpoint{Addr: "127.0.0.1:2001"}),
		endpoints.NewDeleteUpdateOpts("foo/existing", etcd.WithFromKey()),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	resp, err := c.Get(t.Context(), "foo/existing")
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1, "existing endpoint should remain after rejected update")

	resp, err = c.Get(t.Context(), "foo/new")
	require.NoError(t, err)
	require.Empty(t, resp.Kvs, "new endpoint should not be added by rejected update")
}

func TestEndpointManagerCRUD(t *testing.T) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	em, err := endpoints.NewManager(clus.RandClient(), "foo")
	if err != nil {
		t.Fatal("failed to create EndpointManager", err)
	}

	// Add
	k1 := "foo/a1"
	e1 := endpoints.Endpoint{Addr: "127.0.0.1", Metadata: "metadata1"}
	err = em.AddEndpoint(t.Context(), k1, e1)
	if err != nil {
		t.Fatal("failed to add", k1, err)
	}

	k2 := "foo/a2"
	e2 := endpoints.Endpoint{Addr: "127.0.0.2", Metadata: "metadata2"}
	err = em.AddEndpoint(t.Context(), k2, e2)
	if err != nil {
		t.Fatal("failed to add", k2, err)
	}

	eps, err := em.List(t.Context())
	if err != nil {
		t.Fatal("failed to list foo")
	}
	require.Lenf(t, eps, 2, "unexpected the number of endpoints: %d", len(eps))
	require.Truef(t, reflect.DeepEqual(eps[k1], e1), "unexpected endpoints: %s", k1)
	require.Truef(t, reflect.DeepEqual(eps[k2], e2), "unexpected endpoints: %s", k2)

	// Delete
	err = em.DeleteEndpoint(t.Context(), k1)
	if err != nil {
		t.Fatal("failed to delete", k2, err)
	}

	eps, err = em.List(t.Context())
	if err != nil {
		t.Fatal("failed to list foo")
	}
	require.Lenf(t, eps, 1, "unexpected the number of endpoints: %d", len(eps))
	require.Truef(t, reflect.DeepEqual(eps[k2], e2), "unexpected endpoints: %s", k2)

	// Update
	k3 := "foo/a3"
	e3 := endpoints.Endpoint{Addr: "127.0.0.3", Metadata: "metadata3"}
	updates := []*endpoints.UpdateWithOpts{
		{Update: endpoints.Update{Op: endpoints.Add, Key: k3, Endpoint: e3}},
		{Update: endpoints.Update{Op: endpoints.Delete, Key: k2}},
	}
	err = em.Update(t.Context(), updates)
	if err != nil {
		t.Fatal("failed to update", err)
	}

	eps, err = em.List(t.Context())
	if err != nil {
		t.Fatal("failed to list foo")
	}
	require.Lenf(t, eps, 1, "unexpected the number of endpoints: %d", len(eps))
	require.Truef(t, reflect.DeepEqual(eps[k3], e3), "unexpected endpoints: %s", k3)
}
