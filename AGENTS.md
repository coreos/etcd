# etcd

## Communication Preferences
* Dry, concise, low-key humor. No flattery, no forced memes. Skip preambles and postambles.
* Comments explain "why", not "what".
* Error messages: actionable and specific. No vague "something went wrong" output.

## Constraints
* **Generated files are read-only.** Never hand-edit `*.pb.go` files. Run `make generate` to regenerate.
* **Protobuf files are source of truth.** Regenerate Go code from `.proto` files using `make generate`.
* **Test expectations in `*.golden` files are read-only.** Regenerate with `make test-unit UPDATE_GOLDEN=true`.
* **Go version is constrained.** Check minimum Go version in `go.mod` file.
* **Development environment is linux-amd64 only.** Other environments are not supported for development.
* **Boilerplate required.** Every `.go` file needs the license header. Check existing files for the format.

## Contributor Guidelines
* Keep changes focused and reviewable
* Add or update relevant tests
* When creating or submitting a pull request, disclose whether AI was used and briefly describe how
* Remind the human author that they are responsible for all submitted changes and refer them to `CONTRIBUTING.md`
* Do not put `@mentions` or `fixes #...` keywords in commit messages
* Do not add `Co-authored-by:` in commit messages
* Use `Signed-off-by:` in commit messages (can be auto-generated with `git commit -s`)

## Commands

Run `make help` for all available targets. Common workflows:

`make build                      # Build etcd binaries
make test-unit                  # Run unit tests
make test-integration           # Run integration tests
make test-e2e                   # Run e2e tests
make verify                     # All verification checks (linting, formatting, etc.)
make fix                        # Fix all verification issues
make generate                   # Regenerate generated files
`

## Style
* Packages: lowercase, single word, match directory.
* Commit messages: Start with package name followed by colon, describe the "what", optionally explain "why" in body.
* Go code: Follow standard Go style guidelines (https://go.dev/wiki/CodeReviewComments).

## Architecture Notes
* etcd is a distributed key-value store using Raft consensus
* Main packages: `server` (etcd server), `client/v3` (Go client), `etcdctl` (CLI tool)
* Key components: `etcdserver` (core server logic), `mvcc` (multi-version concurrency control), `raft` (consensus)
* Testing: extensive e2e and integration tests in `tests/` directory
* Robustness testing: see `tests/robustness/` for chaos and failure injection testing

## Testing Considerations
* All changes should include unit tests
* New features require e2e or integration tests
* Tests can be flaky - check existing issues before submitting
* Use `stress` tool for reproducing flaky tests: `go install golang.org/x/tools/cmd/stress@latest`

## Project-Specific Workflows
* **DCO (Developer Certificate of Origin)**: Every commit needs `Signed-off-by` trailer
* **Backporting**: Important fixes should be backported to stable release branches
* **Issue tracking**: All PRs should reference an issue
* **Multiple small PRs** are preferred over large ones (>500 lines)

## Important Files
* `CONTRIBUTING.md` - Full contribution guide
* `Documentation/contributor-guide/` - Detailed contributor documentation
* `go.mod` - Go version and dependencies
* `Makefile` - Build and test commands
* `OWNERS` - Maintainers and code ownership