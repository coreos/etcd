# etcd

[![Coverage](https://codecov.io/gh/etcd-io/etcd/branch/main/graph/badge.svg)](https://app.codecov.io/gh/etcd-io/etcd/tree/main)
[![Tests](https://github.com/etcd-io/etcd/actions/workflows/tests.yaml/badge.svg)](https://github.com/etcd-io/etcd/actions/workflows/tests.yaml)
[![codeql-analysis](https://github.com/etcd-io/etcd/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/etcd-io/etcd/actions/workflows/codeql-analysis.yml)
[![Docs](https://img.shields.io/badge/docs-latest-green.svg)](https://etcd.io/docs)
[![Godoc](http://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)](https://godocs.io/go.etcd.io/etcd/v3)
[![Releases](https://img.shields.io/github/release/etcd-io/etcd/all.svg?style=flat-square)](https://github.com/etcd-io/etcd/releases)
[![LICENSE](https://img.shields.io/github/license/etcd-io/etcd.svg?style=flat-square)](https://github.com/etcd-io/etcd/blob/main/LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/etcd-io/etcd/badge)](https://scorecard.dev/viewer/?uri=github.com/etcd-io/etcd)

**Note**: The `main` branch may be in an *unstable or even broken state* during development. For stable versions, see [releases][github-release].

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/cncf/artwork/9870640f123303a355611065195c43ac3f27aa19/projects/etcd/horizontal/white/etcd-horizontal-white.png">
  <source media="(prefers-color-scheme: light)" srcset="logos/etcd-horizontal-color.svg">
  <img alt="etcd logo" src="logos/etcd-horizontal-color.svg" width=269 />
</picture>

etcd is a distributed reliable key-value store for the most critical data of a distributed system, with a focus on being:

* *Simple*: well-defined, user-facing API (gRPC)
* *Secure*: automatic TLS with optional client cert authentication
* *Fast*: benchmarked 10,000 writes/sec
* *Reliable*: properly distributed using Raft

etcd is written in Go and uses the [Raft][] consensus algorithm to manage a highly-available replicated log.

etcd is used [in production by many companies](./ADOPTERS.md), and the development team stands behind it in critical deployment scenarios, where etcd is frequently teamed with applications such as [Kubernetes][k8s], [locksmith][], [vulcand][], [Doorman][], and many others. Reliability is further ensured by rigorous [**robustness testing**](https://github.com/etcd-io/etcd/tree/main/tests/robustness).

See [etcdctl][etcdctl] for a simple command line client.

![etcd reliability is important](logos/etcd-xkcd-2347.png)

<sub>Original image credited to  xkcd.com/2347, alterations by Josh Berkus.</sub>

[raft]: https://raft.github.io/
[k8s]: http://kubernetes.io/
[doorman]: https://github.com/youtube/doorman
[locksmith]: https://github.com/coreos/locksmith
[vulcand]: https://github.com/vulcand/vulcand
[etcdctl]: https://github.com/etcd-io/etcd/tree/main/etcdctl

## Documentation

The most common API documentation you'll need can be found here:

* [go.etcd.io/etcd/api/v3](https://godocs.io/go.etcd.io/etcd/api/v3)
* [go.etcd.io/etcd/client/pkg/v3](https://godocs.io/go.etcd.io/etcd/client/pkg/v3)
* [go.etcd.io/etcd/client/v3](https://godocs.io/go.etcd.io/etcd/client/v3)
* [go.etcd.io/etcd/etcdctl/v3](https://godocs.io/go.etcd.io/etcd/etcdctl/v3)
* [go.etcd.io/etcd/pkg/v3](https://godocs.io/go.etcd.io/etcd/pkg/v3)
* [go.etcd.io/etcd/raft/v3](https://godocs.io/go.etcd.io/etcd/raft/v3)
* [go.etcd.io/etcd/server/v3](https://godocs.io/go.etcd.io/etcd/server/v3)

## Maintainers

[Maintainers](OWNERS) strive to shape an inclusive open source project culture where users are heard and contributors feel respected and empowered. Maintainers aim to build productive relationships across different companies and disciplines. Read more about [Maintainers role and responsibilities](Documentation/contributor-guide/community-membership.md#maintainers).

## Getting started

### Getting etcd

The easiest way to get etcd is to use one of the pre-built release binaries which are available for OSX, Linux, Windows, and Docker on the [release page][github-release].

For more installation guides, please check out [operating etcd](https://etcd.io/docs/latest/op-guide).

[github-release]: https://github.com/etcd-io/etcd/releases

### Running etcd

First start a single-member cluster of etcd.

If etcd is installed using the [pre-built release binaries][github-release], run it from the installation location as below:

```bash
/tmp/etcd-download-test/etcd
```

The etcd command can be simply run as such if it is moved to the system path as below:

```bash
mv /tmp/etcd-download-test/etcd /usr/local/bin/
etcd
```

This will bring up etcd listening on port 2379 for client communication and on port 2380 for server-to-server communication.

Next, let's set a single key, and then retrieve it:

```bash
etcdctl put mykey "this is awesome"
etcdctl get mykey
```

etcd is now running and serving client requests. For more, please check out:

* [Animated quick demo](https://etcd.io/docs/latest/demo)

### etcd TCP ports

The [official etcd ports][iana-ports] are 2379 for client requests, and 2380 for peer communication.

[iana-ports]: http://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.txt

### Running a local etcd cluster

First install [goreman](https://github.com/mattn/goreman), which manages Procfile-based applications.

Our [Procfile script](./Procfile) will set up a local example cluster. Start it with:

```bash
goreman start
```

This will bring up 3 etcd members `infra1`, `infra2` and `infra3` and optionally etcd `grpc-proxy`, which runs locally and composes a cluster.

Every cluster member and proxy accepts key value reads and key value writes.

Follow the comments in [Procfile script](./Procfile) to add a learner node to the cluster.

### Install etcd client v3

```bash
go get go.etcd.io/etcd/client/v3
```

### Next steps

Now it's time to dig into the full etcd API and other guides.

* Read the full [documentation].
* Review etcd [frequently asked questions].
* Explore the full gRPC [API].
* Set up a [multi-machine cluster][clustering].
* Learn the [config format, env variables and flags][configuration].
* Find [language bindings and tools][integrations].
* Use TLS to [secure an etcd cluster][security].
* [Tune etcd][tuning].

[documentation]: https://etcd.io/docs/latest
[api]: https://etcd.io/docs/latest/learning/api
[clustering]: https://etcd.io/docs/latest/op-guide/clustering
[configuration]: https://etcd.io/docs/latest/op-guide/configuration
[integrations]: https://etcd.io/docs/latest/integrations
[security]: https://etcd.io/docs/latest/op-guide/security
[tuning]: https://etcd.io/docs/latest/tuning

## Contact

* Email: [etcd-dev](https://groups.google.com/g/etcd-dev)
* Slack: [#sig-etcd](https://kubernetes.slack.com/archives/C3HD8ARJ5) channel on Kubernetes ([get an invite](http://slack.kubernetes.io/))
* [Community meetings](#community-meetings)

### Community meetings

etcd contributors and maintainers meet every week at `11:00` AM (USA Pacific) on Thursday and meetings alternate between community meetings and issue triage meetings. Meeting agendas are recorded in a [shared Google doc][shared-meeting-notes] and everyone is welcome to suggest additional topics or other agendas.

Issue triage meetings are aimed at getting through our backlog of PRs and Issues. Triage meetings are open to any contributor; you don't have to be a reviewer or approver to help out! They can also be a good way to get started contributing.

The meeting lead role is rotated for each meeting between etcd maintainers or sig-etcd leads and is recorded in a [shared Google sheet][shared-rotation-sheet].

Meeting recordings are uploaded to the official etcd [YouTube channel].

Get calendar invitations by joining [etcd-dev](https://groups.google.com/g/etcd-dev) mailing group.

Join the CNCF-funded Zoom channel: [zoom.us/my/cncfetcdproject](https://zoom.us/my/cncfetcdproject)

[shared-meeting-notes]: https://docs.google.com/document/d/16XEGyPBisZvmmoIHSZzv__LoyOeluC5a4x353CX0SIM/edit
[shared-rotation-sheet]: https://docs.google.com/spreadsheets/d/1jodHIO7Dk2VWTs1IRnfMFaRktS9IH8XRyifOnPdSY8I/edit
[YouTube channel]: https://www.youtube.com/@etcdio

## Contributing

See [CONTRIBUTING](CONTRIBUTING.md) for details on setting up your development environment, submitting patches and the contribution workflow.

Please refer to [community-membership.md](Documentation/contributor-guide/community-membership.md#member) for information on becoming an etcd project member.  We welcome and look forward to your contributions to the project!

Please also refer to [roadmap](Documentation/contributor-guide/roadmap.md) to get more details on the priorities for the next few major or minor releases.

## Reporting bugs

See [reporting bugs](https://github.com/etcd-io/etcd/blob/main/Documentation/contributor-guide/reporting_bugs.md) for details about reporting any issues. Before opening an issue please check it is not covered in our [frequently asked questions].

[frequently asked questions]: https://etcd.io/docs/latest/faq

## Reporting a security vulnerability

See [security disclosure and release process](security/README.md) for details on how to report a security vulnerability and how the etcd team manages it.

## Issue and PR management

See [issue triage guidelines](https://github.com/etcd-io/etcd/blob/main/Documentation/contributor-guide/triage_issues.md) for details on how issues are managed.

See [PR management](https://github.com/etcd-io/etcd/blob/main/Documentation/contributor-guide/triage_prs.md) for guidelines on how pull requests are managed.

## etcd Emeritus Maintainers

etcd [emeritus maintainers](OWNERS) dedicated a part of their career to etcd and reviewed code, triaged bugs and pushed the project forward over a substantial period of time. Their contribution is greatly appreciated.

### License

etcd is under the Apache 2.0 license. See the [LICENSE](LICENSE) file for details.
