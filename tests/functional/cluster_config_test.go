package test

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/coreos/etcd/tests"
	"github.com/coreos/etcd/third_party/github.com/stretchr/testify/assert"
)

// Ensure that the cluster configuration can be updated.
func TestClusterConfig(t *testing.T) {
	_, etcds, err := CreateCluster(3, &os.ProcAttr{Files: []*os.File{nil, os.Stdout, os.Stderr}}, false)
	assert.NoError(t, err)
	defer DestroyCluster(etcds)

	resp, _ := tests.Put("http://localhost:7001/v2/admin/config/cluster", "application/json", bytes.NewBufferString(`{"activeSize":3, "promoteDelay":60}`))
	assert.Equal(t, resp.StatusCode, 200)

	time.Sleep(1 * time.Second)

	resp, _ = tests.Get("http://localhost:7002/v2/admin/config/cluster")
	body := tests.ReadBodyJSON(resp)
	assert.Equal(t, resp.StatusCode, 200)
	assert.Equal(t, body["activeSize"], 3)
	assert.Equal(t, body["promoteDelay"], 60)
}
