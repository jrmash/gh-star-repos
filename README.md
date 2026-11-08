# gh star-repos

[![Build and Test](https://github.com/jrmash/gh-star-repos/actions/workflows/build-test.yml/badge.svg)](https://github.com/jrmash/gh-star-repos/actions/workflows/build-test.yml)
[![Check Code](https://github.com/jrmash/gh-star-repos/actions/workflows/check-code.yml/badge.svg)](https://github.com/jrmash/gh-star-repos/actions/workflows/check-code.yml)
[![Check Markdown](https://github.com/jrmash/gh-star-repos/actions/workflows/check-md.yml/badge.svg)](https://github.com/jrmash/gh-star-repos/actions/workflows/check-md.yml)

Star repositories owned by a GitHub user or organization, newest first, and optionally organize them into your star lists.

```sh
gh star-repos example-org --dry-run
gh star-repos example-org --list 'Go tools' --create-lists --max-repos 10
```

Defaults include accessible public, private, internal, and archived repositories. Forks are excluded. Existing stars can receive missing list memberships, and existing memberships are preserved. Rerunning a completed batch makes no changes.

## Installation

Install [GitHub CLI](https://cli.github.com/), authenticate, and install a published release:

```sh
gh auth login
gh extension install jrmash/gh-star-repos
gh star-repos --help
```

Remote installation requires a published binary release. During development, build locally with Go 1.27.3 or newer:

```sh
git clone https://github.com/jrmash/gh-star-repos.git
cd gh-star-repos
go build .
gh extension install .
```

Windows builds produce `gh-star-repos.exe`. Local builds report `dev`; release binaries report their tag. Upgrade with `gh extension upgrade star-repos`.

The extension uses GitHub CLI for API access, authentication, and host selection. `GH_HOST`, `GH_TOKEN`/`GITHUB_TOKEN`, and enterprise tokens follow [GitHub CLI's rules](https://cli.github.com/manual/gh_help_environment). Credentials stay with GitHub CLI. Discovery includes only repositories accessible to the active account and token; inaccessible repositories cannot be reported as a complete owner inventory.

Star lists require the host's GraphQL list APIs; unsupported hosts fail preflight when lists are requested. For fine-grained tokens, GitHub documents **Starring (write)** user permissions and **Metadata (read)** repository permissions for starring. Private repository access also depends on the token and organization policies. See the [starring API](https://docs.github.com/en/rest/activity/starring). Dry runs validate reads and API availability, but cannot establish whether future mutations will be permitted.

## Usage

```text
gh star-repos [owner] [flags]
gh star-repos config show [--sources] [--format human|json|yaml]
```

Owner may be a user or organization. If omitted, configuration applies, then the authenticated user's login. For an owner named `config`, use `gh star-repos -- config`.

| Flag | Accepted values | Default |
| --- | --- | --- |
| `--archived`, `-a` | `any`, `exclude`, `only` | `any` |
| `--forks` | `any`, `exclude`, `only` | `exclude` |
| `--visibility`, `-v` | `any`, `public`, `private`, `internal` | `any` |
| `--language`, `-l` | Case-insensitive primary language | No filter |
| `--topic`, `-t` | CSV or repeated topics; all must match | No filter |
| `--exclude` | Repeated repository-name globs | No exclusions |
| `--list` | Repeated exact list names | No assignments |
| `--create-lists` | Boolean | `false` |
| `--list-visibility` | New lists: `private`, `public` | `private` |
| `--sleep`, `-s` | Integer seconds between mutations, from 0 to 300 | `1` |
| `--max-repos`, `-m` | Nonnegative integer; `0` means unlimited | `0` |
| `--dry-run` | Boolean | `false` |
| `--format` | `human`, `json`, `yaml` | `human` |
| `--help`, `-h` | Show help without configuration or authentication | — |
| `--version` | Show version without configuration or authentication | — |

Filters combine with AND. Exclusion globs match repository names rather than `OWNER/NAME`, ignoring case. Quote globs so the shell does not expand them. Topics match case-insensitively, and all requested topics must exist. Repositories without a primary language do not match a language filter. List names are exact and case sensitive; a CLI list value preserves spaces and commas.

```sh
# Include forks and select public Go repositories with both topics.
gh star-repos example-org --forks any --visibility public --language Go --topic cli,tools

# Exclude naming patterns and add two lists.
gh star-repos example-org --exclude 'test-*' --exclude '*-old' --list 'Go tools' --list CLI

# Explicitly override inherited settings.
gh star-repos example-org --dry-run=false --create-lists=false --sleep=0 --language= --topic= --exclude= --list=

# Keep stdout machine-readable.
gh star-repos example-org --format json > results.json

# Explain effective configuration without API calls.
gh star-repos config show --sources --format yaml
```

## Configuration

Two optional YAML files are loaded: global `<gh-config-dir>/star-repos.yml`, then local `.gh-star-repos.yml` from the invocation directory. Parent directories are not searched.

The GitHub CLI configuration directory is `GH_CONFIG_DIR`, then `$XDG_CONFIG_HOME/gh`, then `$AppData/GitHub CLI` on Windows, then `$HOME/.config/gh`.

Precedence: **explicit CLI → environment → local YAML → global YAML → defaults**. Settings merge individually. CLI defaults do not override configured values unless the flag is supplied.

| YAML key (global and local) | Primary environment name | Alias | CLI |
| --- | --- | --- | --- |
| `owner` | `GHSR_OWNER` | — | `[owner]` |
| `archived` | `GHSR_OPT_ARCHIVED` | `GHSR_ARCHIVED` | `--archived`, `-a` |
| `forks` | `GHSR_OPT_FORKS` | `GHSR_FORKS` | `--forks` |
| `visibility` | `GHSR_OPT_VISIBILITY` | `GHSR_VISIBILITY` | `--visibility`, `-v` |
| `language` | `GHSR_OPT_LANGUAGE` | `GHSR_LANGUAGE` | `--language`, `-l` |
| `topic` | `GHSR_OPT_TOPIC` | `GHSR_TOPIC` | `--topic`, `-t` |
| `exclude` | `GHSR_OPT_EXCLUDE` | `GHSR_EXCLUDE` | `--exclude` |
| `list` | `GHSR_OPT_LIST` | `GHSR_LIST` | `--list` |
| `create-lists` | `GHSR_OPT_CREATE_LISTS` | `GHSR_CREATE_LISTS` | `--create-lists` |
| `list-visibility` | `GHSR_OPT_LIST_VISIBILITY` | `GHSR_LIST_VISIBILITY` | `--list-visibility` |
| `sleep` | `GHSR_OPT_SLEEP` | `GHSR_SLEEP` | `--sleep`, `-s` |
| `max-repos` | `GHSR_OPT_LIMIT` | `GHSR_MAX_REPOS` | `--max-repos`, `-m` |
| `dry-run` | `GHSR_OPT_DRY_RUN` | `GHSR_DRY_RUN` | `--dry-run` |
| `format` | `GHSR_OPT_FORMAT` | `GHSR_FORMAT` | `--format` |

Primary environment names win over aliases, including explicit empty, `false`, and `0` values. Boolean flags accept `--dry-run=false` and `--create-lists=false`; YAML and environment settings also accept `false`. Archive and fork selectors use `any|exclude|only`.

`topic`, `exclude`, and `list` are YAML sequences and accept one CSV record in the environment. Quote CSV fields containing commas. Higher-priority sequences replace complete lower-priority sequences. An empty environment value, `topic: []`, or `--topic=` clears topics; lists and exclusions work the same way. Empty language clears its filter.

For example, global `star-repos.yml`:

```yaml
archived: any
forks: exclude
sleep: 1
list-visibility: private
```

And `.gh-star-repos.yml` in an organization's directory:

```yaml
owner: example-org
visibility: any
list:
  - Go tools
  - CLI
create-lists: true
exclude:
  - 'test-*'
  - '*-old'
```

An environment override can replace those lists:

```sh
export GHSR_OPT_LIST='"Tools, libraries",CLI'
gh star-repos --dry-run
```

Missing files are allowed. Malformed YAML, unknown or duplicate keys, wrong YAML types, and invalid effective settings fail before API access. YAML must contain one mapping document. `config show --sources` reports each setting's filename, environment name, CLI flag, or `default`. Empty owner in this output means authenticated-user fallback at execution time; configuration inspection makes no API calls.

## Batches, API usage, and lists

Discovery uses GraphQL owner connections with `ownerAffiliations: [OWNER]`, creation-date descending order, and pages of 100. Archive, fork, and visibility filters are sent to GitHub; filters are also enforced locally. Metadata for language and topics is requested only when those filters are active. Collaborator repositories owned by other accounts are excluded.

GitHub exposes `viewerHasStarred` with each repository but has no unstarred-only argument on this connection. Local filtering avoids separate star-status calls and Search API result ceilings. An exhaustive 1,000-repository listing requires about ten repository-page requests, plus authenticated-owner resolution when needed. Each missing star still requires one mutation. See the [GraphQL repository reference](https://docs.github.com/en/graphql/reference/repos).

`--max-repos N` selects the newest N repositories needing **either a star or a requested list membership**, after filters. Completed repositories consume no slots. Failures consume their selected slots; batches are not refilled to reach N successes. Discovery stops after a page fills the batch. Encountering a starred repository does not imply all older repositories are complete. Equal creation timestamps retain GitHub's cursor order.

List names refer to the **authenticated user's lists**. Unknown or ambiguous names fail preflight. `--create-lists` permits creating missing lists before repository mutations, privately by default. Existing list visibility is unchanged. Empty selections do not create empty lists.

GitHub's [list mutation](https://docs.github.com/en/graphql/reference/users#updateuserlistsforitem) supplies complete membership. The extension reads all your lists and paginates their contents once, batches up to ten independent item pages per request, and unions existing and requested memberships. This preserves other lists from the snapshot. Concurrent list edits and overlapping runs for the same account are outside this snapshot guarantee. Without requested lists, all list reads and writes are omitted. Each changed repository needs at most one membership update regardless of how many lists were requested.

Discovery and list preflight must succeed before writes begin. Dry runs perform the same reads and validation, report planned list creations, stars, and assignments, and issue zero GraphQL mutations. They do not sleep between planned changes; API reads may still require rate-limit waits. Reruns rediscover current state and can resume partial work without a checkpoint. There is no unstar, undo, or list-removal operation.

Writes are sequential. `sleep` applies between mutation attempts, including list creation, stars, and assignments, with no delay after the final request. Three retries are allowed for transient and rate-limit failures. `Retry-After` and primary-reset headers take precedence; otherwise exponential delays start at one second for transient failures and one minute for secondary limits. Uncertain list creation is reconciled before retrying. Repository-specific failures allow processing to continue; authentication failures and exhausted rate-limit retries stop the batch. GitHub recommends at least one second between mutations, and large batches may require longer server-directed waits. See [GitHub's rate guidance](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api).

## Output and exit codes

Human output shows an aligned row for each repository, with status symbols and a final summary. Interactive terminals show a transient progress spinner that clears before each result; redirected output contains completed rows and diagnostics without progress chatter. Terminal rows fit the available width, and redirected output preserves full names. GitHub CLI color preferences such as `NO_COLOR` are respected. JSON/YAML each produce one stdout document, including partial results on operational failure or interruption.

Each result reports `star` and `lists` actions independently. A confirmed star remains successful if a later list assignment fails; rerunning can repair only that assignment. Action statuses include `unchanged`, `planned`, `starred`, `updated`, `failed`, and `not_attempted`. Overall repository statuses are `starred`, `listed`, `planned`, or `failed`.

Documents include list-creation outcomes, summary counts, `discovery_complete`, and `limit_reached`. `scanned`, `matched`, and `unchanged` count unique repositories in fetched pages, including the remainder of the final page after the selection limit filled. They are not whole-owner totals when discovery stops early. `discovery_complete` means owner pagination was exhausted; `limit_reached` means the batch filled its configured limit. `processed` counts attempted repositories, `planned` counts dry-run repositories, and `failed` counts repositories with failed actions. Preflight and list-creation failures appear in `error`. Zero matches and completed reruns succeed.

| Code | Meaning |
| --- | --- |
| `0` | Success, completed dry run, or no changes needed |
| `1` | API, command, output, or repository-operation failure |
| `2` | Invalid arguments or configuration |
| `130` | Interrupted; recorded successes remain preserved |

## Development

Architecture follows `gh-get-repos`: `main.go` handles signals; `internal/cli` owns commands, output, and batch orchestration; `internal/settings` resolves configuration; `internal/github` handles API requests and discovery; `internal/stars` applies repository changes; and `internal/executil` provides the injectable command runner. GitHub CLI is invoked without a shell.

```sh
go mod download
go mod verify
go mod tidy -diff
go test -race ./...
go vet ./...
go build .
```

Unit tests use a stateful fake API, injected commands, and fake clocks. They need no credentials and do not change real stars. The optional installed-extension smoke test (`go test -tags smoke ./integration`) uses GitHub CLI authentication for read-only API calls. CI runs on Linux, macOS, and Windows.

The Release PR workflow prepares releases from `main`. Merging its release PR creates a draft release. The GitHub CLI extension precompile action builds, uploads, and attests the binaries; the workflow then publishes the release.

See [Contributing](CONTRIBUTING.md) for local checks, installation smoke tests, and release requirements. See [Security](SECURITY.md) for vulnerability reporting and repository security settings.

## License

Licensed under the [Apache License 2.0](LICENSE).
