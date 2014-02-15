package test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/coreos/etcd/tests"
	"github.com/coreos/etcd/third_party/github.com/coreos/go-etcd/etcd"
	"github.com/coreos/etcd/third_party/github.com/stretchr/testify/assert"
)

// Create a full cluster and then add extra proxy nodes.
func TestProxy(t *testing.T) {
	clusterSize := 10 // MaxClusterSize + 1
	_, etcds, err := CreateCluster(clusterSize, &os.ProcAttr{Files: []*os.File{nil, os.Stdout, os.Stderr}}, false)
	assert.NoError(t, err)
	defer DestroyCluster(etcds)

	if err != nil {
		t.Fatal("cannot create cluster")
	}

	c := etcd.NewClient(nil)
	c.SyncCluster()

	// Set key.
	time.Sleep(time.Second)
	if _, err := c.Set("foo", "bar", 0); err != nil {
		panic(err)
	}
	time.Sleep(time.Second)

	// Check that all peers and proxies have the value.
	for i, _ := range etcds {
		resp, err := tests.Get(fmt.Sprintf("http://localhost:%d/v2/keys/foo", 4000 + (i+1)))
		if assert.NoError(t, err) {
			body := tests.ReadBodyJSON(resp)
			if node, _ := body["node"].(map[string]interface{}); assert.NotNil(t, node) {
				assert.Equal(t, node["value"], "bar")
			}
		}
	}
}
