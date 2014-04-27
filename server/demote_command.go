package server

import (
	"fmt"

	"github.com/coreos/etcd/log"
	"github.com/coreos/etcd/third_party/github.com/goraft/raft"
)

func init() {
	raft.RegisterCommand(&DemoteCommand{})
}

// DemoteCommand represents a command to change a peer to a standby.
type DemoteCommand struct {
	Name string `json:"name"`
}

// CommandName returns the name of the command.
func (c *DemoteCommand) CommandName() string {
	return "etcd:demote"
}

// Apply executes the command.
func (c *DemoteCommand) Apply(context raft.Context) (interface{}, error) {
	ps, _ := context.Server().Context().(*PeerServer)

	// Ignore this command if there is no peer.
	if !ps.registry.PeerExists(c.Name) {
		return nil, fmt.Errorf("peer does not exist: %s", c.Name)
	}

	// Save URLs.
	clientURL, _ := ps.registry.ClientURL(c.Name)
	peerURL, _ := ps.registry.PeerURL(c.Name)

	// Remove node from the shared registry.
	err := ps.registry.UnregisterPeer(c.Name)
	if err != nil {
		log.Debugf("Demote peer %s: Error while unregistering (%v)", c.Name, err)
		return nil, err
	}

	// Delete from stats
	delete(ps.followersStats.Followers, c.Name)

	// Remove peer in raft
	err = context.Server().RemovePeer(c.Name)
	if err != nil {
		log.Debugf("Demote peer %s: (%v)", c.Name, err)
		return nil, err
	}

	// Register node as a standby.
	ps.registry.RegisterStandby(c.Name, peerURL, clientURL)

	// Update mode if this change applies to this server.
	if c.Name == ps.Config.Name {
		log.Infof("Demote peer %s: Set mode to standby with %s", c.Name, ps.server.Leader())
		ps.standbyPeerURL, _ = ps.registry.PeerURL(ps.server.Leader())
		// TODO(yichengq): get rid of this workaround
		// We set the mode explicitly here to ensure the mode has been
		// set when the command is applied. This is important for log
		// replay because it needs updated mode info to decide cluster finding.
		ps.mode = StandbyMode
		go ps.setMode(StandbyMode)
	}

	return nil, nil
}

// NodeName returns the name of the affected node.
func (c *DemoteCommand) NodeName() string {
	return c.Name
}
