## Standbys

Adding peers in an etcd cluster adds network, CPU, and disk overhead to the leader since each one requires replication.
Peers primarily provide resiliency in the event of a leader failure but the benefit of more failover nodes decreases as the cluster size increases.
A lightweight alternative is the standby.

Standbys are a way for an etcd node to forward requests along to the cluster but the standbys are not part of the Raft cluster themselves.
This provides an easier API for local applications while reducing the overhead required by a regular peer node.
Standbys also act as standby nodes in the event that a peer node in the cluster has not recovered after a long duration.


## Configuration Parameters

Three configuration parameters used by standbys: active size, promotion delay and standby sync interval.

The active size specifies a target size for the number of peers in the cluster.
If there are not enough peers to meet the active size, standbys will send join requests until the peer count is equal to the active size.
If there are more peers than the target active size then peers are demoted to standbys by the leader.

The promotion delay specifies how long the cluster should wait before removing a dead peer.
By default this is 30 minutes.
If a peer is inactive for 30 minutes then the peer is removed.

The standby sync interval specifies the synchronization interval of standbys with the cluster.
By default this is 30 minutes.
After each interval, Standbys synchronization information with cluster.


## Logical Workflow

### Start a etcd machine

#### Main logic

```
Find cluster as required
If valid to start peer server:
  Goto peer loop
Else:
  Goto standby loop

Peer loop:
  Start peer mode
  If running:
    Wait for stop
  Goto standby loop

Standby loop:
  Start standby mode
  If running:
    Wait for stop
  Goto peer loop
```


#### [Find cluster logic][discovery.md]


#### Join request logic:

```
Fetch machine info
If cannot match version:
  return false
If it has existed in the cluster:
  return true
If active size <= peer count:
  return false
If join request fails:
  return false
return true
```

**Note**
1. [TODO] The running mode cannot be determined by log, because the log may be outdated. But the log could be used to estimate its state.
2. Even if sync cluster fails, it will restart still for recovery from full outage.
3. [BUG] Possible peers from discover URL, peers flag and data dir could be outdated because standby machine doesn't record log. This could make reconnect fail if the whole cluster migrates to new address.


#### Peer mode start logic

```
Start raft server
Monitor the cluster
```


#### Peer mode auto stop logic

```
When removed from the cluster:
  Stop raft server
  Stop monitor
```


#### Standby mode run logic

```
Loop:
  Sleep for some time

  Sync cluster

  If peer count < active size:
    Send join request
    If succeed:
      Return
```


#### Serve Requests as Standby

Don't server requests from peer address.

**Note**
1. Peer address is used for raft communication and cluster management, which should not be used as standby.


Serve requests from client:

```
Redirect all requests to client URL of leader
```

**Note**
1. The leader here implies the one in raft cluster when doing the latest successful synchronization.
2. [IDEA] We could extend HTTP Redirect to multiple possible targets.


### Join Request Handling

```
If machine has existed in the cluster:
  Return
If peer count < active size:
  Add peer
  Increase peer count
```


### Remove Request Handling

```
If machine exists in the cluster:
  Remove peer
  Decrease peer count
```


## Cluster Monitor Logic

### Active Size Monitor:

This is only run by current cluster leader.

```
Loop:
  Sleep for some time

  If peer count > active size:
    Remove randomly selected peer
```


### Peer Activity Monitor

This is only run by current cluster leader.

```
Loop:
  Sleep for some time

  For each peer:
    If peer last activity time > promote delay:
      Remove the peer
      Goto Loop
```


## Cluster Cases

### Create Cluster with Thousands of Instances

First few machines run in peer mode.

All the others check the status of the cluster, and keep in standby mode.


### Recover from full outage

Machines with log data restart with join failure.

Machines in peer mode recover heartbeat between each other.

Machines in standby mode always sync the cluster. If sync fails, it uses the first address from data log as redirect target.

**Note**
1. [TODO] Machine which runs in standby mode and has no log data cannot be recovered.


### Kill one peer machine

Leader of the cluster lose the connection with the peer.

When the time exceeds promotion delay, it removes the peer from the cluster.

Machine in standby mode finds one available place of the cluster. It sends join request and joins the cluster.

**Note**
1. [TODO] Machine which was divided from majority and was removed from the cluster will distribute running of the cluster if it reconnects back.


### Kill one standby machine

No change for the cluster.


## Cons

1. New instance cannot join immediately after one peer is kicked out of the cluster, because the leader doesn't know the info about the standby instances.

2. It may introduce join collision

3. Cluster needs a good interval setting to balance the join delay and join collision.


## Future Attack Plans

1. Based on heartbeat miss and promotion delay, standby could adjust its next check time.

2. Preregister the promotion target when heartbeat miss happens.

3. Get the estimated cluster size from the check happened in the sync interval, and adjust sync interval dynamically.

4. Accept join requests based on active size and alive peers.
