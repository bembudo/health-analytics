# Health-Analytics

`health-analytics` is a small Go daemon that periodically checks endpoints and reports the result. It supports two kinds of checks:

- **HTTP checks** request a URL and verify the status code.
- **DNS checks** send a DNS query to a resolver and verify the response code (rcode).

When a check passes and a [healthchecks.io](https://healthchecks.io/) callback is configured, the daemon pings that callback. Your monitoring service then alerts you if the pings are past your configured grace period.

## Installation

Health Analytics provides an installation script that automatically detects your system architecture and installs the appropriate binary.

### Supported Architectures

- `amd64` — 64-bit x86 systems
- `arm64` — 64-bit ARM systems
- `armv7` — 32-bit ARM systems

### Install

Run the following command from your terminal:

```bash
curl -sSL "https://raw.githubusercontent.com/bembudo/health-analytics/main/install.sh" | bash
```

The installation script downloads and installs the appropriate Health Analytics binary for your system.

Once installed, refer to the [Configuration](#configuration) section to configure your monitors.

## Use Cases

- Confirm that self-hosted services are reachable and return a healthy status.
- Confirm that a DNS resolver, answers queries for a name you care about.
- Confirm that a name does not resolve, such as a blocklisted domain or a retired hostname, by using `NXDOMAIN`.
- Confirm that DNS over TLS (DoT) works on a resolver's port 853.

## Running

The daemon reads `config.yaml` from its current working directory, so run it from the directory that contains the file.



The daemon checks `config.yaml` every 5 seconds. When the file changes, it reloads the config and starts, stops, or restarts only the monitors whose settings changed. If the new file is invalid, the daemon logs a warning and keeps the previous config.

Results are printed to stdout with a timestamp, for example `SUCCESS`, `FAILURE`, `ERROR`, or `WARNING`.

## Configuration Reference

The config file has a single top-level key, `endpoints`, which is a list of checks.

```yaml
endpoints:
  - url: "https://example.com"
    alias: "example"
    healthcheck callback url: "https://healthchecks.example.com/ping/<uuid>"
    refresh rate: "1m"
    expected status: [200, 204]
```

### Common Options

| Key | Required | Default | Description |
| --- | --- | --- | --- |
| `url` | Yes | None | The URL to request for HTTP checks, or the resolver to query for DNS checks. |
| `alias` | No | Empty | A display name used in logs. If empty, logs show the `url`. |
| `refresh rate` | Yes | None | How often to run the check, as a Go duration such as `30s`, `5m`, or `1h`. Must be greater than zero. |
| `healthcheck callback url` | No | Empty | A URL that receives a GET request after each successful check. Failures never ping it. |

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
- Callbacks are pinged only after a successful check.
- Unknown keys in `config.yaml` are rejected at load time with an error that names the key, so a misspelled key cannot silently change a check. This also applies to Gatus-only keys such as `conditions`, `alerts`, `group`, and `client`.
- DNS checks read a single UDP response of up to 512 bytes. Responses that are truncated (the TC flag is set) are not retried over TCP. This is fine for the record types listed above when the answers are small.

## Current Limitations

These Gatus features are not supported:

- `conditions` (for example `[BODY]` and `[RESPONSE_TIME]`). DNS checks verify only the response code, not the answer records or response time.
- `alerts`, including Telegram. Use `healthcheck callback url` with healthchecks.io for alerting.
- `group`, and `client.failure-threshold`. A single failed check is reported immediately.
- DNS over HTTPS and DNS over QUIC.

  ## Acknowledgements

- [healthchecks.io](https://healthchecks.io/) — For providing an excellent monitoring service and heartbeat-based monitoring infrastructure that Health Analytics integrates with.
- [Gatus](https://github.com/TwiN/gatus) — An excellent open-source monitoring project that inspired the development of Health Analytics and its approach to endpoint and DNS monitoring.

Health Analytics is an independent project and is not affiliated with either project.
