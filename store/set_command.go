package store

import (
	"github.com/coreos/etcd/log"
	"github.com/coreos/go-raft"
	"time"
)

func init() {
	raft.RegisterCommand(&SetCommand{})
}

// Create command
type SetCommand struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	ExpireTime time.Time `json:"expireTime"`
}

// The name of the create command in the log
func (c *SetCommand) CommandName() string {
	return "etcd:set"
}

// Create node
func (c *SetCommand) Apply(server raft.Server) (interface{}, error) {
	s, _ := server.StateMachine().(Store)

	// create a new node or replace the old node.
	e, err := s.Set(c.Key, c.Value, c.ExpireTime, server.CommitIndex(), server.Term())

	if err != nil {
		log.Debug(err)
		return nil, err
	}

	return e, nil
}
