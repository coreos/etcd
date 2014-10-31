# Clustering Guide

This guide will walk you through configuring a three machine etcd cluster with
the following details:

|Name	|Address	|
|-------|---------------|
|infra0	|10.0.1.10	|
|infra1	|10.0.1.11	|
|infra2	|10.0.1.12	|

## Static

As we know the cluster members, their addresses and the size of the cluster
before starting we can use an offline bootstrap configuration. Each machine
will get either the following command line or environment variables:

```
ETCD_INITIAL_CLUSTER="infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379"
ETCD_INITIAL_CLUSTER_STATE=new
```

```
-initial-cluster infra0=http://10.0.1.10:2379,http://10.0.1.11:2379,infra2=http://10.0.1.12:2379 \
	-initial-cluster-state new
```

If you are spinning up multiple clusters (or creating and destroying a single cluster) with same configuration for testing purpose, it is highly recommended that you specify a unique `initial-cluster-token` for the different clusters. 
By doing this, etcd can generate unique cluster IDs and member IDs for the clusters even if they otherwise have the exact same configuration. This can protect you from cross-cluster-interaction, which might corrupt your clusters.

On each machine you would start etcd with these flags:

```
$ etcd -name infra0 -initial-advertise-peer-urls https://10.0.1.10:2379 initial-cluster-token etcd-cluster-1\
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379 \
	-initial-cluster-state new
$ etcd -name infra1 -initial-advertise-peer-urls https://10.0.1.11:2379 initial-cluster-token etcd-cluster-1\
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379 \
	-initial-cluster-state new
$ etcd -name infra2 -initial-advertise-peer-urls https://10.0.1.12:2379 initial-cluster-token etcd-cluster-1\
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379 \
	-initial-cluster-state new
```

The command line parameters starting with `-initial-cluster` will be ignored on
subsequent runs of etcd. You are free to remove the environment variables or
command line flags after the initial bootstrap process. If you need to make
changes to the configuration later see our guide on runtime configuration.

### Error Cases

In the following case we have not included our host in the list of
enumerated nodes.
```
$ etcd -name infra1 -initial-advertise-peer-urls http://10.0.1.11:2379 \
	-initial-cluster infra0=http://10.0.1.10:2379 \
	-initial-cluster-state new
etcd: infra1 not listed in the initial cluster config
exit 1
```

In this case we are attempting to map a node (infra0) on a different address
(127.0.0.1:2379) than its enumerated address in the cluster list
(10.0.1.10:2379). If this node is to listen on multiple addresses, all
addresses must be reflected in the "initial-cluster" configuration directive.

```
$ etcd -name infra0 -initial-advertise-peer-urls http://127.0.0.1:2379 \
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379 \
	-initial-cluster-state=new
etcd: infra0 has different advertised URLs in the cluster and advertised peer URLs list
exit 1
```

If you configure a peer with a different set of configuration and attempt to
join this cluster you will get a cluster ID mismatch and etcd will exit.

```
$ etcd -name infra3 -initial-advertise-peer-urls http://10.0.1.13:2379 \
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra3=http://10.0.1.13:2379 \
	-initial-cluster-state=new
etcd: conflicting cluster ID to the target cluster (c6ab534d07e8fcc4 != bc25ea2a74fb18b0). Exiting.
exit 1
```


## Discovery

In a number of cases you might not know the IPs of your cluster peers ahead of
time. This is common when utilizing cloud providers or when your network uses
DHCP. In these cases you can use an existing etcd cluster to bootstrap a new
one. We call this process "discovery".

### Custom etcd discovery service

Discovery uses an existing cluster to bootstrap itself. If you are using your
own etcd cluster you can create a URL like so:

```
$ curl -X PUT https://myetcd.local/v2/keys/discovery/6c007a14875d53d9bf0ef5a6fc0257c817f0fb83/_config/size -d value=3
```

By setting the size key to the URL, you create a discovery URL with expected-cluster-size of 3.

The URL you will use in this case will be
`https://myetcd.local/v2/keys/discovery/6c007a14875d53d9bf0ef5a6fc0257c817f0fb83`
and the etcd members will use the
`https://myetcd.local/v2/keys/discovery/6c007a14875d53d9bf0ef5a6fc0257c817f0fb83`
directory for registration as they start.

Now we start etcd with those relevant flags for each member:

```
$ etcd -name infra0 -initial-advertise-peer-urls http://10.0.1.10:2379 -discovery https://myetcd.local/v2/keys/discovery/6c007a14875d53d9bf0ef5a6fc0257c817f0fb83
$ etcd -name infra1 -initial-advertise-peer-urls http://10.0.1.11:2379 -discovery https://myetcd.local/v2/keys/discovery/6c007a14875d53d9bf0ef5a6fc0257c817f0fb83
$ etcd -name infra2 -initial-advertise-peer-urls http://10.0.1.12:2379 -discovery https://myetcd.local/v2/keys/discovery/6c007a14875d53d9bf0ef5a6fc0257c817f0fb83
```

This will cause each member to register itself with the custom etcd discovery service and begin
the cluster once all machines have been registered.

### Public discovery service

If you do not have access to an existing cluster you can use the public discovery
service hosted at discovery.etcd.io.  You can create a private discovery URL using the
"new" endpoint like so:

```
$ curl https://discovery.etcd.io/new?size=3
https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
```

