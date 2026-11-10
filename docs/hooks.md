# HOST / IP hooks

This custom build supports generic outbound API and script hooks. No hook is
enabled unless a `hooks` entry references a `hook-providers` definition.

```yaml
rule-providers:
  TargetDomains:
    type: inline
    behavior: domain
    payload: [example.com, '+.example.com']
  TargetProcesses:
    type: inline
    behavior: classical
    payload: ['PROCESS-NAME,example-app']

hook-providers:
  RegisterHost:
    type: api
    mode: async
    url: http://127.0.0.1:1234/hooks/host
    timeout: 2s

  ConfirmIp:
    type: api
    mode: sync
    url: http://127.0.0.1:1234/hooks/ip
    timeout: 2s
    success-status: 200
    on-error: block
    confirmation-ttl: 30s

hooks:
  - event: HOST
    match: OR,((RULE-SET,TargetDomains),(RULE-SET,TargetProcesses))
    providers: [RegisterHost]
  - event: IP
    match: OR,((RULE-SET,TargetDomains),(RULE-SET,TargetProcesses))
    providers: [ConfirmIp]
```

## Events and matching

- `HOST`: a DNS question or a business connection's known target hostname.
- `IP`: real business-target addresses returned by resolution or about to be
  used by a TCP connection / UDP send. Fake-IP addresses are excluded.
- Remote proxy resolution does not cause an extra local DNS lookup. If Mihomo
  has no real target IP, only `HOST` is emitted.
- Literal-IP connections have `host: ""` and no `HOST` event.

Hook conditions are independent of routing: DIRECT, GLOBAL, selected proxies
and normal rule routing all participate. Existing domain, IP, process, inbound,
network and `RULE-SET` conditions and `AND` / `OR` / `NOT` are supported.
`SUB-RULE` is a routing action and is not a hook condition. Referenced rule
providers remain live and their normal updates are visible to hooks.

Hook matching never resolves DNS. Missing information is unknown: in particular
`NOT(PROCESS-NAME,...)` does not match just because no process is known.
DNS requests do not imply a business process or a TCP/UDP business connection.
Process conditions can match later, when an intercepted business connection is
identified. `find-process-mode: off` disables additional process discovery.
The initial connection `HOST` check runs before route matching; conditions are
checked again after routing supplies additional metadata, without repeating an
already dispatched HOST notification to the same provider.

An IP condition is checked against each returned address; a provider receives
only the matching addresses. Hooks execute in configuration order. The same
provider referenced more than once receives a matching event once, at its first
matching position. An async entry is enqueued at that position; it does not
delay a following sync entry.

## Providers

Common fields:

| Field | Default | Meaning |
| --- | --- | --- |
| `type` | required | `api` or `script` |
| `mode` | `async` | `sync` or `async` |
| `timeout` | `2s` | Per-execution timeout, also bounded by the caller's deadline |
| `on-error` | `block` for sync; `continue` for async | Sync may explicitly use `continue`; async cannot block |
| `confirmation-ttl` | `30s` | Maximum reuse of an IP confirmation within the same operation / UDP association |

API fields are `url`, optional `headers`, and `success-status` (default `200`).
Requests use POST with a JSON body. Redirects are not followed. Hook API traffic
uses Mihomo's direct socket policy, without inheriting business hooks or an
environment HTTP proxy. The endpoint's success status is its acknowledgement;
Mihomo does not inspect or implement the endpoint's business logic.

Script providers use a fixed executable and arguments:

```yaml
hook-providers:
  ScriptExample:
    type: script
    mode: async
    command: /usr/local/bin/on-mihomo-hook
    args: [--format, json]
    timeout: 2s
```

The executable must exist when the configuration is parsed. On Windows, set a
Windows executable path. Event JSON is sent to stdin; event values are never
interpolated into shell commands or arguments. Exit code zero means success;
stdout/stderr are discarded. A shell can be selected explicitly using fixed
`command` / `args` if needed.

## JSON body

```json
{
  "version": 1,
  "event_id": "8ad5cc8f-17d7-41d2-86a6-f52b559e68d0",
  "event": "IP",
  "observed_at": "2026-11-10T12:00:00Z",
  "host": "example.com",
  "ips": ["192.0.2.1", "2001:db8::1"],
  "source": "resolver",
  "network": "tcp",
  "process": "example-app",
  "process_path": "/usr/local/bin/example-app",
  "outbound": "DIRECT"
}
```

Unknown optional fields are omitted. `source` is `dns`, `resolver` or
`connection`; `resolver` includes cache and hosts results without claiming a
specific cache origin. `outbound` names the routing choice (possibly a proxy
group), not a guessed final chain. Events are immutable across async retries:
both `event_id` and `observed_at` stay unchanged.

## Ordering, failure and lifetime

- Sync IP hooks complete before a DNS answer is returned, before a final TCP
  dial starts, or before the relevant UDP packet is written. DNS done during
  routing collects observations and executes hooks after releasing the route
  lock. No global route, DNS-cache or NAT lock is held across provider I/O.
- A blocking failure stops the current operation. Ordinary dial retries cannot
  bypass it. A later UDP packet may retry; a failure is never cached as success.
- Confirmations are local to an operation / UDP association and the provider,
  host, IP and port. A new TCP operation does not inherit an old confirmation.
  UDP checks the confirmation before every send and renews it on demand after
  expiry. Configure the lease to agree with the endpoint's acknowledgement.
- Configuration reload cancels old hook executions. Existing UDP associations
  use the new configuration on their next send and do not reuse old approvals.
- Async delivery uses an in-memory queue of 256 jobs and four workers. It tries
  at most three times with short backoff. Queue overflow, exhausted retries,
  shutdown and reload can lose async events; they are logged, not persisted.
- The feature handles DNS and intercepted TCP/UDP business traffic. It cannot
  act on traffic that bypasses Mihomo. Internal proxy/DNS transport and hook API
  calls are not classified as business-target events.
