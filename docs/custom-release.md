# Custom releases

The repository's default branch is `custom`. The **Custom Release** workflow
builds macOS arm64 and Windows amd64 packages after a push to that branch. Changes
limited to documentation, the README, or issue templates do not trigger a build.
You can also run it from **Actions → Custom Release → Run workflow**, selecting
`custom`.

The workflow first runs the Go tests on Linux, both with and without
`with_gvisor`, using the same interop/concurrency test exclusions as the upstream
build workflow. It then builds both packages with MetaCubeX/go 1.26 and
`with_gvisor`. macOS packaging reuses `scripts/build-custom-macos.sh`, including
ad-hoc signing and signature verification. Windows uses the Makefile's
`windows-amd64-v1` target for broad CPU compatibility.

After both builds succeed, the workflow publishes a GitHub Release containing:

- `mihomo-darwin-arm64-<version>.gz`
- `mihomo-windows-amd64-<version>.zip`
- `SHA256SUMS`

Versions combine the nearest upstream stable tag and the source commit, for
example `v1.19.33-custom.g0123456789ab`. Release tags start with `custom-`, so they
do not match the upstream workflows' `v*` triggers. Both packages come from the
same tested commit. Rerunning a published commit leaves its release unchanged;
an interrupted draft can be completed by rerunning the workflow.

The workflow needs no additional repository secrets. Only the release job has
`contents: write`; test and build jobs have read access. It does not synchronize
or merge upstream changes.

For future upstream synchronization, call this workflow as a reusable workflow
or dispatch it explicitly after updating `custom`. A push made by another
workflow with `GITHUB_TOKEN` does not trigger this workflow's `push` event:
see [GitHub's workflow triggering documentation](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow).
