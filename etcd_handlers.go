package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	etcdErr "github.com/coreos/etcd/error"
	"github.com/coreos/etcd/store"
	"github.com/coreos/go-raft"
)

//-------------------------------------------------------------------
// Handlers to handle etcd-store related request via etcd url
//-------------------------------------------------------------------

func NewEtcdMuxer() *http.ServeMux {
	// external commands
	etcdMux := http.NewServeMux()
	etcdMux.Handle("/"+version+"/keys/", errorHandler(Multiplexer))
	etcdMux.Handle("/"+version+"/leader", errorHandler(LeaderHttpHandler))
	etcdMux.Handle("/"+version+"/machines", errorHandler(MachinesHttpHandler))
	etcdMux.Handle("/"+version+"/stats/", errorHandler(StatsHttpHandler))
	etcdMux.Handle("/version", errorHandler(VersionHttpHandler))

	etcdMux.Handle("/v1/keys/", errorHandler(MultiplexerV1))
	etcdMux.Handle("/v1/leader", errorHandler(LeaderHttpHandler))
	etcdMux.Handle("/v1/machines", errorHandler(MachinesHttpHandler))
	etcdMux.Handle("/v1/stats/", errorHandler(StatsHttpHandler))
	etcdMux.HandleFunc("/test/", TestHttpHandler)
	return etcdMux
}

type errorHandler func(http.ResponseWriter, *http.Request) error

// addCorsHeader parses the request Origin header and loops through the user
// provided allowed origins and sets the Access-Control-Allow-Origin header if
// there is a match.
func addCorsHeader(w http.ResponseWriter, r *http.Request) {
	val, ok := corsList["*"]
	if val && ok {
		w.Header().Add("Access-Control-Allow-Origin", "*")
		return
	}

	requestOrigin := r.Header.Get("Origin")
	val, ok = corsList[requestOrigin]
	if val && ok {
		w.Header().Add("Access-Control-Allow-Origin", requestOrigin)
		return
	}
}

func (fn errorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	addCorsHeader(w, r)
	if e := fn(w, r); e != nil {
		if etcdErr, ok := e.(*etcdErr.Error); ok {
			debug("Return error: ", (*etcdErr).Error())
			etcdErr.Write(w)
		} else {
			http.Error(w, e.Error(), http.StatusInternalServerError)
		}
	}
}

// Multiplex GET/POST/DELETE request to corresponding handlers
func Multiplexer(w http.ResponseWriter, req *http.Request) error {

	switch req.Method {
	case "GET":
		return GetHttpHandler(w, req)
	case "POST":
		return CreateHttpHandler(w, req)
	case "PUT":
		return UpdateHttpHandler(w, req)
	case "DELETE":
		return DeleteHttpHandler(w, req)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}

	return nil
}

//--------------------------------------
// State sensitive handlers
// Set/Delete will dispatch to leader
//--------------------------------------

func CreateHttpHandler(w http.ResponseWriter, req *http.Request) error {
	key := getNodePath(req.URL.Path)

	debugf("recv.post[%v] [%v%v]\n", req.RemoteAddr, req.Host, req.URL)

	value := req.FormValue("value")

	expireTime, err := durationToExpireTime(req.FormValue("ttl"))

	if err != nil {
		return etcdErr.NewError(etcdErr.EcodeTTLNaN, "Create", store.UndefIndex, store.UndefTerm)
	}

	command := &CreateCommand{
		Key:        key,
		Value:      value,
		ExpireTime: expireTime,
	}

	if req.FormValue("incremental") == "true" {
		command.IncrementalSuffix = true
	}

	return dispatchEtcdCommand(command, w, req)

}

func UpdateHttpHandler(w http.ResponseWriter, req *http.Request) error {
	key := getNodePath(req.URL.Path)

	debugf("recv.put[%v] [%v%v]\n", req.RemoteAddr, req.Host, req.URL)

	req.ParseForm()

	value := req.Form.Get("value")

	expireTime, err := durationToExpireTime(req.Form.Get("ttl"))

	if err != nil {
		return etcdErr.NewError(etcdErr.EcodeTTLNaN, "Update", store.UndefIndex, store.UndefTerm)
	}

	// update should give at least one option
	if value == "" && expireTime.Sub(store.Permanent) == 0 {
		return etcdErr.NewError(etcdErr.EcodeValueOrTTLRequired, "Update", store.UndefIndex, store.UndefTerm)
	}

	prevValue, valueOk := req.Form["prevValue"]

	prevIndexStr, indexOk := req.Form["prevIndex"]

	if !valueOk && !indexOk { // update without test
		command := &UpdateCommand{
			Key:        key,
			Value:      value,
			ExpireTime: expireTime,
		}

		return dispatchEtcdCommand(command, w, req)

	} else { // update with test
		var prevIndex uint64

		if indexOk {
			prevIndex, err = strconv.ParseUint(prevIndexStr[0], 10, 64)

			// bad previous index
			if err != nil {
				return etcdErr.NewError(etcdErr.EcodeIndexNaN, "Update", store.UndefIndex, store.UndefTerm)
			}
		} else {
			prevIndex = 0
		}

		command := &TestAndSetCommand{
			Key:       key,
			Value:     value,
			PrevValue: prevValue[0],
			PrevIndex: prevIndex,
		}

		return dispatchEtcdCommand(command, w, req)
	}

}

