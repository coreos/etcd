package test

import (
	"os"
	"testing"
	"time"

	"github.com/coreos/go-etcd/etcd"
)

// This test creates a single node and then set a value to it.
// Then this test kills the node and restart it and tries to get the value again.
func TestSingleNodeRecovery(t *testing.T) {
	procAttr := new(os.ProcAttr)
	procAttr.Files = []*os.File{nil, os.Stdout, os.Stderr}
	args := []string{"etcd", "-name=node1", "-data-dir=/tmp/node1"}

	process, err := os.StartProcess(EtcdBinPath, append(args, "-force-config"), procAttr)
	if err != nil {
		t.Fatal("start process failed:" + err.Error())
		return
	}

	time.Sleep(time.Second)

	c := etcd.NewClient(nil)

	c.SyncCluster()
	// Test Set
	result, err := c.Set("foo", "bar", 100)

	if err != nil || result.Key != "/foo" || result.Value != "bar" || result.TTL < 95 {
		if err != nil {
			t.Fatal(err)
		}

		t.Fatalf("Set 1 failed with %s %s %v", result.Key, result.Value, result.TTL)
	}

	time.Sleep(time.Second)

	process.Kill()

	process, err = os.StartProcess(EtcdBinPath, args, procAttr)
	defer process.Kill()
	if err != nil {
		t.Fatal("start process failed:" + err.Error())
		return
	}

	time.Sleep(time.Second)

	results, err := c.Get("foo")
	if err != nil {
		t.Fatal("get fail: " + err.Error())
		return
	}

	result = results[0]

	if err != nil || result.Key != "/foo" || result.Value != "bar" || result.TTL > 99 {
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("Recovery Get failed with %s %s %v", result.Key, result.Value, result.TTL)
	}
}
