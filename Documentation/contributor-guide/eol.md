# End of Life (EOL) Process

This document describes the process for ending support (EOL) for etcd versions.

## Overview

etcd follows a predictable release lifecycle to ensure users have time to upgrade between versions while maintaining security and stability. This document outlines the process for ending support for a version.

## Version Support Policy

etcd maintains support for multiple major versions simultaneously. The general support policy is:

- **Latest major version**: Active development and security fixes
- **Previous major versions**: Security and critical bug fixes only
- **Older versions**: No longer supported (EOL)

### Current Support Status

As of the current date, the following etcd versions are supported:

- v3.7.x: Active development (if applicable)
- v3.6.x: Security and critical bug fixes
- v3.5.x: Security and critical bug fixes
- v3.4.x: End of Life (EOL) as of v3.4.45 (2026-06-01)

*Note: Check the latest [etcd releases](https://github.com/etcd-io/etcd/releases) for the most current support status.*

## EOL Process

The EOL process for an etcd version follows these steps:

### 1. Planning and Announcement

- **Timeline**: EOL announcements should be made at least 6 months before the final release
- **Communication**: EOL plans should be communicated through:
  - GitHub issues (create a tracking issue)
  - etcd-dev mailing list
  - etcd website/blog posts
  - Documentation updates

### 2. Final Release Preparation

For the final release of a version before EOL:

- Ensure all critical security fixes are included
- Include a prominent EOL notice in the release notes
- Update the CHANGELOG with the EOL announcement
- Add upgrade documentation to help users migrate to supported versions

### 3. Final Release

The final release before EOL should:

- Follow the standard [release process](release.md)
- Include the EOL notice in the release description
- Tag the release as the final patch for that version
- Update version documentation to mark as EOL

### 4. Post-EOL Actions

After a version reaches EOL:

- **Branch management**: 
  - The release branch remains read-only
  - No further commits or cherry-picks are accepted
  - Branch is archived (not deleted)

- **CI/CD**:
  - Remove the version from active CI pipelines
  - Keep minimal testing for documentation purposes if needed

- **Documentation**:
  - Update all documentation to reflect EOL status
  - Add clear upgrade paths to supported versions
  - Archive version-specific documentation

- **Security**:
  - Security vulnerabilities will not be fixed
  - CVEs may still be filed for documentation purposes
  - Users are strongly advised to upgrade

## EOL Announcement Template

When announcing the EOL of a version, use the following template:

```
Subject: etcd v{VERSION} End of Life Announcement

etcd v{VERSION} will reach End of Life (EOL) on {DATE}.

After this date, no further security updates or bug fixes will be released for v{VERSION}. Users are strongly advised to upgrade to a supported version.

Upgrade Path:
- v{VERSION} → v{NEXT_SUPPORTED_VERSION}

For upgrade instructions, see: {UPGRADE_DOCS_URL}

If you have questions or concerns, please reach out via:
- GitHub Issues: https://github.com/etcd-io/etcd/issues
- etcd-dev mailing list: etcd-dev@googlegroups.com

Thank you for using etcd!
```

## Criteria for EOL

A version should be considered for EOL when:

- A newer major version has been stable for at least 12 months
- Usage metrics show declining adoption (if available)
- Maintainer capacity cannot adequately support the version
- Critical dependencies (Go, protobuf, etc.) become incompatible
- Security vulnerabilities cannot be reasonably addressed

## Emergency EOL

In rare cases, a version may require emergency EOL due to:

- Critical security vulnerabilities that cannot be safely patched
- Complete loss of maintainer capacity
- Fundamental architectural flaws

Emergency EOL follows the same process but with accelerated timelines.

## Related Documentation

- [Release Process](release.md)
- [Upgrade Documentation](https://etcd.io/docs/latest/upgrades/)
- [Security Release Process](../../security/security-release-process.md)
- [Branch Management](branch_management.md)

## Questions

For questions about the EOL process or specific version support status, please:

1. Check the [GitHub releases](https://github.com/etcd-io/etcd/releases) for latest version information
2. Review the [etcd documentation](https://etcd.io/docs/latest/)
3. Contact the etcd maintainers via GitHub issues or the etcd-dev mailing list