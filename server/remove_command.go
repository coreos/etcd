package server

import (
	"encoding/binary"
	"encoding/json"

	"github.com/coreos/etcd/log"
	"github.com/coreos/etcd/third_party/github.com/goraft/raft"
)

func init() {
	raft.RegisterCommand(&RemoveCommandV1{})
	raft.RegisterCommand(&RemoveCommandV2{})
}

// The RemoveCommandV1 removes a server from the cluster.
type RemoveCommandV1 struct {
	Name string `json:"name"`
}

// The name of the remove command in the log
func (c *RemoveCommandV1) CommandName() string {
	return "etcd:remove"
}

// Remove a server from the cluster
func (c *RemoveCommandV1) Apply(context raft.Context) (interface{}, error) {
	ps, _ := context.Server().Context().(*PeerServer)

	// If this is a proxy then remove it and exit.
	if ps.registry.ProxyExists(c.Name) {
		return []byte{0}, ps.registry.UnregisterProxy(c.Name)
	}

	// Remove node from the shared registry.
	err := ps.registry.UnregisterPeer(c.Name)

	// Delete from stats
	delete(ps.followersStats.Followers, c.Name)

	if err != nil {
		log.Debugf("Error while unregistering: %s (%v)", c.Name, err)
		return []byte{0}, err
	}

	// Remove peer in raft
	err = context.Server().RemovePeer(c.Name)
	if err != nil {
		log.Debugf("Unable to remove peer: %s (%v)", c.Name, err)
		return []byte{0}, err
	}

	if c.Name == context.Server().Name() {
		// the removed node is this node

		// if the node is not replaying the previous logs
		// and the node has sent out a join request in this
		// start. It is sure that this node received a new remove
		// command and need to be removed
		if context.CommitIndex() > ps.joinIndex && ps.joinIndex != 0 {
			log.Debugf("server [%s] is removed", context.Server().Name())
			if ps.removedChan != nil {
				close(ps.removedChan)
			}
		} else {
			// else ignore remove
			log.Debugf("ignore previous remove command.")
		}
	}

	b := make([]byte, 8)
	binary.PutUvarint(b, context.CommitIndex())

	return b, err
}

// RemoveCommandV2 represents a command to remove a machine from the server.
type RemoveCommandV2 struct {
	Name string `json:"name"`
}

// CommandName returns the name of the command.
func (c *RemoveCommandV2) CommandName() string {
	return "etcd:v2:remove"
}

// Apply removes the given machine from the cluster.
func (c *RemoveCommandV2) Apply(context raft.Context) (interface{}, error) {
	ps, _ := context.Server().Context().(*PeerServer)
	ret, _ := json.Marshal(removeMessageV2{CommitIndex: context.CommitIndex()})

	// If this is a proxy then remove it and exit.
	if ps.registry.ProxyExists(c.Name) {
		if err := ps.registry.UnregisterProxy(c.Name); err != nil {
			return nil, err
		}
		return ret, nil
	}

	// Remove node from the shared registry.
	err := ps.registry.UnregisterPeer(c.Name)

	// Delete from stats
	delete(ps.followersStats.Followers, c.Name)

	if err != nil {
		log.Debugf("Error while unregistering: %s (%v)", c.Name, err)
		return nil, err
	}

	// Remove peer in raft
	if err := context.Server().RemovePeer(c.Name); err != nil {
		log.Debugf("Unable to remove peer: %s (%v)", c.Name, err)
		return nil, err
	}

	if c.Name == context.Server().Name() {
		// the removed node is this node

		// if the node is not replaying the previous logs
		// and the node has sent out a join request in this
		// start. It is sure that this node received a new remove
		// command and need to be removed
		if context.CommitIndex() > ps.joinIndex && ps.joinIndex != 0 {
			log.Debugf("server [%s] is removed", context.Server().Name())
			if ps.removedChan != nil {
				close(ps.removedChan)
			}
		} else {
			// else ignore remove
			log.Debugf("ignore previous remove command.")
		}
	}
	return ret, nil
}

type removeMessageV2 struct {
	CommitIndex uint64 `json:"commitIndex"`
}
