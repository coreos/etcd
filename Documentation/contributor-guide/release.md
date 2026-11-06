# Release

The guide talks about how to release a new version of etcd.

The procedure includes some manual steps for sanity checking, but it can probably be further scripted. Please keep this document up-to-date if making changes to the release process.

## Release management

Under the leadership of **Ivan Valdes Castillo** [@ivanvc](https://github.com/ivanvc), the following pool of release candidates manages the release of each etcd major/minor version as well as manages patches
to each stable release branch. They are responsible for communicating the timelines and status of each release and
for ensuring the stability of the release branch.

- Benjamin Wang [@ahrtr](https://github.com/ahrtr)
- Fu Wei [@fuweid](https://github.com/fuweid)
- Josh Berkus [@jberkus](https://github.com/jberkus)
- Ivan Valdes Castillo [@ivanvc](https://github.com/ivanvc)
- Marek Siarkowicz [@serathius](https://github.com/serathius)
- Siyuan Zhang [@siyuanfoundation](https://github.com/siyuanfoundation)

All release version numbers follow the format of [semantic versioning 2.0.0](http://semver.org/).

### Major, minor version release, or its pre-release

- Ensure the relevant [milestone](https://github.com/etcd-io/etcd/milestones) on GitHub is complete. All referenced issues should be closed or moved elsewhere.
- Ensure the latest [upgrade documentation](https://etcd.io/docs/next/upgrades) is available.
- Bump [hardcoded MinClusterVersion in the repository](https://github.com/etcd-io/etcd/blob/v3.4.15/version/version.go#L28), if necessary.
- Add feature capability maps for the new version, if necessary.

### Patch version release

- To request a backport, developers submit cherry-pick PRs targeting the release branch. The commits should not include merge commits. The commits should be restricted to bug fixes and security patches.
- The cherrypick PRs should target the appropriate release branch (`base:release-<major>-<minor>`). The k8s infra cherry pick robot `/cherrypick <branch>` PR chatops command may be used to automatically generate cherrypick PRs.
- The release patch manager reviews the cherrypick PRs. Please discuss carefully what is backported to the patch release. Each patch release should be strictly better than its predecessor.
- The release patch manager will cherry-pick these commits starting from the oldest one into stable branch.

## Write a release note

- Write an introduction for the new release. For example, what major bug we fix, what new features we introduce, or what performance improvement we make.
- Put `[GH XXXX]` at the head of the change line to reference the Pull Request that introduces the change. Moreover, add a link on it to jump to the Pull Request.
- Find PRs with the `release-note` label and explain them in the `NEWS` file, as a straightforward summary of changes for end-users.

## Patch release criteria

The etcd project aims to release a new patch version if any of the following conditions are met:

- Fixed one or more major CVEs (>=7.5).
- Fixed one or more critical bugs.
- Fixed three or more major bugs.
- Fixed five or more minor bugs.

## Release guide

### Prerequisites

There are some prerequisites, which should be done before the release process. These are one-time operations,
which don't need to be executed before releasing each version.
1. Generate a GPG key and add it to your GitHub account. Refer to the links on [settings](https://github.com/settings/keys).
2. Ensure you have a Linux machine, on which the git, Golang, and docker have been installed.
    - Ensure the Golang version matches the version defined in `.go-version` file.
    - Ensure non-privileged users can run docker commands, refer to the [Linux postinstall](https://docs.docker.com/engine/install/linux-postinstall/).
    - Ensure there is at least 5GB of free space on your Linux machine.
3. Install gsutil, refer to [gsutil_install](https://cloud.google.com/storage/docs/gsutil_install). When asked about cloud project to use, pick `etcd-development`.
4. Authenticate the image registry, refer to [Authentication methods](https://cloud.google.com/container-registry/docs/advanced-authentication).
   - `gcloud auth login`
   - `gcloud auth configure-docker`
5. Install gh, refer to [GitHub's documentation](https://github.com/cli/cli#installation). Ensure that running
   `gh auth login` succeeds for the GitHub account you use to contribute to etcd,
   and that `gh auth status` has a clean exit and doesn't show any issues.

### Release steps

About one week before the release:

1. Raise an issue to publish the release plan, e.g. [issues/17350](https://github.com/etcd-io/etcd/issues/17350).
2. Raise a `kubernetes/org` pull request ([example PR](https://github.com/kubernetes/org/pull/5582)) to ensure members of the release team are added to the [release github team](https://github.com/orgs/etcd-io/teams/release-etcd).
3. Start a draft of the release announcement for the blog and mailing list.  If mirroring to Kubernetes blog (see Announcing), notify sig-docs-blog and raise a placeholder PR there.  If engaging sig-contribex-comms, open a comms request.

About one day before the release:

1. If this is the first RC release for a major/minor update, then make the new version of the docs live, and generate a new draft version for the next minor release.

On the day of the release:

1. Verify you can pass the authentication to the image registries,
   - `docker login gcr.io`
   - `docker login quay.io`
     - If the release person doesn't have access to 1password, one of the owners (@ahrtr, @ivanvc, @jmhbnz, @serathius) needs to share the password with them per [this guide](https://support.1password.com/share-items/). See rough steps below,
       - [Sign in](https://team-etcd.1password.com/home) to your account on 1password.com.
       - Click `Your Vault Items` on the right side.
       - Select `Password of quay.io`.
       - Click `Share` on the top right, and set expiration as `1 hour` and only available to the release person using his/her email.
       - Click `Copy Link` then send the link to the release person via slack or email.
2. Clone the etcd repository and checkout the target branch,
   - `git clone --branch release-3.X git@github.com:etcd-io/etcd.git`
3. Run the release script under the repository's root directory, replacing `${VERSION}` with a value without the `v` prefix, i.e. `3.5.13`.
   - `DRY_RUN=false ./scripts/release.sh ${VERSION}`
   - **NOTE:** When doing a pre-release (i.e., a version from the main branch, 3.6.0-alpha.2), you will need to explicitly set the branch to main:

     ```bash
     DRY_RUN=false BRANCH=main ./scripts/release.sh ${VERSION}
     ```

   It generates all release binaries under the directory `/tmp/etcd-release-${VERSION}/etcd/release/` and images. Binaries are pushed to the Google Cloud bucket
   under project `etcd-development`, and images are pushed to `quay.io` and `gcr.io`.
   - It is advisable to do a dry run before the actual release. This will create a `/tmp` directory. Do **NOT** forget to remove this directory before the actual release.

      ```bash
      DRY_RUN=true BRANCH=${BRANCH} ./scripts/release.sh ${VERSION}
      ```

4. Publish the release page on GitHub
   - Open the **draft** release URL shown by the release script
   - Click the pen button at the top right to edit the release
   - Review that it looks correct, reviewing that the bottom checkboxes are checked depending on the
     release version (v3.4 & v3.5 no checkboxes, v3.6 has the set as latest release checkbox checked,
     v3.7 has the set as pre-release checkbox checked)
   - Then, publish the release
5. Update the changelog to reflect the correct release date.
6. Announce the release, see [Announcing](#announcing) below.
7. If this is a minor/major release, update the documentation to remove the "draft" tag from the latest version of the documentation, and create a new docs tree for the next version.
8. Paste the release link to the issue raised in Step 1 and close the issue.
9. Raise a follow-up `kubernetes/org` pull request to return the GitHub release team to empty, least privilege state.
10. Create a new stable branch through `git push origin release-${VERSION_MAJOR}.${VERSION_MINOR}` if this is a new major or minor stable release.
11. Re-generate a new password for quay.io if needed (e.g. shared to a contributor who isn't in the release team, and we should rotate the password at least once every 3 months).
12. Bump the new etcd release in Kubernetes, refer to [Bump etcd Version in Kubernetes](bump_etcd_version_k8s.md).

- For etcd 3.7 patches, bump it to Kubernetes 1.35 and all newer minor versions (including `master` branch)
- For etcd 3.5 patches, bump it to Kubernetes 1.33 and all older supported versions

#### Announcing

Usually, release announcements are handled by the SIG-etcd docs team, sometimes with support from SIG-Contribex and/or SIG-Docs Blog subproject.  We have several channels available for announcing releases:

- #sig-etcd Slack channel
- etcd-dev mailing list
- etcd.io blog
- Kubernetes blog
- Kubernetes community social media channels
- Kubernetes dev mailing list
- Additional Kubernetes Slack channels, such as #announcements

etcd releases are announced in different places depending on the content of the release. All releases, patch, minor, or major are announced in etcd's slack channel, mailing list, and blog. Releases which require more awareness are announced additional places:

- Patch releases which fix critical CVEs or data loss bugs that affect Kubernetes users and/or most etcd users: kubernetes dev mailing list, additional Kubernetes Slack channels, possibly social media channels, depending on severity and affected users
- First beta (or RC if no betas) of a new major/minor release: Kubernetes blog, Kubernetes community social media channels
- Major/Minor final release: Kubernetes blog, Kubernetes social media channels, Kubernetes dev mailing list, #announcements Slack channel

For patch releases, the text of the blog post and the release email will be the same, and should cover the basics of what's in the patch release and how it affects users; see previous announcements for examples.  Announcements on Slack will be abbreviated versions, with a link to the blog post.

For minor/major releases, the blog post will be extensive and will cover features and contribution stories in detail. Announcements elsewhere will be shorter, and will link back to this blog post.

#### Release known issues

1. Timeouts pushing binaries - If binaries fail to fully upload to Google Cloud storage, the script must be re-run using the same command. Any artifacts that are already pushed will be overwritten to ensure they are correct. The storage bucket does not use object versioning so incorrect files cannot remain.

2. Timeouts pushing images - It is rare, although possible for connection timeouts to occur when publishing etcd release images to `quay.io` or `gcr.io`. If this occurs, it is known to be safe to rerun the release script command appending the `--no-upload` flag, and image uploads will gracefully resume.

3. GPG vs SSH signing - The release scripts assume that git tags will be signed with a GPG key. Since 2022 GitHub has supported [signing commits and tags using ssh](https://github.blog/changelog/2022-08-23-ssh-commit-verification-now-supported). Until further release script updates are completed you will need to disable this feature in your `~/.gitconfig` and revert to signing via GPG to perform etcd releases.