// Delete Handler
func DeleteHttpHandler(w http.ResponseWriter, req *http.Request) error {
	key := getNodePath(req.URL.Path)

	debugf("recv.delete[%v] [%v%v]\n", req.RemoteAddr, req.Host, req.URL)

	command := &DeleteCommand{
		Key: key,
	}

	if req.FormValue("recursive") == "true" {
		command.Recursive = true
	}

	return dispatchEtcdCommand(command, w, req)
}

// Dispatch the command to leader
func dispatchEtcdCommand(c Command, w http.ResponseWriter, req *http.Request) error {
	return dispatch(c, w, req, nameToEtcdURL)
}

//--------------------------------------
// State non-sensitive handlers
// command with consistent option will
// still dispatch to the leader
//--------------------------------------

// Handler to return the current leader's raft address
func LeaderHttpHandler(w http.ResponseWriter, req *http.Request) error {
	leader := r.Leader()

	if leader != "" {
		w.WriteHeader(http.StatusOK)
		raftURL, _ := nameToRaftURL(leader)
		w.Write([]byte(raftURL))

		return nil
	} else {
		return etcdErr.NewError(etcdErr.EcodeLeaderElect, "", store.UndefIndex, store.UndefTerm)
	}
}

// Handler to return all the known machines in the current cluster
func MachinesHttpHandler(w http.ResponseWriter, req *http.Request) error {
	machines := getMachines(nameToEtcdURL)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(strings.Join(machines, ", ")))

	return nil
}

// Handler to return the current version of etcd
func VersionHttpHandler(w http.ResponseWriter, req *http.Request) error {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "etcd %s", releaseVersion)

	return nil
}

// Handler to return the basic stats of etcd
func StatsHttpHandler(w http.ResponseWriter, req *http.Request) error {
	option := req.URL.Path[len("/v1/stats/"):]
	w.WriteHeader(http.StatusOK)

	switch option {
	case "self":
		w.Write(r.Stats())
	case "leader":
		if r.State() == raft.Leader {
			w.Write(r.PeerStats())
		} else {
			leader := r.Leader()
			// current no leader
			if leader == "" {
				return etcdErr.NewError(300, "", store.UndefIndex, store.UndefTerm)
			}
			hostname, _ := nameToEtcdURL(leader)
			redirect(hostname, w, req)
		}
	case "store":
		w.Write(etcdStore.JsonStats())
	}

	return nil
}

func GetHttpHandler(w http.ResponseWriter, req *http.Request) error {
	var err error
	var event interface{}
	debugf("recv.get[%v] [%v%v]\n", req.RemoteAddr, req.Host, req.URL)

	if req.FormValue("consistent") == "true" && r.State() != raft.Leader {
		// help client to redirect the request to the current leader
		leader := r.Leader()
		hostname, _ := nameToEtcdURL(leader)
		redirect(hostname, w, req)
		return nil
	}

	key := getNodePath(req.URL.Path)

	recursive := req.FormValue("recursive")

	if req.FormValue("wait") == "true" { // watch
		command := &WatchCommand{
			Key: key,
		}

		if recursive == "true" {
			command.Recursive = true
		}

		indexStr := req.FormValue("wait_index")
		if indexStr != "" {
			sinceIndex, err := strconv.ParseUint(indexStr, 10, 64)

			if err != nil {
				return etcdErr.NewError(etcdErr.EcodeIndexNaN, "Watch From Index", store.UndefIndex, store.UndefTerm)
			}

			command.SinceIndex = sinceIndex
		}

		event, err = command.Apply(r.Server)

	} else { //get

		command := &GetCommand{
			Key: key,
		}

		sorted := req.FormValue("sorted")
		if sorted == "true" {
			command.Sorted = true
		}

		if recursive == "true" {
			command.Recursive = true
		}

		event, err = command.Apply(r.Server)
	}

	if err != nil {
		return err

	} else {
		event, _ := event.(*store.Event)
		bytes, _ := json.Marshal(event)

		w.Header().Add("X-Etcd-Index", fmt.Sprint(event.Index))
		w.Header().Add("X-Etcd-Term", fmt.Sprint(event.Term))
		w.WriteHeader(http.StatusOK)

		w.Write(bytes)

		return nil
	}

}

// TestHandler
func TestHttpHandler(w http.ResponseWriter, req *http.Request) {
	testType := req.URL.Path[len("/test/"):]

	if testType == "speed" {
		directSet()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("speed test success"))

		return
	}

	w.WriteHeader(http.StatusBadRequest)
}
