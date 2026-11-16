# health-analytics

`health-analytics` is a small Go daemon that periodically checks endpoints and reports the result. It supports two kinds of checks:

- **HTTP checks** request a URL and verify the status code.
- **DNS checks** send a DNS query to a resolver and verify the response code (rcode).

When a healthchecks.io callback is configured, the daemon pings it after every check: the callback URL on success, and the same URL with `/fail` appended on failure. Your monitoring service then alerts you on explicit failures and when pings are past your configured grace period.

## Use Cases

- Confirm that self-hosted services are reachable and return a healthy status.
- Confirm that a DNS resolver, answers queries for a name you care about.
- Confirm that a name does not resolve, such as a blocklisted domain or a retired hostname, by using `NXDOMAIN`.
- Confirm that DNS over TLS (DoT) works on a resolver's port 853.

## Running

The daemon reads `config.yaml` from its current working directory, so run it from the directory that contains the file.


```

The daemon checks `config.yaml` every 5 seconds. When the file changes, it reloads the config and starts, stops, or restarts only the monitors whose settings changed. If the new file is invalid, the daemon logs a warning and keeps the previous config.

Results are printed to stdout with a timestamp, for example `SUCCESS`, `FAILURE`, `ERROR`, or `WARNING`.

## Configuration Reference

The config file has the top-level key `endpoints`, which is a list of checks. It can also set global options such as `retries` that apply to every endpoint.

```yaml
retries: 1
retry interval: "5s"
endpoints:
  - url: "https://example.com"
    alias: "example"
    healthcheck callback url: "https://healthchecks.example.com/ping/<uuid>"
    refresh rate: "1m"
    expected status: [200, 204]
```

### Global Options

These keys sit at the top level of `config.yaml`, next to `endpoints`, and apply to every endpoint.

| Key | Required | Default | Description |
| --- | --- | --- | --- |
| `retries` | No | `0` | How many extra attempts to make after a failed check before reporting it. Each retry runs the same check. A failure sends `/fail` only after the last attempt fails. Must not be negative. |
| `retry interval` | No | `5s` | How long to wait between attempts, as a Go duration. Must not be negative. Only used when `retries` is greater than zero. |

With `retries` set, each check can take up to `(retries + 1) × 20s` plus `retries × retry interval`. An attempt can take up to 20s, for example an HTTP HEAD request followed by a GET, or a DoT dial followed by a read. This must be less than every endpoint's `refresh rate`, or the daemon refuses to load the config.

### Common Options

| Key | Required | Default | Description |
| --- | --- | --- | --- |
| `url` | Yes | None | The URL to request for HTTP checks, or the resolver to query for DNS checks. |
| `alias` | No | Empty | A display name used in logs. If empty, logs show the `url`. |
| `refresh rate` | Yes | None | How often to run the check, as a Go duration such as `30s`, `5m`, or `1h`. Must be greater than zero. |
| `healthcheck callback url` | No | Empty | A URL that receives a GET request after each check. On success the URL is pinged as-is. On failure `/fail` is appended to the path (for example `.../ping/<uuid>/fail`). |

### HTTP Options

| Key | Required | Default | Description |
| --- | --- | --- | --- |
| `expected status` | No | `200` to `299` | A list of status codes that count as healthy. Each value must be between 100 and 599. |

HTTP requests time out after 10 seconds.

### DNS Options

Add a `dns:` block to make an endpoint a DNS check. When this block is present, `expected status` is ignored and `url` is the resolver.

```yaml
  - url: "1.1.1.1:53"
    alias: "DNS"
    refresh rate: "30s"
    dns:
      query-name: "example.com"
      query-type: "A"
      expected-rcode: "NOERROR"
```

| Key | Required | Default | Allowed values |
| --- | --- | --- | --- |
| `dns.query-name` | Yes | None | The domain name to query, sent exactly as written. A trailing dot is optional. |
| `dns.query-type` | No | `A` | `A`, `AAAA`, `CNAME`, `MX`, `NS`, `TXT` (not case-sensitive). |
| `dns.expected-rcode` | No | `NOERROR` | `NOERROR`, `FORMERR`, `SERVFAIL`, `NXDOMAIN`, `NOTIMP`, `REFUSED` (not case-sensitive). |

### Resolver Address Formats

The `url` for a DNS check accepts these forms:

- `10.10.0.138`: plain DNS over UDP on port 53.
- `10.10.0.138:5353`: plain DNS over UDP on a custom port.
- `tls://10.10.0.138`: DNS over TLS on port 853.
- `tls://dns.example.com:8853`: DNS over TLS on a custom port.
- IPv6 addresses work in brackets, for example `[2001:db8::1]:53` or `tls://[2001:db8::1]`.

DNS queries time out after 10 seconds.

## Choosing an Expected Rcode

- Use `NOERROR` (the default) for normal monitoring. The name exists and the resolver answers.
- Use `NXDOMAIN` only when the check should pass if the name is absent. Examples include blocklist checks, retired hostnames, and split-horizon checks that should not leak internal names.
- A typo in `query-name` also returns `NXDOMAIN`, so the check would pass without testing anything. Verify the name first.
- `SERVFAIL`, `REFUSED`, and timeouts all fail the check when you expect `NOERROR`.

## Setting Up Technitium

- Plain DNS: set `url` to the Technitium IP on port 53. The forwarder protocol setting in Technitium does not affect this check, because the check talks to Technitium directly.
- DNS over TLS: set `url` to `tls://<technitium-ip>:853` and make sure Technitium has a TLS certificate for its DoT listener.
- Recursion: if Technitium's "Allow Recursion Only For Private Networks" setting is on and the daemon runs off your LAN, queries are refused, and the check fails with `REFUSED`.
- Verify first: run `dig @<technitium-ip> <name>` from the machine that runs the daemon. If `dig` gets an answer, the check should pass too.

## TLS Certificates

DoT checks verify the server certificate against the system trust store. For an IP address, the certificate must list that IP as a subject alternative name. Self-signed certificates fail with an `x509` error unless the CA is installed on the machine running the daemon. There is currently no option to skip verification.

## Behaviour Notes

- Each endpoint runs on its own schedule, and the first check runs as soon as the monitor starts.
- Callbacks are pinged after every check. Failures ping `<callback>/fail` instead of the callback URL.
- A check that fails is retried according to the global `retries` setting. Each retry waits `retry interval` before running again, and a check stops retrying as soon as it passes.
- Unknown keys in `config.yaml` are rejected at load time with an error that names the key, so a misspelled key cannot silently change a check. This also applies to Gatus-only keys such as `conditions`, `alerts`, `group`, and `client`.
- DNS checks read a single UDP response of up to 512 bytes. Responses that are truncated (the TC flag is set) are not retried over TCP. This is fine for the record types listed above when the answers are small.

## Current Limitations

These Gatus features are not supported:

- `conditions` (for example `[BODY]` and `[RESPONSE_TIME]`). DNS checks verify only the response code, not the answer records or response time.
- `alerts`, including Telegram. Use `healthcheck callback url` with healthchecks.io for alerting.
- `group`, and `client.failure-threshold`. A single failed check is reported immediately.
- DNS over HTTPS and DNS over QUIC.
