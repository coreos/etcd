package v2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	etcdErr "github.com/coreos/etcd/error"
	"github.com/coreos/etcd/log"
	"github.com/coreos/etcd/store"
	"github.com/coreos/go-raft"
	"github.com/gorilla/mux"
)

func GetHandler(w http.ResponseWriter, req *http.Request, s Server) error {
	var err error
	var event *store.Event

	vars := mux.Vars(req)
	key := "/" + vars["key"]

	// Help client to redirect the request to the current leader
	if req.FormValue("consistent") == "true" && s.State() != raft.Leader {
		leader := s.Leader()
		hostname, _ := s.PeerURL(leader)
		url := hostname + req.URL.Path
		log.Debugf("Redirect consistent get to %s", url)
		http.Redirect(w, req, url, http.StatusTemporaryRedirect)
		return nil
	}

	recursive := (req.FormValue("recursive") == "true")
	sorted := (req.FormValue("sorted") == "true")

	if req.FormValue("wait") == "true" { // watch
		// Create a command to watch from a given index (default 0).
		var sinceIndex uint64 = 0

		waitIndex := req.FormValue("waitIndex")
		if waitIndex != "" {
			sinceIndex, err = strconv.ParseUint(string(req.FormValue("waitIndex")), 10, 64)
			if err != nil {
				return etcdErr.NewError(etcdErr.EcodeIndexNaN, "Watch From Index", store.UndefIndex, store.UndefTerm)
			}
		}

		// Start the watcher on the store.
		c, err := s.Store().Watch(key, recursive, sinceIndex, s.CommitIndex(), s.Term())
		if err != nil {
			return etcdErr.NewError(500, key, store.UndefIndex, store.UndefTerm)
		}
		event = <-c

	} else { //get
		// Retrieve the key from the store.
		event, err = s.Store().Get(key, recursive, sorted, s.CommitIndex(), s.Term())
		if err != nil {
			return err
		}
	}

	w.Header().Add("X-Etcd-Index", fmt.Sprint(event.Index))
	w.Header().Add("X-Etcd-Term", fmt.Sprint(event.Term))
	w.WriteHeader(http.StatusOK)

	b, _ := json.Marshal(event)
	w.Write(b)

	return nil
}
