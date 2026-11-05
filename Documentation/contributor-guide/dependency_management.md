# Dependency management

## Table of Contents

- **[Main branch](#main-branch)**
  - [Dependencies used in workflows](#dependencies-used-in-workflows)
  - [Bumping order](#bumping-order)
  - [Steps to bump a dependency](#steps-to-bump-a-dependency)
    - [Alternative: Using the update_dep.sh script](#alternative-using-the-update_depsh-script)
  - [Indirect dependencies](#indirect-dependencies)
  - [Known incompatible dependency updates](#known-incompatible-dependency-updates)
    - [arduino/setup-protoc](#arduinosetup-protoc)
  - [Rotation worksheet](#rotation-worksheet)
- **[Stable branches](#stable-branches)**
- **[Golang versions](#golang-versions)**
- **[Core dependencies mappings](#core-dependencies-mappings)**
- **[etcd and Kubernetes version matrix](#etcd-and-kubernetes-version-matrix)**

## Main branch

The dependabot is enabled & [configured](https://github.com/etcd-io/etcd/blob/main/.github/dependabot.yml) to
manage dependencies for etcd `main` branch. But dependabot doesn't work well for multi-module repository like `etcd`,
see [dependabot-core/issues/6678](https://github.com/dependabot/dependabot-core/issues/6678).
Usually, human intervention is required each time when dependabot automatically opens some PRs to bump dependencies.
Please see the guidance below.

### Dependencies used in workflows

The PRs that automatically bump dependencies (see examples below) used in workflows are fine and can be approved & merged directly as long as all checks are successful.

- [build(deps): bump github/codeql-action from 2.2.11 to 2.2.12](https://github.com/etcd-io/etcd/pull/15736)
- [build(deps): bump actions/checkout from 3.5.0 to 3.5.2](https://github.com/etcd-io/etcd/pull/15735)
- [build(deps): bump ossf/scorecard-action from 2.1.2 to 2.1.3](https://github.com/etcd-io/etcd/pull/15607)

### Bumping order

When multiple etcd modules depend on the same package, please bump the package version for all the modules in the correct order. The rule is simple:
if module A depends on module B, then bump the dependency for module B before module A. If the two modules do not depend on each other, then
it doesn't matter to bump which module first. For example, multiple modules depend on `github.com/spf13/cobra`, so we need to bump the dependency
in the following order,

- go.etcd.io/etcd/pkg/v3
- go.etcd.io/etcd/server/v3
- go.etcd.io/etcd/etcdctl/v3
- go.etcd.io/etcd/etcdutl/v3
- go.etcd.io/etcd/tests/v3
- go.etcd.io/etcd/v3
- go.etcd.io/etcd/tools/v3
For more details about etcd Golang modules, please check <https://etcd.io/docs/next/dev-internal/modules>

Note the module `go.etcd.io/etcd/tools/v3` doesn't depend on any other modules, nor by any other modules, so it doesn't matter when to bump dependencies for it.

### Steps to bump a dependency

Use the `github.com/spf13/cobra` as an example, follow the steps below to bump it from 1.6.1 to 1.7.0 for module `go.etcd.io/etcd/etcdctl/v3`,

```bash
cd ${ETCD_ROOT_DIR}/etcdctl
go get github.com/spf13/cobra@v1.7.0
go mod tidy
cd ..
make fix # This will update the bill of materials, Go modules and workspace, etc.
```

Execute the same steps for all other modules. When you finish bumping the dependency for all modules, then commit the change,

```bash
git add .
git commit --signoff -m "dependency: bump github.com/spf13/cobra from 1.6.1 to 1.7.0"
```

Please close the related PRs which were automatically opened by dependabot. 

When you bump multiple dependencies in one PR, it's recommended to create a separate commit for each dependency. But it isn't a must; for example,
you can get all dependencies bumping for the module `go.etcd.io/etcd/tools/v3` included in one commit.

#### Alternative: Using the update_dep.sh script

> Note: Please use bash shell version 5.x or higher.

As an alternative to the manual steps above, you can use the `update_dep.sh` script to automate the dependency bump process across all modules:

```bash
# Update to a specific version
./scripts/update_dep.sh github.com/spf13/cobra v1.7.0

# Update to the latest version
./scripts/update_dep.sh github.com/spf13/cobra
```

The script will:

1. Display the current version of the dependency across all go.mod files
2. Warn and prompt for confirmation if the dependency is purely indirect
3. Update the dependency in all modules that depend on it
4. Run `make fix verify-dep` to ensure consistency across all modules
5. Display the updated versions for verification

This script handles the correct bumping order automatically and ensures version consistency across all modules.

#### Troubleshooting

In an event of bumping the version of protoc, protoc plugins or grpc-gateway, it might change `*.proto` file which can result in the following error:

```bash
[0;31mFAIL: 'genproto' FAILED at Wed Jul 31 07:09:08 UTC 2024
make: *** [Makefile:134: verify-genproto] Error 255
```

To fix the above error, run the following script from the root of etcd repository:

```bash
./scripts/genproto.sh
```

### Indirect dependencies

Usually, we don't bump a dependency if all modules just indirectly depend on it, such as `github.com/go-logr/logr`.

If an indirect dependency (e.g. `D1`) causes any CVE or bugs that affect etcd, usually the module (e.g. `M1`, not part of etcd, but used by etcd)
which depends on it should bump the dependency (`D1`), and then etcd just needs to bump `M1`. However, if the module (`M1`) somehow doesn't
bump the problematic dependency, then etcd can still bump it (`D1`) directly following the same steps above. But as a long-term solution, etcd should
try to remove the dependency on such module (`M1`) that lack maintenance.

For mixed cases, in which some modules directly while others indirectly depend on a dependency, we have multiple options,

- Bump the dependency for all modules, no matter it's direct or indirect dependency.
- Bump the dependency only for modules that directly depend on it.

We should try to follow the first way, and temporarily fall back to the second one if we run into any issue on the first way. Eventually we
should fix the issue and ensure all modules depend on the same version of the dependency.

### Known incompatible dependency updates

#### arduino/setup-protoc

Please refer to [build(deps): bump arduino/setup-protoc from 1.3.0 to 2.0.0](https://github.com/etcd-io/etcd/pull/16016)

### Rotation worksheet

The dependabot scheduling interval is weekly; it means dependabot will automatically raise a bunch of PRs per week.
Usually, human intervention is required each time. We have a [rotation worksheet](https://docs.google.com/spreadsheets/d/1jodHIO7Dk2VWTs1IRnfMFaRktS9IH8XRyifOnPdSY8I/edit#gid=1394774387),
and everyone is welcome to participate; you just need to register your name in the worksheet.

## Stable branches

Usually, we don't proactively bump dependencies for stable releases unless there are any CVEs or bugs that affect etcd.

If we have to do it, then follow the same guidance above. Note that there is no `./scripts/fix.sh`/`make fix` in release-3.4, so no need to
execute it for 3.4.

## Golang versions

For all libraries that exist as independent subprojects (e.g., bbolt, raft, gofail), we should always stick
to the oldest supported Go minor version for all branches, including main. It's up to the users of these
libraries to choose which [Go version](https://go.dev/dl) they want to use in their own projects.

For other subprojects that produce binaries or images (e.g. etcd, etcd-operator, auger), the main
branches should use the latest Go minor version for development, while stable releases should use the
latest patch of the previous supported Go minor version to ensure stability.

Suggested steps for performing a minor version upgrade for the etcd development branch:

1. Carefully review new Go version release notes and potentially related blog posts for any deprecations, performance impacts, or other considerations.
2. Create a GitHub issue to signal intent to upgrade and invite discussion, for example, <https://github.com/etcd-io/etcd/issues/16393>.
3. Complete the upgrade locally in your development environment by editing `.go-version` and running `make fix`.
4. Run performance benchmarks locally to compare before and after.
5. Raise a pull request for the changes, for example, <https://github.com/etcd-io/etcd/pull/16394>.

Stable etcd release branches will be maintained to stay on the latest patch release of a supported Go version. Upgrading minor versions will be completed before the minor version in use currently is no longer supported. Refer to the [Go release policy](https://go.dev/doc/devel/release).

For an example of how to update etcd to a new patch release of Go refer to issue <https://github.com/etcd-io/etcd/issues/16343> and the linked pull requests.

References:

- <https://github.com/kubernetes/sig-release/blob/master/release-engineering/handbooks/go.md>

## Core dependencies mappings

[bbolt](https://github.com/etcd-io/bbolt) and [raft](https://github.com/etcd-io/raft) are two core dependencies of etcd.

Both etcd 3.4.x and 3.5.x depend on bbolt 1.3.x, and etcd 3.6.x depends on bbolt 1.4.x.

raft is included in the etcd repository for release-3.4 and release-3.5 branches, so etcd 3.4.x and 3.5.x do not depend on any
external raft module. We moved raft into [a separate repository](https://github.com/etcd-io/raft) starting from 3.6, and the first raft
release is v3.6.0, so etcd 3.6.0 depends on raft v3.6.0.

Please see the table below:

| etcd versions | bbolt versions | raft versions |
|---------------|----------------|---------------|
| 3.4.x         | v1.3.x         | N/A           |
| 3.5.x         | v1.3.x         | N/A           |
| 3.6.x         | v1.4.x         | v3.6.x        |
| 3.7.x         | v1.5.x         | v3.7.x        |

## etcd and Kubernetes version matrix

The table below shows which Kubernetes releases are tested with each etcd minor version.

| etcd versions | Kubernetes versions    |
|---------------|------------------------|
| 3.5.x         | v1.33 and old versions |
| 3.6.x         | v1.34 - v1.36          |
| 3.7.x         | v1.37                  |
