# Custom releases

The repository's default branch is `custom`. **Custom Release** runs daily at
01:17 UTC (09:17 UTC+8), checks MetaCubeX/mihomo's latest non-prerelease GitHub
Release, and merges only that release's `vX.Y.Z` tag. It does not merge unreleased
commits from `Meta` or `Alpha`. If the release is already merged, it stops without
building or publishing. **Actions → Custom Release → Run workflow** performs
the same check immediately.

When a new upstream release exists, the workflow merges it locally, runs tests,
then pushes the tested merge and its version tag atomically and packages that
exact commit. Merge conflicts, test failures, or a rejected push stop the
workflow. It never force
pushes the branch or automatically resolves conflicts. No release is published
unless both platform builds succeed. Ordinary pushes to `custom` do not build.

To build local custom changes manually, push a version tag:

```sh
git push origin HEAD:refs/heads/custom
git tag v1.19.33-custom.2
git push origin v1.19.33-custom.2
```

The tag's upstream version must match the upstream stable tag in that commit's
history. The version tag is used verbatim. Pushing a tag named `custom` also
triggers packaging and chooses a version automatically. To move that trigger
again, update only the `custom` tag; published version tags stay fixed:

```sh
git tag -f custom
git push origin refs/tags/custom --force
```

Automatic versions use the upstream stable version and this workflow's run
number, for example `v1.19.33-custom.2`. The run number increases across upstream
versions and is unchanged when rerunning the same run. If a version tag already
belongs to another commit, automatic versioning skips that number. Upstream
tags are fetched separately, since merging commits does not copy tags to a fork.

Tests run on Linux with and without `with_gvisor`, using the same
interop/concurrency exclusions as the upstream build workflow. Both packages
use MetaCubeX/go 1.26 and `with_gvisor`. macOS packaging reuses
`scripts/build-custom-macos.sh`, including ad-hoc signing and verification.
Windows uses the Makefile's `windows-amd64-v1` target for broad CPU compatibility.
Each GitHub Release contains:

- `mihomo-darwin-arm64-<version>.gz`
- `mihomo-windows-amd64-<version>.zip`
- `SHA256SUMS`

Rerunning a published version leaves it unchanged; an interrupted draft can be
completed by rerunning the workflow. Build artifacts are also retained for seven
days. Sync, tests, packaging, and publication run in one workflow, avoiding the
restriction that a push using `GITHUB_TOKEN` does not trigger another workflow's
`push` event: see [GitHub's documentation](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow).

The inherited **Build**, **Test**, and **Trigger CMFA Update** workflows are
disabled in this fork's Actions settings. Their files remain unchanged for
upstream merges. Keep them disabled: their `v*` triggers also match custom
release tags and their release tasks are intended for MetaCubeX's repository.

Automatic merge and version tag pushes use a write-enabled deploy key dedicated
to this repository, stored in the Actions secret `CUSTOM_UPSTREAM_SSH_KEY`. This also
allows upstream workflow files to be merged without requiring a personal access
token. Only sync runs and the moving `custom` tag receive the SSH key; explicitly
versioned tags do not. Automatically pushed version tags skip duplicate builds.
Release publication verifies the existing version tag and its tested commit,
so a later workflow change on the default branch does not affect publication. The
built-in `GITHUB_TOKEN` is read only during sync, tests, and packaging; only the
release job has `contents: write`.

GitHub can delay scheduled runs, and disables schedules in inactive public
repositories after 60 days. See [scheduled workflow behavior](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule).
