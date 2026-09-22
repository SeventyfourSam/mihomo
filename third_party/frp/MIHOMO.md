# frp embedded dependency

Source: https://github.com/fatedier/frp/releases/tag/v0.62.1

The `client`, `pkg`, `server`, `assets`, `go.mod`, `go.sum`, and `LICENSE`
entries are copied from the published `github.com/fatedier/frp@v0.62.1` Go
module. The server is used by the integration test fixture. The dependency remains
under its original Apache-2.0 license.

Local patch:

- `client/service.go`: remove `os.Setenv` calls for
  `QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING` and `QUIC_GO_DISABLE_ECN`, and the
  now-unused `os` import. Importing frpc must not change mihomo's QUIC settings.
  Keep `golib/crypto.DefaultSalt = "frp"`, which the frp wire protocol requires.
- `client/service.go`: read the current controller under `ctlMu` in the reconnect
  worker, detach controllers under the same lock during shutdown, and join both
  controller shutdown and the reconnect worker before `Run` returns. This fixes
  the upstream stop/reconnect data race and prevents an old client surviving a
  configuration reload in the embedding process.
- `pkg/util/http/server.go`: remove the `net/http/pprof` import and its admin
  handlers, rejecting `PprofEnable` before opening a listener. This prevents
  process-wide default HTTP route registration merely from importing frpc.

The frps test fixture runs in a separate process, like a real remote deployment.
Upstream v0.62.1 has a server UDP channel close/send race and does not always join
its listener mux goroutines on `Close`. The server code is not linked into mihomo
and is intentionally not changed here. Client-side integration tests run with
the race detector; the fixture is built without it.

`component/frpc` provides the restricted configuration, lifecycle, logging and
TCP/TLS/yamux connector. It never enables upstream DNS overrides, admin servers,
virtual networks, plugins or visitors. The connector uses mihomo's direct socket
policy and does not inherit `http_proxy`.

When upgrading: start from a released upstream module, reapply the small patch,
check upstream package initialization for new global side effects, update the
root yamux replacement to match upstream, and run the frpc integration tests.
Do not edit the Go module cache or add local machine paths to go.mod.
