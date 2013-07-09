package main

import (
	"encoding/json"
	"github.com/coreos/go-raft"
	"net/http"
	"strconv"
	"time"
)

//--------------------------------------
// Internal HTTP Handlers via server port
//--------------------------------------

// Get all the current logs
func GetLogHttpHandler(w http.ResponseWriter, req *http.Request) {
	debug("[recv] GET http://%v/log", raftServer.Name())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(raftServer.LogEntries())
}

func VoteHttpHandler(w http.ResponseWriter, req *http.Request) {
	rvreq := &raft.RequestVoteRequest{}
	err := decodeJsonRequest(req, rvreq)
	if err == nil {
		debug("[recv] POST http://%v/vote [%s]", raftServer.Name(), rvreq.CandidateName)
		if resp := raftServer.RequestVote(rvreq); resp != nil {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
			return
		}
	}
	warn("[vote] ERROR: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
}

func AppendEntriesHttpHandler(w http.ResponseWriter, req *http.Request) {
	aereq := &raft.AppendEntriesRequest{}
	err := decodeJsonRequest(req, aereq)

	if err == nil {
		debug("[recv] POST http://%s/log/append [%d]", raftServer.Name(), len(aereq.Entries))
		if resp := raftServer.AppendEntries(aereq); resp != nil {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
			if !resp.Success {
				debug("[Append Entry] Step back")
			}
			return
		}
	}
	warn("[Append Entry] ERROR: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
}

func SnapshotHttpHandler(w http.ResponseWriter, req *http.Request) {
	aereq := &raft.SnapshotRequest{}
	err := decodeJsonRequest(req, aereq)
	if err == nil {
		debug("[recv] POST http://%s/snapshot/ ", raftServer.Name())
		if resp, _ := raftServer.SnapshotRecovery(aereq); resp != nil {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
			return
		}
	}
	warn("[Snapshot] ERROR: %v", err)
	w.WriteHeader(http.StatusInternalServerError)
}

// Get the port that listening for client connecting of the server
func clientHttpHandler(w http.ResponseWriter, req *http.Request) {
	debug("[recv] Get http://%v/client/ ", raftServer.Name())
	w.WriteHeader(http.StatusOK)
	client := address + ":" + strconv.Itoa(clientPort)
	w.Write([]byte(client))
}

//
func JoinHttpHandler(w http.ResponseWriter, req *http.Request) {

	command := &JoinCommand{}

	if err := decodeJsonRequest(req, command); err == nil {
		debug("Receive Join Request from %s", command.Name)
		excute(command, &w, req)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

//--------------------------------------
// external HTTP Handlers via client port
//--------------------------------------

// Dispatch GET/POST/DELETE request to corresponding handlers
func Multiplexer(w http.ResponseWriter, req *http.Request) {

	if req.Method == "GET" {
		GetHttpHandler(&w, req)
	} else if req.Method == "POST" {
		SetHttpHandler(&w, req)
	} else if req.Method == "DELETE" {
		DeleteHttpHandler(&w, req)
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
}

// Set Command Handler
func SetHttpHandler(w *http.ResponseWriter, req *http.Request) {
	key := req.URL.Path[len("/v1/keys/"):]

	debug("[recv] POST http://%v/v1/keys/%s", raftServer.Name(), key)

	command := &SetCommand{}
	command.Key = key

	command.Value = req.FormValue("value")
	strDuration := req.FormValue("ttl")

	if strDuration != "" {
		duration, err := strconv.Atoi(strDuration)

		if err != nil {
			warn("Bad duration: %v", err)
			(*w).WriteHeader(http.StatusInternalServerError)
			return
		}
		command.ExpireTime = time.Now().Add(time.Second * (time.Duration)(duration))
	} else {
		command.ExpireTime = time.Unix(0, 0)
	}

	excute(command, w, req)

}

func TestAndSetHttpHandler(w http.ResponseWriter, req *http.Request) {
	key := req.URL.Path[len("/v1/testAndSet/"):]

	debug("[recv] POST http://%v/v1/testAndSet/%s", raftServer.Name(), key)

	command := &TestAndSetCommand{}
	command.Key = key

	command.PrevValue = req.FormValue("prevValue")
	command.Value = req.FormValue("value")
	strDuration := req.FormValue("ttl")

	if strDuration != "" {
		duration, err := strconv.Atoi(strDuration)

		if err != nil {
			warn("Bad duration: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		command.ExpireTime = time.Now().Add(time.Second * (time.Duration)(duration))
	} else {
		command.ExpireTime = time.Unix(0, 0)
	}

	excute(command, &w, req)

}

func DeleteHttpHandler(w *http.ResponseWriter, req *http.Request) {
	key := req.URL.Path[len("/v1/keys/"):]

	debug("[recv] DELETE http://%v/v1/keys/%s", raftServer.Name(), key)

	command := &DeleteCommand{}
	command.Key = key

	excute(command, w, req)
}

func excute(c Command, w *http.ResponseWriter, req *http.Request) {
	if raftServer.State() == "leader" {
		if body, err := raftServer.Do(c); err != nil {
			warn("Commit failed %v", err)
			(*w).WriteHeader(http.StatusInternalServerError)
			return
		} else {
			(*w).WriteHeader(http.StatusOK)

			if body == nil {
				return
			}

			body, ok := body.([]byte)
			if !ok {
				panic("wrong type")
			}

			(*w).Write(body)
			return
		}
	} else {
		// current no leader
		if raftServer.Leader() == "" {
			(*w).WriteHeader(http.StatusInternalServerError)
			return
		}

		// tell the client where is the leader
		debug("Redirect to the leader %s", raftServer.Leader())

		path := req.URL.Path

		var scheme string

		if scheme = req.URL.Scheme; scheme == "" {
			scheme = "http://"
		}

		url := scheme + raftTransporter.GetLeaderClientAddress() + path

		debug("redirect to %s", url)

		http.Redirect(*w, req, url, http.StatusTemporaryRedirect)
		return
	}

	(*w).WriteHeader(http.StatusInternalServerError)

	return
}

func MasterHttpHandler(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(raftServer.Leader()))
}

func GetHttpHandler(w *http.ResponseWriter, req *http.Request) {
	key := req.URL.Path[len("/v1/keys/"):]

	debug("[recv] GET http://%v/v1/keys/%s", raftServer.Name(), key)

	command := &GetCommand{}
	command.Key = key

	if body, err := command.Apply(raftServer); err != nil {
		warn("raftd: Unable to write file: %v", err)
		(*w).WriteHeader(http.StatusInternalServerError)
		return
	} else {
		(*w).WriteHeader(http.StatusOK)

		body, ok := body.([]byte)
		if !ok {
			panic("wrong type")
		}

		(*w).Write(body)
		return
	}

}

func ListHttpHandler(w http.ResponseWriter, req *http.Request) {
	prefix := req.URL.Path[len("/v1/list/"):]

	debug("[recv] GET http://%v/v1/list/%s", raftServer.Name(), prefix)

	command := &ListCommand{}
	command.Prefix = prefix

	if body, err := command.Apply(raftServer); err != nil {
		warn("Unable to write file: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		w.WriteHeader(http.StatusOK)

		body, ok := body.([]byte)
		if !ok {
			panic("wrong type")
		}

		w.Write(body)
		return
	}

}

func WatchHttpHandler(w http.ResponseWriter, req *http.Request) {
	key := req.URL.Path[len("/v1/watch/"):]

	command := &WatchCommand{}
	command.Key = key

	if req.Method == "GET" {
		debug("[recv] GET http://%v/watch/%s", raftServer.Name(), key)
		command.SinceIndex = 0

	} else if req.Method == "POST" {
		debug("[recv] POST http://%v/watch/%s", raftServer.Name(), key)
		content := req.FormValue("index")

		sinceIndex, err := strconv.ParseUint(string(content), 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
		command.SinceIndex = sinceIndex

	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if body, err := command.Apply(raftServer); err != nil {
		warn("Unable to write file: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		w.WriteHeader(http.StatusOK)

		body, ok := body.([]byte)
		if !ok {
			panic("wrong type")
		}

		w.Write(body)
		return
	}

}
