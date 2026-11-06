# Container Images

The container images defined in this directory are used to maintain
depenendencies up to date. **These images are not used to build the project.**

## `devcontainer`

The `devcontainer` image triggers [Dependabot] to do a version bump. After that,
a [GitHub action] will update other artifacts in the repository.

## `trivy`

We keep the `trivy` image to track the tool version. Even though it is a Go
project, it's not possible to track it as our other tools
(`tools/mod/tools.go`), because they point the Go directive to the latest
version patch. This breaks our Go workspace.

[Dependabot]: ../../.github/dependabot.yml
[GitHub action]: ../../.github/workflows/bump-devcontainer-version.yml
