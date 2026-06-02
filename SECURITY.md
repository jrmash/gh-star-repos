# Security policy

Report vulnerabilities privately through the repository's Security → Report a vulnerability feature when enabled. If private reporting is unavailable, contact the repository maintainer through a private channel before sharing sensitive details publicly. Include the affected version, operating system, reproduction steps, and impact; redact tokens, private repository names, and credentials.

Use the latest stable release for fixes. The project does not currently commit to backporting fixes to older release lines.

The extension relies on GitHub CLI for authentication and API access. Credentials stay with GitHub CLI; configuration files hold extension settings such as the owner, filters, star lists, and whether missing lists may be created. Review a directory's `.gh-star-repos.yml` and any environment overrides before running the command there. Use `gh star-repos config show --sources` to inspect effective settings without API access and `--dry-run` to preview changes without adding stars, creating lists, or changing list memberships.

Repository automation includes CodeQL for Go and GitHub Actions, a reachable-vulnerability scan with `govulncheck`, and weekly Dependabot updates. Required PR checks gate merges when branch protection is configured. After a release PR is merged, publishing builds the release commit without repeating those checks. Release assets include provenance attestations produced by the official extension release action.

See [CONTRIBUTING.md](CONTRIBUTING.md#hosted-repository-setup) for hosted security settings that maintainers must enable. Availability depends on the repository and GitHub plan; see [GitHub's code scanning requirements](https://docs.github.com/en/code-security/how-tos/find-and-fix-code-vulnerabilities/manage-your-configuration/codeql-for-compiled-languages).
