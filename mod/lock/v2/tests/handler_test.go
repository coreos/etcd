package lock

import (
	"fmt"
	"testing"
	"time"

	"github.com/coreos/etcd/server"
	"github.com/coreos/etcd/tests"
	"github.com/stretchr/testify/assert"
)

// Ensure that a lock can be acquired and released.
func TestModLockAcquireAndRelease(t *testing.T) {
	tests.RunServer(func(s *server.Server) {
		// Acquire lock.
		body, err := testAcquireLock(s, "foo", 10)
		assert.NoError(t, err)
		assert.Equal(t, body, "2")

		// Check that we have the lock.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "2")

		// Release lock.
		body, err = testReleaseLock(s, "foo", 2)
		assert.NoError(t, err)
		assert.Equal(t, body, "")

		// Check that we have the lock.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "")
	})
}

// Ensure that a lock can be acquired and another process is blocked until released.
func TestModLockBlockUntilAcquire(t *testing.T) {
	tests.RunServer(func(s *server.Server) {
		c := make(chan bool)

		// Acquire lock #1.
		go func() {
			body, err := testAcquireLock(s, "foo", 10)
			assert.NoError(t, err)
			assert.Equal(t, body, "2")
			c <- true
		}()
		<-c

		// Acquire lock #2.
		go func() {
			c <- true
			body, err := testAcquireLock(s, "foo", 10)
			assert.NoError(t, err)
			assert.Equal(t, body, "4")
		}()
		<-c

		time.Sleep(1 * time.Second)

		// Check that we have the lock #1.
		body, err := testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "2")

		// Release lock #1.
		body, err = testReleaseLock(s, "foo", 2)
		assert.NoError(t, err)

		// Check that we have lock #2.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "4")

		// Release lock #2.
		body, err = testReleaseLock(s, "foo", 4)
		assert.NoError(t, err)

		// Check that we have no lock.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "")
	})
}

// Ensure that a lock will be released after the TTL.
func TestModLockExpireAndRelease(t *testing.T) {
	tests.RunServer(func(s *server.Server) {
		c := make(chan bool)

		// Acquire lock #1.
		go func() {
			body, err := testAcquireLock(s, "foo", 2)
			assert.NoError(t, err)
			assert.Equal(t, body, "2")
			c <- true
		}()
		<-c

		// Acquire lock #2.
		go func() {
			c <- true
			body, err := testAcquireLock(s, "foo", 10)
			assert.NoError(t, err)
			assert.Equal(t, body, "4")
		}()
		<-c

		time.Sleep(1 * time.Second)

		// Check that we have the lock #1.
		body, err := testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "2")

		// Wait for lock #1 TTL.
		time.Sleep(2 * time.Second)

		// Check that we have lock #2.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "4")
	})
}

// Ensure that a lock can be renewed.
func TestModLockRenew(t *testing.T) {
	tests.RunServer(func(s *server.Server) {
		// Acquire lock.
		body, err := testAcquireLock(s, "foo", 3)
		assert.NoError(t, err)
		assert.Equal(t, body, "2")

		time.Sleep(2 * time.Second)

		// Check that we have the lock.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "2")

		// Renew lock.
		body, err = testRenewLock(s, "foo", 2, 3)
		assert.NoError(t, err)
		assert.Equal(t, body, "")

		time.Sleep(2 * time.Second)

		// Check that we still have the lock.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "2")

		time.Sleep(2 * time.Second)

		// Check that lock was released.
		body, err = testGetLockIndex(s, "foo")
		assert.NoError(t, err)
		assert.Equal(t, body, "")
	})
}

func testAcquireLock(s *server.Server, key string, ttl int) (string, error) {
	resp, err := tests.PostForm(fmt.Sprintf("%s/mod/v2/lock/%s?ttl=%d", s.URL(), key, ttl), nil)
	ret := tests.ReadBody(resp)
	return string(ret), err
}

func testGetLockIndex(s *server.Server, key string) (string, error) {
	resp, err := tests.Get(fmt.Sprintf("%s/mod/v2/lock/%s", s.URL(), key))
	ret := tests.ReadBody(resp)
	return string(ret), err
}

func testReleaseLock(s *server.Server, key string, index int) (string, error) {
	resp, err := tests.DeleteForm(fmt.Sprintf("%s/mod/v2/lock/%s/%d", s.URL(), key, index), nil)
	ret := tests.ReadBody(resp)
	return string(ret), err
}

func testRenewLock(s *server.Server, key string, index int, ttl int) (string, error) {
	resp, err := tests.PutForm(fmt.Sprintf("%s/mod/v2/lock/%s/%d?ttl=%d", s.URL(), key, index, ttl), nil)
	ret := tests.ReadBody(resp)
	return string(ret), err
}
