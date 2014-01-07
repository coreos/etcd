package v2

import (
	"time"

	"github.com/coreos/etcd/log"
	"github.com/coreos/etcd/store"
	"github.com/coreos/raft"
)

func init() {
	raft.RegisterCommand(&CreateCommand{})
}

// Create command
type CreateCommand struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	ExpireTime time.Time `json:"expireTime"`
	Unique     bool      `json:"unique"`
	Dir        bool      `json:"dir"`
}

// The name of the create command in the log
func (c *CreateCommand) CommandName() string {
	return "etcd:create"
}

// Create node
func (c *CreateCommand) Apply(cxt raft.Context) (interface{}, error) {
	server := cxt.Server()

	s, _ := server.StateMachine().(store.Store)

	e, err := s.Create(c.Key, c.Dir, c.Value, c.Unique, c.ExpireTime)

	if err != nil {
		log.Debug(err)
		return nil, err
	}

	return e, nil
}
