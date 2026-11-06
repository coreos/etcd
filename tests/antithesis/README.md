# etcd Antithesis tests

This document describes the etcd test integration with [Antithesis].
Antithesis provides a testing platform that allows you to explore edge cases, race conditions, and rare
bugs that are difficult or impossible to reproduce in a normal environment.

[![Watch the video](https://img.youtube.com/vi/qgCpdb2sbh8/hqdefault.jpg)](https://youtu.be/qgCpdb2sbh8)

[Antithesis]: https://antithesis.com/

## Robustness vs Antithesis tests

[Antithesis] runs the robustness tests inside their
[deterministic simulation testing](https://antithesis.com/resources/deterministic_simulation_testing/)
environment and [fault injection](https://antithesis.com/docs/environment/fault_injection/).

For more details on robustness tests, see the [robustness directory](../robustness).

## Antithesis Setup

The setup consists of a 3-node etcd cluster and a client container, orchestrated
via [Kubernetes](https://antithesis.com/docs/getting_started/setup_k8s/).

During the etcd Antithesis test suite the etcd server is built with the following patches:

* **Critical code locations**: We replace etcd `gofail` comments (which signify
  code locations important for failure injection in robustness tests) with
  Antithesis `assert.Reachable`. This guides Antithesis to explore the
  execution space around these points.
* **Assertions**: We change etcd `verify` package assertions to Antithesis
  `assert.Always`, encouraging the platform to try and break those assertions.
* **Instrumentation**: The etcd binary is instrumented using
  `antithesis-go-instrumentor` to enable coverage tracking and feedback for
  the Antithesis platform.

The Antithesis etcd tests configure the
[Test Composer](https://antithesis.com/docs/test_templates/test_composer_reference/)
in the following way:

* **`entrypoint`**:
  * Waits for all etcd nodes to be healthy.
  * Emits the `setup_complete` message to Antithesis to start the testing phase.
* **`singleton_driver_traffic`**:
  * Generates robustness test traffic against the cluster while faults are injected.
  * Runs as a [Singleton Driver Command], meaning it is the only one generating traffic.
  * All generated traffic is saved as an operation history and stored on a shared volume.
* **`finally_validation`**:
  * Runs as a [Finally Command], meaning it is the last to run, with failure injection disabled.
  * Reads the history of operations and validates them using the robustness test validation logic.
  * Results of robustness tests are executed as Antithesis `assert.Always` assertions.
  * Similar to robustness tests, it emits a visualization of the operations
    history to an HTML file that is uploaded to the Antithesis platform.

[Singleton Driver Command]: https://antithesis.com/docs/test_templates/test_composer_reference/#singleton-driver
[Finally Command]: https://antithesis.com/docs/test_templates/test_composer_reference/#finally-command

## Running tests locally with Kubernetes

### Prerequisites

Please make sure that you have the following tools installed on your local:

* [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)
* [kind](https://kind.sigs.k8s.io/docs/user/quick-start#installation)

## Quickstart

### 1. Build and Tag the Docker Images

Run this command from the `antithesis/test-template` directory:

```bash
make antithesis-build-client-docker-image
make antithesis-build-etcd-image
```

Both commands build etcd-server and etcd-client from the current branch. To build a different version of etcd you can use:

```bash
make antithesis-build-etcd-image REF=${GIT_REF} 
```

#### 2. Set up the Kubernetes cluster

Run the following command from the root directory for Antithesis tests (`tests/antithesis`):

```bash
make antithesis-k8s-up
```

This command will:

* Create a local `kind` cluster named `etcd-antithesis`.
* Load the built `etcd-client` and `etcd-server` images into the cluster.
* Deploy the Kubernetes manifests from `config/manifests`.

The client pod will continuously check the health of the etcd nodes and wait for the cluster to be healthy.

#### 3. Running the tests

To run the tests locally against the deployed cluster:

```bash
make antithesis-run-k8s-traffic
make antithesis-run-k8s-validation
```

#### 4. Prepare for next run

Unfortunately robustness tests don't support running on a non-empty database.
So for now you need to cleanup the storage before repeating the run or you will get "non empty database at start, required by model used for linearizability validation" error.

```bash
make antithesis-clean
```

This command will delete the `kind` cluster and remove the local data directories.

### Troubleshooting

* **Image Pull Errors**: If Kubernetes can’t pull `etcd-client:latest`, make sure you built it locally and that it was successfully loaded into the `kind` cluster.
