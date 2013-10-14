package store

import (
	"github.com/coreos/etcd/log"
	"github.com/coreos/go-raft"
)

func init() {
	raft.RegisterCommand(&DeleteCommand{})
}

// The DeleteCommand removes a key from the Store.
type DeleteCommand struct {
	Key       string `json:"key"`
	Recursive bool   `json:"recursive"`
}

// The name of the delete command in the log
func (c *DeleteCommand) CommandName() string {
	return "etcd:delete"
}

// Delete the key
func (c *DeleteCommand) Apply(server *raft.Server) (interface{}, error) {
	s, _ := server.StateMachine().(Store)

	e, err := s.Delete(c.Key, c.Recursive, server.CommitIndex(), server.Term())

	if err != nil {
		log.Debug(err)
		return nil, err
	}

	return e, nil
}
