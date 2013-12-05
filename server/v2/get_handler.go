package v2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	etcdErr "github.com/coreos/etcd/error"
	"github.com/coreos/etcd/log"
	"github.com/coreos/etcd/store"
	"github.com/coreos/raft"
	"github.com/gorilla/mux"
)

func GetHandler(w http.ResponseWriter, req *http.Request, s Server) error {
	var err error
	var event *store.Event

	vars := mux.Vars(req)
	key := "/" + vars["key"]

	consistent := (req.FormValue("consistent") == "true")
	recursive := (req.FormValue("recursive") == "true")
	sorted := (req.FormValue("sorted") == "true")
	wait := (req.FormValue("wait") == "true")
	waitIndex := (req.FormValue("waitIndex"))

	// default index is 0
	var sinceIndex uint64 = 0

	if waitIndex != "" {
		sinceIndex, err = strconv.ParseUint(string(waitIndex), 10, 64)
		if err != nil {
			return etcdErr.NewError(etcdErr.EcodeIndexNaN, "Watch From Index", s.Store().Index())
		}
	}

	if s.Mode() == "proxy" {
		if wait {
			return nil
		}

		return s.Proxy().Get(key, sorted, recursive, w)
	}

	// Help client to redirect the request to the current leader
	if consistent && s.State() != raft.Leader {
		leader := s.Leader()
		hostname, _ := s.ClientURL(leader)
		url := hostname + req.URL.Path
		log.Debugf("Redirect consistent get to %s", url)
		http.Redirect(w, req, url, http.StatusTemporaryRedirect)
		return nil
	}

	if wait { // watch
		// Start the watcher on the store.
		eventChan, err := s.Store().Watch(key, recursive, sinceIndex)
		if err != nil {
			return etcdErr.NewError(500, key, s.Store().Index())
		}

		cn, _ := w.(http.CloseNotifier)
		closeChan := cn.CloseNotify()

		select {
		case <-closeChan:
			return nil
		case event = <-eventChan:
		}

	} else { //get
		// Retrieve the key from the store.
		event, err = s.Store().Get(key, recursive, sorted)
		if err != nil {
			return err
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("X-Etcd-Index", fmt.Sprint(s.Store().Index()))
	w.Header().Add("X-Raft-Index", fmt.Sprint(s.CommitIndex()))
	w.Header().Add("X-Raft-Term", fmt.Sprint(s.Term()))
	w.WriteHeader(http.StatusOK)
	b, _ := json.Marshal(event)

	w.Write(b)

	return nil
}
