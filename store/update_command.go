package store

import (
	"time"

	"github.com/coreos/etcd/log"
	"github.com/coreos/go-raft"
)

func init() {
	raft.RegisterCommand(&UpdateCommand{})
}

// The UpdateCommand updates the value of a key in the Store.
type UpdateCommand struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	ExpireTime time.Time `json:"expireTime"`
}

// The name of the update command in the log
func (c *UpdateCommand) CommandName() string {
	return "etcd:update"
}

// Update node
func (c *UpdateCommand) Apply(server raft.Server) (interface{}, error) {
	s, _ := server.StateMachine().(Store)

	e, err := s.Update(c.Key, c.Value, c.ExpireTime, server.CommitIndex(), server.Term())

	if err != nil {
		log.Debug(err)
		return nil, err
	}

	return e, nil
}