This will create the cluster with an initial expected size of 3 members. If you
do not specify a size a default of 3 will be used.

```
ETCD_DISCOVERY=https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
```

```
-discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
```

Now we start etcd with those relevant flags for each member:

```
$ etcd -name infra0 -initial-advertise-peer-urls http://10.0.1.10:2379 -discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
$ etcd -name infra1 -initial-advertise-peer-urls http://10.0.1.11:2379 -discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
$ etcd -name infra2 -initial-advertise-peer-urls http://10.0.1.12:2379 -discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
```

This will cause each member to register itself with the discovery service and begin
the cluster once all members have been registered.

You can use the environment variable `ETCD_DISCOVERY_PROXY` to cause etcd to use an HTTP proxy to connect to the discovery service.

### Error and Warning Cases

#### Discovery Server Errors

```
$ etcd -name infra0 -initial-advertise-peer-urls http://10.0.1.10:2379 -discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
etcd: error: the cluster doesn’t have a size configuration value in https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de/_config
exit 1
```

#### User Errors

```
$ etcd -name infra0 -initial-advertise-peer-urls http://10.0.1.10:2379 -discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
etcd: error: the cluster using discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de has already started with all 5 members
exit 1
```

#### Warnings

This is a harmless warning notifying you that the discovery URL will be
ignored on this machine.

```
$ etcd -name infra0 -initial-advertise-peer-urls http://10.0.1.10:2379 -discovery https://discovery.etcd.io/3e86b59982e49066c5d813af1c2e2579cbf573de
etcd: warn: ignoring discovery URL: etcd has already been initialized and has a valid log in /var/lib/etcd
```

## Dynamic Configuration

You may need to change cluster configuration from time to time. A normal case
is to retire an old machine and replace it with a new one. Another one is to
achieve higher availability through expanding the cluster, or gain better
performance through shrinking the cluster.

etcd supports adding and removing member in zero downtime now.

### Add member

Add the member to the cluster first:

```
$ curl -X POST http://myetcd.local/v2/members \
	-H "Content-Type:application/json" \
	-d '{"PeerURLs":["http://10.0.1.13:2379"]}'
```

See [POST /v2/members][https://github.com/coreos/etcd/blob/master/Documentation/0.5/other_apis.md#post-v2members] for more details.

Now start etcd with those relevant flags for the new member:

```
$ etcd -name infra3 \
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379,infra3=http://10.0.1.13:2379 \
	-initial-cluster-state existing
```

The new member will run as a part of the cluster and receive the missing log
from the cluster.

### Remove member

As the first step, you need to find the ID of the removed member, which can
be found in the debug log, or [Get /v2/members][https://github.com/coreos/etcd/blob/master/Documentation/0.5/other_apis.md#get-v2members].

Let us say the ID is 272e204152.

Use HTTP API to remove the member from the cluster:

```
$ curl -X DELETE http://myetcd.local/v2/members/272e204152
```

See [DELETE /v2/members/:id][https://github.com/coreos/etcd/blob/master/Documentation/0.5/other_apis.md#delete-v2membersid] for more details.

etcd that represents the removed member will stop itself, which indicates that
it has been removed successfully:

```
etcd: this member has been permanently removed from the cluster. Exiting.
```

### Error Cases

In the following case we have not included our new host in the list of
enumerated nodes. If this is a new cluster, the node must be added to the list
of initial cluster members.

```
$ etcd -name infra3 \
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379 \
	-initial-cluster-state existing
etcdserver: assign ids error: the member count is unequal
exit 1
```

In this case we give the different address (10.0.1.14:2379) than the one that
we used to join the cluster (10.0.1.13:2379).

```
$ etcd -name infra4 \
	-initial-cluster infra0=http://10.0.1.10:2379,infra1=http://10.0.1.11:2379,infra2=http://10.0.1.12:2379,infra4=http://10.0.1.14:2379 \
	-initial-cluster-state existing
etcdserver: assign ids error: unmatched member while checking PeerURLs
exit 1
```

# 0.4 to 0.5+ Migration Guide

In etcd 0.5 we introduced the ability to listen on more than one address and to
advertise multiple addresses. This makes using etcd easier when you have
complex networking, such as private and public networks on various cloud
providers.

To make understanding this feature easier, we changed the naming of some flags,
but we support the old flags to make the migration from the old to new version
easier.

|Old Flag		|New Flag		|Migration Behavior									|
|-----------------------|-----------------------|---------------------------------------------------------------------------------------|
|-peer-addr		|-initial-advertise-peer-urls 	|If specified, peer-addr will be used as the only peer URL. Error if both flags specified.|
|-addr			|-advertise-client-urls	|If specified, addr will be used as the only client URL. Error if both flags specified.|
|-peer-bind-addr	|-listen-peer-urls	|If specified, peer-bind-addr will be used as the only peer bind URL. Error if both flags specified.|
|-bind-addr		|-listen-client-urls	|If specified, bind-addr will be used as the only client bind URL. Error if both flags specified.|
|-peers			|none			|Deprecated. The -initial-cluster flag provides a similar concept with different semantics. Please read this guide on cluster startup.|
|-peers-file		|none			|Deprecated. The -initial-cluster flag provides a similar concept with different semantics. Please read this guide on cluster startup.|
