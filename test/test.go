package test

import (
	"fmt"
	"github.com/coreos/go-etcd/etcd"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

var client = http.Client{
	Transport: &http.Transport{
		Dial: dialTimeoutFast,
	},
}

// Sending set commands
func Set(stop chan bool) {

	stopSet := false
	i := 0
	c := etcd.NewClient(nil)
	for {
		key := fmt.Sprintf("%s_%v", "foo", i)

		result, err := c.Set(key, "bar", 0)

		if err != nil || result.Key != "/"+key || result.Value != "bar" {
			select {
			case <-stop:
				stopSet = true

			default:
			}
		}

		select {
		case <-stop:
			stopSet = true

		default:
		}

		if stopSet {
			break
		}

		i++
	}
	stop <- true
}

// Create a cluster of etcd nodes
func CreateCluster(size int, procAttr *os.ProcAttr, ssl bool) ([][]string, []*os.Process, error) {
	argGroup := make([][]string, size)

	sslServer1 := []string{"-serverCAFile=./fixtures/ca/ca.crt",
		"-serverCert=./fixtures/ca/server.crt",
		"-serverKey=./fixtures/ca/server.key.insecure",
	}

	sslServer2 := []string{"-serverCAFile=./fixtures/ca/ca.crt",
		"-serverCert=./fixtures/ca/server2.crt",
		"-serverKey=./fixtures/ca/server2.key.insecure",
	}

	for i := 0; i < size; i++ {
		if i == 0 {
			argGroup[i] = []string{"etcd", "-d=/tmp/node1", "-n=node1"}
			if ssl {
				argGroup[i] = append(argGroup[i], sslServer1...)
			}
		} else {
			strI := strconv.Itoa(i + 1)
			argGroup[i] = []string{"etcd", "-n=node" + strI, "-c=127.0.0.1:400" + strI, "-s=127.0.0.1:700" + strI, "-d=/tmp/node" + strI, "-C=127.0.0.1:7001"}
			if ssl {
				argGroup[i] = append(argGroup[i], sslServer2...)
			}
		}
	}

	etcds := make([]*os.Process, size)

	for i, _ := range etcds {
		var err error
		etcds[i], err = os.StartProcess("etcd", append(argGroup[i], "-f"), procAttr)
		if err != nil {
			return nil, nil, err
		}

		// TODOBP: Change this sleep to wait until the master is up.
		// The problem is that if the master isn't up then the children
		// have to retry. This retry can take upwards of 15 seconds
		// which slows tests way down and some of them fail.
		if i == 0 {
			time.Sleep(time.Second * 2)
		}
	}

	return argGroup, etcds, nil
}

// Destroy all the nodes in the cluster
func DestroyCluster(etcds []*os.Process) error {
	for _, etcd := range etcds {
		err := etcd.Kill()
		if err != nil {
			panic(err.Error())
		}
		etcd.Release()
	}
	return nil
}

//
func Monitor(size int, allowDeadNum int, leaderChan chan string, all chan bool, stop chan bool) {
	leaderMap := make(map[int]string)
	baseAddrFormat := "http://0.0.0.0:400%d"

	for {
		knownLeader := "unknown"
		dead := 0
		var i int

		for i = 0; i < size; i++ {
			leader, err := getLeader(fmt.Sprintf(baseAddrFormat, i+1))

			if err == nil {
				leaderMap[i] = leader

				if knownLeader == "unknown" {
					knownLeader = leader
				} else {
					if leader != knownLeader {
						break
					}

				}

			} else {
				dead++
				if dead > allowDeadNum {
					break
				}
			}

		}

		if i == size {
			select {
			case <-stop:
				return
			case <-leaderChan:
				leaderChan <- knownLeader
			default:
				leaderChan <- knownLeader
			}

		}
		if dead == 0 {
			select {
			case <-all:
				all <- true
			default:
				all <- true
			}
		}

		time.Sleep(time.Millisecond * 10)
	}

}

func getLeader(addr string) (string, error) {

	resp, err := client.Get(addr + "/v1/leader")

	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return "", fmt.Errorf("no leader")
	}

	b, err := ioutil.ReadAll(resp.Body)

	resp.Body.Close()

	if err != nil {
		return "", err
	}

	return string(b), nil

}

// Dial with timeout
func dialTimeoutFast(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, time.Millisecond*10)
}
