# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v3.13.1] — 2026-11-11

### Security

- The log redaction now hides the secret of an API token that Proxmox Backup Server or Proxmox Datacenter Manager creates or regenerates. Those replies name the token with `tokenid` where PVE uses `full-tokenid`, so their `value` member reached the log in plain text. A member named `value` is now redacted whenever an object it sits in has a member whose name is a secret name, which also covers a pending configuration entry such as `{"key":"cipassword","value":...}`. The `pending` member of such an entry, which holds a change not yet applied, is redacted by the same rule, so the pending configuration of a VM, a container, or a cloud-init drive no longer logs a new password either. The same two members are redacted when a member named `name` or `key` in the object holds a secret name as its value. That covers the Ceph config database, whose entries name an option such as `mgr/influx/password` in `name` and give its setting in `value`, which used to reach the log in plain text. A `value` or `pending` member with neither kind of sibling is still logged.
- A string member named `data` is now redacted at any depth of a logged body, and not only at the top. The reply to a read of an ACME DNS plugin holds the provider's credentials in such a string one level down, alone or inside a list, and it used to reach the log as sent.
- The content type that a log line gives for a body it does not parse, and every name and value in the `resp_headers` field, are now escaped and cut at 1 KiB. A header in that field keeps at most 16 values and then says how many more there were. The field holds at most 32 KiB of names and values, taken in the order of their names, and it counts the headers it leaves out under `(dropped headers)`. A server used to be able to put a direction override or a control character into the content type, and to put close to the transport's whole 10 MB header limit into every debug log line through one long header value, one long header name, or one header repeated many times.
- An error from reading a response body now goes through the same cleaner as an error from sending the request. A chunked reply with a malformed trailer line used to put that line, with any control characters and secrets in it, into the error the client returned, and into the errors from the login and second-factor decoders, the response parser, the WebSocket proxy, the batch executor, and a stream that `NewFromResponse` in `pkg/stream` builds. That error is now a permanent `HeaderError` that does not quote the line, and the words each caller wraps around it stay the same.
- The kind that a framing error reports is now the first marker in the error text, and it is cut at 1 KiB. Before, a marker later in the text could win and leave the server's bytes in front of it in the kind. The phrase that shows a bad header name or value belongs to the caller's own request is now looked for only up to the end of the marker, because a header name that an HTTP/2 server sent could hold that phrase and stop the cut.
- The phrases that turn an error into a redirect refusal, a header failure, or a permanent failure for an unsupported scheme are now read only in the text before a protocol error starts to quote the server. The debug data of an HTTP/2 GOAWAY frame is chosen by the server, so a server behind an HTTP/2 proxy could put one of those phrases there and turn a dropped connection into a permanent failure that the retry loop never repeats. That error is now a framing error, and the retry behavior for it is the same as for any other GOAWAY.
- The URLs in a refused redirect, and the URL of any error the cleaner rewrites, are now cut at 1 KiB after their query and fragment are removed. A server could otherwise answer with a `Location` header hundreds of kilobytes long and have the client repeat all of it in an error.
- The text of any cause that the cleaner keeps is now escaped and cut at 8 KiB. That covers a TLS failure that quotes a name from the server's certificate, which used to reach the error with its control characters as received. The cleaned error still unwraps to its cause, and a cause that reports a timeout still reports one through `Timeout`. The cleaned error has a `Timeout` or a `Temporary` method only when its cause has one, so `errors.As` for a `net.Error` passes over it whenever it would have passed over the cause and still finds a timeout further down the chain.
- A `*url.Error` that a transport wrapper returns inside another error, through `%w` or `errors.Join`, is now cleaned the same way as one returned on its own. Its query and fragment used to reach the error text whenever a `WrapTransport` wrapper or a retry layer wrapped the error before returning it. The words the wrapper added stay, the cleaned error still unwraps link by link to the original cause, and a nested header failure still stops the retry loop. When `errors.As` looks for a `*url.Error`, or for an interface such as `net.Error` that one satisfies, it now finds the cleaned copy of the nested error. The original error is still in the chain below the cleaned text, so code that unwraps it link by link and prints each link still prints the query.
- A login reply with a success status that asks for a second factor is now handled the same way as the reply with a 401 status. Its TFA types are escaped, and the error keeps at most 32 of them, each cut at 1 KiB. Its ticket and challenge are escaped and kept whole, because the caller sends them back to finish the login. Before, the types reached the `TFARequiredError` text exactly as the server sent them. A TFA handler set on the client receives the types the error keeps, which no real server makes more than a handful of.
- The batch executor now cleans the error for a request it cannot build. A path that does not parse used to come back in `Response.Error` whole, with any `vncticket` or `password` in its query.

### Added

- `NewTFARequiredError(ticket, challenge, types)` in `pkg/errors` builds a `TFARequiredError` with its own copy of the types slice. It escapes the types and keeps at most 32 of them, each cut at 1 KiB, and it escapes the ticket and the challenge and keeps them whole.

## [v3.13.0] — 2026-11-11

### Security

- Go's default redirect policy keeps the `Authorization` and `Cookie` headers when a redirect changes only the port or the scheme of the host, and it replays the request body for a 307 or 308. A server that answered a request with such a redirect therefore got the API token, the ticket cookie, the CSRF token, or the form body sent to another listener on the same host, in plain text when the target was `http`. Every HTTP client the SDK builds now refuses every redirect, so nothing follows a 301, 302, 303, 307, or 308 by default. That covers API requests, uploads, the ticket login, renewal, TFA, and logout requests, the `ProxyClient` ticket POST in `pkg/websocket`, and the shared client in `pkg/pool`. The clients you can give a policy through `Options.CheckRedirect` or `pool.Config.CheckRedirect` follow redirects only when you set one, and `ProxyClient` always refuses. The WebSocket dialer never followed redirects.
- The refusal happens in the transport, before `net/http` reads the `Location` header. Go parses that header before it calls a redirect policy, and when the header is malformed it fails the request with an error that quotes the whole header, including any user information, query, and fragment. The client's transport now turns every 3xx response except 304 into a permanent refusal and closes the response body, so a malformed `Location` header never reaches `net/http`. This transport sits above the one a `WrapTransport` wrapper returns, so the wrapper sees the redirect response but cannot let it through. A 304 passes through, because it answers a conditional request and tells the caller to use the copy it already holds, and the PVE API does not use conditional requests. Setting `Options.CheckRedirect` removes the refusal, and your function then decides.
- The error for a refused redirect names the target as scheme, host, and path only, and a `Location` header that cannot be parsed is not quoted. The request fails with a permanent redirect refusal and is attempted once.
- Every error from a request now passes through one cleaner. The URL in the error keeps only its scheme, host, and path, so the query and fragment of a request URL no longer appear in an error message, and neither does the target of a redirect that your own `CheckRedirect` follows to a host that is down. A message that quotes a response header the transport could not read, such as a `Location` or `Set-Cookie` line with a control byte, is replaced with an error that names only the requested URL. That error is permanent, so the client attempts the request once. The cleaning covers the SDK's own requests, the ticket login, renewal, TFA, and logout requests, `ProxyClient`, `Pool.Do`, `Pool.DoWithContext`, and the error text `pkg/batch` puts in a response. The client that `Pool.Get` returns refuses redirects before `net/http` parses them, but when `pool.Config.CheckRedirect` follows redirects, an error from a request you send directly through that client still carries what `net/http` puts in it.
- The error from a failed VNC, terminal, or tunnel WebSocket dial in `pkg/websocket` used to print the whole dial URL, and its query holds the one-time `vncticket` or tunnel `ticket` and the tunnel socket path. Callers that logged the error therefore wrote a live session ticket to their logs. `ProxyClient` now names the scheme, host, and path in that error, `Client.Connect` in the same package passes its dial error through the same cleaner, and `errors.Is` and `errors.As` still reach the wrapped cause, such as `websocket.ErrBadHandshake` or a `*net.OpError`.
- A client built with an `APIToken` that does not parse used to put the whole token, secret included, in the error its authenticator returns on every request. The error now says the token is invalid and gives the parse failure without the token.
- A protocol error that `net/http` or its HTTP/2 transport builds from response bytes no longer carries those bytes. That covers a bad status line, status code, or version, a bad or repeated `Content-Length`, an unsupported or repeated `Transfer-Encoding`, a bad trailer key, an invalid pseudo-header or header field, and the debug text of a GOAWAY frame. The error keeps its kind, such as `malformed HTTP status code`, so an operator can still tell what failed. The retry behavior for these errors has not changed, because the repeat goes to the same host with the same credentials. The cleaned error still unwraps to the original, so `errors.Is` and `errors.As` see what they saw before, and a TLS, certificate, context, or refused redirect error is never rewritten by its text, so a certificate name that holds a phrase such as `bad Content-Length` cannot change how a request is retried.
- The `url` field of a log line and the `URL` of a hook event used to carry the whole query, so a `password`, `vncticket`, or `ticket` parameter was written to the log in plain text even though the separate `query` field hid it. A scan of a CIFS share with its password in the query is one example. The URL is now logged as scheme, host, and path, without user information or a fragment, and when `LogQueryParams` is on it keeps the query with the value of each redacted parameter replaced by `REDACTED`. Redaction of headers and parameters now ignores case, and the credential names the SDK itself deals with, such as `Authorization`, `Cookie`, `Set-Cookie`, `CSRFPreventionToken`, `ticket`, and `vncticket`, are redacted whatever the configured lists say. Errors from closing a response body and from automatic ticket renewal go through the same cleaner as request errors before they are logged.
- `LogResponseBody` used to log response bodies exactly as received, which wrote the ticket and CSRF token of a login response, and the secret of a token create response, to the log. A body that parses as JSON is now logged with the value of every secret member replaced by `REDACTED`, such as `ticket`, `CSRFPreventionToken`, `password`, `token`, `secret`, the `value` of a token create response, and any name in `RedactParams` or `RedactHeaders`. A body that does not parse as JSON, or whose top level is a string or another scalar, is no longer logged, and a string in the top-level `data` member, and every string item of an array at any depth, is replaced by `REDACTED`, because nothing names it as harmless. The log line carries its length and content type instead. The body is cut at `MaxBodyBytes` after redaction, and a cut body ends with a marker. `LogBody` is reserved and the client does not log request bodies.
- The PVE boolean, integer, and float decoders, and the parsers that report a JSON decode failure, used to put bytes the server chose into error text as received. A broken or hostile server could use that to put control characters, escape sequences, or text direction overrides into a log. The decoders now show the unsupported value as a quoted literal of at most 64 bytes followed by an ellipsis, and decode errors have control characters, direction overrides, and invalid bytes escaped, with the error chain kept. Two sources remain outside the SDK. Setting `GODEBUG=http2debug=1` makes the standard library log request headers, and the standard library logs `Unsolicited response received on idle HTTP channel` through the global logger. The SDK cannot filter either one.
- The text of an error that the PVE API returns used to carry the server's bytes exactly as sent, so a broken or hostile server could clear a terminal, rewrite the line with a carriage return, or hide text behind a direction override. `ParseAPIError` now escapes, in the message, the `errors` keys and values, the file name, and the TFA types, every control character except newline and tab, which includes escape, carriage return, NUL, and DEL, as well as the direction controls and other format characters such as U+202A to U+202E, U+2066 to U+2069, U+200E, U+200F, and U+061C, the line and paragraph separators, and invalid UTF-8. Newlines stay, because PVE writes some messages on several lines, so a program that prints an error on one line should quote it. The same escaping applies to the error the client builds from a reply with a success status whose body has `success` of 0 and a `message`. A success reply that carries an `errors` map but no such `message` is still returned as a normal response, with the map in `Response.Errors`, and the client does not turn it into an error. Each text field of an error, meaning the message, every `errors` key and value, and the file name, is cut at 1 KiB of escaped text and ends with `...(truncated)` when it was cut, whether the reply is JSON or not, so a 3 MB message no longer produces a 3 MB error. An error also keeps and prints at most 32 entries of the `errors` map, in the order of their names, and at most 32 TFA types, and says `and N more` for the rest, and the text of `Error()` is never longer than 8 KiB, so a reply with 500,000 TFA types or 200,000 `errors` keys no longer produces an error of several megabytes. `NewEnvelopeError` in `pkg/errors` is the one constructor both paths use.
- The secret names in the log redaction are now matched as fragments and not only as exact names. A query parameter, header, or JSON member is redacted when its name, ignoring case, holds `password`, `passwd`, `secret`, `token`, `ticket`, `key`, `authkey`, `recovery`, `challenge`, `csrf`, `cookie`, `credential`, or `private`, or starts or ends a word with `otp` or `totp`. That covers the recovery codes and challenge of a TFA call, `corosync_authkey`, `cipassword`, and `encryption-key`, which were logged as they came when `LogResponseBody` was on. The match errs toward redacting too much, so a name such as `tokenid` is redacted as well. Ordinary PVE options such as `keyboard` and `hotplug` stay readable. Names you list in `RedactParams` and `RedactHeaders` still match exactly, and the headers, the query field, the url field, and the body all use the same matcher.
- `LogResponseBody` no longer parses a response body larger than 64 KiB. Decoding and encoding a body to redact it took many times the size of the body in memory, and a 36 MB reply used about 4 GB. A larger body is now logged by its length and content type only, the same as a body that is not JSON.
- The `resp_headers` log field now passes `Location` and `Content-Location` through the same redaction as the `url` field, so user information and a fragment are dropped and a secret query value is replaced. A value that is not a URL is replaced by `REDACTED`. Every other response header value is logged with control and direction characters escaped.
- A logged response body now has control and text direction characters escaped, so a direction override in a JSON member no longer reaches the log as itself.
- The error that a failed WebSocket read returns, from the event client and from a proxy session, carried the close reason the server chose, as received. It is now escaped and cut at 256 bytes, and `errors.As` still finds the `*websocket.CloseError`.

### Added

- `Options.CheckRedirect` sets what the client does with a redirect a server sends, with the same meaning as `http.Client.CheckRedirect`. A function that returns nil follows the redirect, and one that returns an error fails the request with it. Leaving it nil refuses every redirect, and setting it removes the transport's refusal so that your function decides. Unlike Go's own policy, a function that returns nil for every request never stops, so count `len(via)` or use one of the two helpers below.
- `FollowRedirects(max)` returns a `CheckRedirect` policy that follows at most `max` redirects to any scheme, host, and port, and `FollowSameOrigin(max)` follows at most `max` redirects that keep the scheme, host name, and port of the original request. Both fail with a `RedirectError` past their limit or, for `FollowSameOrigin`, for any other target. Because `FollowRedirects` follows to any host, Go copies every header other than the credential headers it strips for a different host, such as the CSRF token, to the new host, so use it only with a server whose redirects you trust.
- `pool.Config.CheckRedirect` does the same for the shared client in `pkg/pool`, which refuses redirects when it is nil.
- `ErrRedirectRefused` and `RedirectError` describe a refused redirect. `errors.Is(err, ErrRedirectRefused)` matches, and `RedirectError` carries the status code and the two URLs, the one requested and the one the server asked for, with the user information, query, and fragment removed.
- `Permanent(err)` marks an error so the retry loop does not attempt the request again, and `IsPermanent(err)` reports the marker. A `WrapTransport` wrapper returns `Permanent(err)` from `RoundTrip` when it refuses a request on purpose. An error type of your own gets the same treatment when it has a `Permanent() bool` method that returns true. `IsPermanent` looks through every wrapped error, including each member of an `errors.Join`, and answers true when any of them is permanent, so one that answers false does not hide another that answers true.

### Changed

- A client that relied on following redirects now fails with a `RedirectError` and needs `Options.CheckRedirect`. The case most likely to matter is a front proxy or load balancer that answers a plain `http` request with a 301 to the `https` address of the same host. Set `CheckRedirect: pve.FollowRedirects(1)` for that case, or write a function that compares the target's scheme and host with the request's. PVE does not redirect its API, so a client that talks to PVE directly is unaffected. The new API is additive, which is why this ships as a minor release, but the default behavior change is not strictly compatible.
- The `http.Client` that `Pool.Get` returns now has a wrapper as its `Transport`, not the `*http.Transport` the pool created. Code that type-asserts it to tune the transport should use `SetMaxConnections` and `SetMaxConnectionsPerHost` instead. Replacing the client's `Transport` also replaces the refusal.
- An error message shows the URL of the request without its query or fragment. A caller that read the query of a failed request from the error text needs to read it from the request it sent.
- A response with a header line the transport cannot read is attempted once. Before this release the client attempted such a GET up to four times and put the raw header line in the error.
- The retry loop does not retry an error marked with `Permanent`, which includes a refused redirect. Before this release the client attempted a request again when a `WrapTransport` wrapper refused it, until the retry count ran out, which is four attempts for a GET with the defaults. A connection failure that a wrapper marks permanent still comes back as a `ConnectionError`, after one attempt.
- A followed redirect to a scheme the transport cannot speak, such as `ftp`, is attempted once. Before this change the client attempted the request four times and reported the failure after the last one. The message is the same, and it can only happen when `Options.CheckRedirect` follows redirects.

## [v3.12.0] — 2026-11-09

### Added

- `TicketAuthenticator.SetHTTPClient` replaces the `*http.Client` the ticket authenticator sends login, renewal, TFA, and logout requests with. It is safe to call while those requests run, and it ignores a nil client.
- `Options.WrapTransport` is an opt-in hook for the HTTP transport. `NewClient` calls it once with the `*http.Transport` it built, which already carries the TLS configuration, dial function, proxy, and timeouts, and installs the returned `http.RoundTripper` as the client's transport. The wrapper sees every request, including the ticket login and the forced re-login, it sees each retry attempt separately, and it sees `WithHost` requests addressed to the override host and upload streams unbuffered, but the WebSocket APIs bypass it. A wrapper that returns nil, or a typed nil such as `(*http.Transport)(nil)`, makes `NewClient` fail with the new `ErrWrapTransportNil`, which `errors.Is` matches from `pkg/client`. `Close` closes the idle connections of the base transport, so a wrapper that owns further transports should implement `CloseIdleConnections`.

### Changed

- The `pool.Pool.SetMaxConnections` and `SetMaxConnectionsPerHost` docs now say that the setters change the shared transport in place, so they must be called before the pool's transport sends its first request. The setters behave as in earlier releases, `Get` still returns the same `*http.Client` for the life of the pool, and the setters are safe to call concurrently with each other and with `Get`, `Put`, `Stats`, `IsHealthy`, and `Close`.
- `pool.New` copies the `Config` it is given. The setters no longer write the caller's `Config`, and pools built from the same `Config` no longer share it.

### Fixed

- `SetKeepAlive` no longer writes `MaxIdleConns` and `MaxIdleConnsPerHost` on a transport that requests are using, which was a data race with `net/http` in earlier releases. Without a `WrapTransport` wrapper, it installs a tuned clone of the transport for later requests and logins, and it closes the idle connections of the transport it replaced in the background without waiting for them. Requests already in flight finish on the replaced transport, and `Close` also closes the idle connections of the most recently replaced transports. With a wrapper, `SetKeepAlive` records its value and changes no transport, because the wrapper keeps the base it was handed, so a wrapper that needs other idle limits sets them on the base or on its clone inside the hook.
- `SetTimeout` no longer writes `http.Client.Timeout` on the client that requests are using, which was a data race in earlier releases. It installs a copy of the `http.Client` with the new timeout and hands that copy to the ticket authenticator, so logins use the new timeout too. The timeout applies to each HTTP attempt rather than to a whole SDK request, so an attempt already in flight keeps the timeout it started with, while a later retry, or the resend after a forced re-login, uses the new one.
- `pool.Pool.Close` closes idle connections after releasing the pool's lock, because a connection's `Close` may call back into the pool, which deadlocked in earlier releases. It still closes the idle connections of the transport the pool created when a caller has wrapped or replaced the shared client's `Transport` through `Get`, and it calls the replacement's `CloseIdleConnections` method when it has one.
- The client's `SetLogger`, `SetLogConfig`, `AddLogHook`, `SetMetrics`, and `SetTFAHandler` are now safe to call while requests are in flight, where earlier releases wrote fields that every request reads without a lock, as they did with the internal retry count and delay. Each log line or round of hooks uses a logger, configuration, and hook list that were set together, so a request that overlaps a logging setter may log its start and its response under different settings. `SetLogConfig` and `GetLogConfig` now copy the redaction lists, and a request records its whole lifecycle on the metrics collector in effect when it started, so replacing the collector mid-request leaves the active-connections gauge balanced.

## [v3.11.0] — 2026-10-05

### Added

- Bindings refreshed to the current upstream API specifications, fetched 2026-10-04: Proxmox VE 9.2 (pve-docs 9.2.13, pve-manager 9.2.21) and Proxmox Backup Server 4.2.7. PVE now covers 680 method-operations (was 678), and the PBS count is unchanged. Proxmox Datacenter Manager stays at 1.1.7 because no newer release has shipped.
- Ceph health mutes: `Cluster().ListCephHealthMute` (`GET /cluster/ceph/health-mute`) lists the muted health checks, and `Cluster().UpdateCephHealthMute` (`PUT /cluster/ceph/health-mute/{code}`) mutes or unmutes a single check. A muted check stops counting towards the cluster status but stays visible. `Value` chooses between muting and unmuting, `Ttl` sets an expiry such as `2h`, and `Sticky` keeps the mute in place when the check gets worse. The list response is `[]json.RawMessage`, where each entry carries `code`, `sticky`, and optionally `summary` and `ttl`.

### Changed

- The `Force` flag on both Ceph restart-bulk endpoints now documents that muted checks, and checks known to be harmless during a rolling restart, never block the restart, and that the OSD map flags are evaluated only for OSD restarts.
- `Hotstandby` on the Ceph MDS create params is marked deprecated, because Ceph removed the setting in 14.1.1.
- The PVE `POST /nodes/{node}/vzdump` permission text now states that `stop` requires `Sys.Modify` on `/nodes/{node}`. The three PBS tape backup and restore endpoints name `/tape/device/{drive}` in their permission text instead of `/tape/drive/{drive}`.
- PBS network interfaces accept `auto` as a `method` and `method6` value. The `put-rate-limit` deprecation note on the PBS S3 shapes now points to `limit-active-requests`.
- The PBS 4.2.7 spec wraps many list item schemas in `allOf`. The merged fields are identical to the 4.2.5 fields, and those items were already generated as `json.RawMessage`, so none of the generated types change.

## [v3.10.1] — 2026-09-24

### Fixed

- The retry loop no longer retries a request whose server certificate the client refused. A pinned fingerprint that does not match, an unknown fingerprint with no way to approve it, and a chain, hostname, or validity failure from standard verification all come back after one attempt, where they used to cost four handshakes and about six seconds of backoff. The error keeps its usual `request failed after 1 attempt(s): ...` wrapping and its verifier sentinel, so callers that match either still see them. One message changes: a certificate that a `VerifyFingerprintCallback` or `ManualVerifyCallback` rejects now surfaces that attempt's own error, such as `unknown certificate fingerprint (manual verification required)`, where the fourth attempt used to report the cached rejection as `certificate fingerprint is not trusted`. A handshake that timed out or was cut off is not a refusal of the certificate, and it stays retryable.

## [v3.10.0] — 2026-09-03

### Added

- Bindings refreshed to the current upstream API specifications, fetched 2026-09-03: Proxmox VE 9.2 (pve-docs 9.2.4, pve-manager 9.2.11), Proxmox Backup Server 4.2.5, and Proxmox Datacenter Manager 1.1.7. PVE now covers 678 method-operations (was 675); PBS and PDM counts are unchanged.
- Ceph rolling restarts: `Cluster().CreateCephRestartBulk` (`POST /cluster/ceph/restart-bulk`) restarts every MON, MGR, MDS, or OSD daemon in the cluster one at a time, and `Nodes().CreateCephRestartBulk` (`POST /nodes/{node}/ceph/restart-bulk`) does the same for one node's OSDs with per-OSD `noout` handling and checkpointed `Resume`. Both take `DryRun`, `OnlyOutdated`, `Force`, and `Timeout`, and both return the task UPID.
- `Nodes().ListCephReleases` (`GET /nodes/{node}/ceph/releases`) lists the known Ceph releases with `available`, `is-default`, and `unsupported` flags for the node.
- Journal filters on `Nodes().ListJournal` for both PVE and PBS: `Priority` (a level or `LOW..HIGH` range), `Service` (syslog identifier glob), `Unit`, `Kernel`, `Structured`, and, with `Structured`, `Identifiers` and `Units` to also return the distinct identifiers and units present. The response stays `[]string`; a structured request returns one object per entry, which callers decode from `GetRawCtx` on the same path.
- PBS S3 endpoint configuration gains `LimitActiveRequests`, `LimitPassiveRequests`, and `UseNodeProxy` on the create, update, and read shapes. The older `put-rate-limit` family is now documented as deprecated upstream.
- QEMU `net[n]` accepts `host-tunnel`; firewall `comment` parameters carry the `pve-fw-comment-spec` format; the subscription key pattern accepts `arm-` keys.

### Changed

- `Cluster().ListOptions` returns a typed `ListOptionsResponse` struct instead of a `json.RawMessage` alias, now that the spec declares the shape of `GET /cluster/options`. The declared shape is wrong for eleven fields, which the generator corrects: PVE returns the parsed `datacenter.cfg`, so `crs`, `ha`, `migration`, `next-id`, `notify`, `replication`, `tag-style`, `u2f`, `user-tag-access`, and `webauthn` are `json.RawMessage` objects keyed by sub-option and `registered-tags` is `[]string`. A caller that decoded the old raw message by hand can drop that step; one that passed it on untouched decodes the struct's fields instead. `docs/scalars.md` records the live shapes.
- `cmd/pvegen` flattens `allOf`/`oneOf` parameter and response schemas. PVE 9.2 (pve-ha-manager 5.2) declares `POST /cluster/ha/rules` and `PUT /cluster/ha/rules/{rule}` this way, one variant per rule type discriminated by `type`; without the change both params structs would have lost every field. The flattened structs are field-for-field identical to before. Where variants describe one field differently, the doc comment now carries every description labelled by rule type, so `Affinity` explains both the node-affinity and the resource-affinity meaning.
- `cmd/pvegen` gains a per-dialect `returnsPropertyOverrides` table for correcting a single property of an otherwise sound response schema, alongside the existing whole-schema `returnsOverrides`. PBS gains its first `returnsOverrides` entry: the 4.2 spec marks `GET /nodes/{node}/journal` as a streaming `DOWNLOAD` with a null return, but the body is still the standard envelope around an array of lines, so `ListJournalResponse` stays `[]string` rather than disappearing.
- `cmd/pvegen` now fails when an override table entry matches no endpoint in the spec, or when a property override names a property the spec no longer declares, instead of leaving the override silently dead after an upstream rename.
- Generated response types carry the spec's own description of the response as a second doc line. The journal response on PVE, PBS, and PDM uses it to state that a `Structured` request returns objects the `[]string` type cannot decode, and to point at `GetRawCtx`.

## [v3.9.3] — 2026-09-03

### Added

- `docs/scalars.md` documents the tolerant scalar types `PVEBool`, `PVEInt`, and `PVEFloat`, when a pointer form is needed, and the response fields whose declared type in `_data/apidoc.json` does not match the payload. The sharpest case is `osdid` in `GET /nodes/{node}/disks/list`, which arrives as the number `-1` for a disk backing no OSD and as the string `"0"` when it does, both in one response. The README now points to the guide.

### Changed

- The Makefile exports `GOFLAGS=-mod=readonly` for every Go command it runs. Go switches to vendor mode on its own whenever `vendor/modules.txt` exists, so a machine that had run `go mod vendor` resolved against vendored copies while CI used the module proxy, and `go list -m -u` stopped reporting available upgrades. Naming the mode pins both to the same resolution path and keeps a build from rewriting `go.mod` or `go.sum`.
- golangci-lint in CI moved to v2.13.2, matching the pin in the CPI release.
- Dependencies audited on 2026-09-03: every direct and indirect module is at its latest release and none is deprecated or archived. No version changed.

## [v3.9.2] — 2026-08-23

### Changed

- The Go requirement moved to 1.27.0. The separate `toolchain` directive
  is gone: the `go` line now carries both the minimum and the toolchain.
- Deprecated zero-valued fields dropped from the `tls.Config` and
  `http.Transport` literals: `Rand`, `NameToCertificate`,
  `PreferServerCipherSuites`, and `SessionTicketKey` on the former,
  `Dial` and `DialTLS` on the latter. Every one was set to its zero value
  or, in the case of `PreferServerCipherSuites`, to a value the standard
  library has ignored since Go 1.18, so behavior is unchanged. They were
  left over from a period when `exhaustruct` required naming every field,
  and `tls.Config.Rand`'s deprecation in Go 1.27 turned them into build
  noise.
- golangci-lint in CI moved to v2.13.1; v2.12.2 is built with go1.26 and
  refuses to load a config targeting Go 1.27.

## [v3.9.1] — 2026-08-23

### Fixed

- Ticket-based login failures now preserve the full cause chain. A 5xx
  from `POST /access/ticket` previously surfaced as the bare
  `ErrAuthenticationFailedNoTicket` sentinel: `Authenticate`,
  `RefreshForce`, and the TFA completion consumer discarded the real
  error carried in `AuthResult.Error`, so callers could not see the HTTP
  status or match `errors.Is(err, ErrServer)`. The sentinel still
  matches; the chain now survives behind it.
- A failed auto-login no longer disables auto-login for the life of the
  client. The attempt marker was set before authenticating and never
  cleared on failure, so every later request skipped the login, went out
  unauthenticated, and surfaced a differently shaped error from the
  401-retry path. Each request now retries the login and surfaces the
  same `auto-login failed` prefix.
- Two connection-lifecycle races that the standard library reports as
  bare, chainless sentinels now surface as the typed `ConnectionError`:
  the server closing an idle keep-alive connection between pickup and
  write (`http: server closed idle connection`), and the pool handing
  out an already-closed connection (`net.ErrClosed`). Both previously
  escaped as anonymous string wraps that no `errors.As` or `errors.Is`
  check could classify. Retry behavior is unchanged: idempotent requests
  still ride the retry loop; only what surfaces on exhaustion changed.

## [v3.9.0] — 2026-08-21

### Added

- Size-aware multipart file uploads stream through an `io.Pipe` with the
  exact body length precomputed, so a large image upload no longer
  buffers the whole file in memory and the request always carries an
  explicit `Content-Length` (PVE's proxy rejects chunked
  transfer-encoding with a 501). New public API: `client.NewSizedReader`,
  `client.WithHost`, `client.ErrHostOverrideFingerprint`.
- `Options.DialContext` and `Options.Proxy` open a seam for reaching a
  PVE host through a jump host, bastion, or tunnel. Both are opt-in and
  nil by default; a client that sets neither builds byte-identical
  transport behavior to before.

### Fixed

- The retry middleware captures a request body only when the request is
  actually retry eligible with a nonzero retry budget, instead of
  re-buffering (and overwriting `Content-Length` on) streamed bodies.
- The 401 re-auth path no longer replays a request whose drained body
  cannot be rewound; it surfaces the original 401 instead of sending
  zero bytes against a declared `Content-Length`.

## [v3.8.6] — 2026-08-20

### Added

- The project now carries its license in the repository: the Apache
  License, Version 2.0, in `LICENSE`, with the copyright notice in
  `NOTICE`. The README already pointed at a `LICENSE` file; now it
  exists.
- `SECURITY.md` documents how to report a vulnerability privately,
  the response timelines to expect, and the coordinated disclosure
  window.

### Changed

- `CONTRIBUTING.md` rewritten to match how the project actually works:
  pull requests target `main`, generated bindings are regenerated via
  `pvegen` and verified with `make verify-generated`, and releases are
  tag driven. It previously described an MIT license and a `develop`
  branch, neither of which this project uses.

## [v3.8.5] — 2026-08-18

### Changed

- `go.uber.org/multierr` bumped to v1.11.0.
- A dedicated security workflow now runs `govulncheck`, `gosec`, and
  `trivy` in CI, so every push gets the same scans that gate a release.

## [v3.8.4] — 2026-08-18

### Changed

- The Go toolchain requirement moved to 1.26.6 to pick up standard
  library security fixes.
- Dependency bumps: `go.uber.org/zap` to 1.28.0, `golang.org/x/term`
  to 0.45.0, `golang.org/x/sys` to 0.47.0.

### Fixed

- The DNS retry test no longer depends on the machine's resolver
  behavior, so the suite passes in restricted network environments.

## [v3.8.3] — 2026-08-04

### Added

- `Client.GetBytesCtx` returns a response body verbatim, without decoding
  it. Every other request path parses the body as the API's JSON envelope,
  which is right for the whole API bar a handful of endpoints that answer
  with something else:
  `/nodes/{node}/storage/{storage}/file-restore/download` returns the
  bytes of the file being restored. Those bodies previously failed the
  decode before the caller could see them, so no consumer could download
  a file out of a backup snapshot at all. A non-2xx status is still an
  error, and its body is still parsed for the API's complaint, so error
  reporting is unchanged. Transports implemented outside this package are
  unaffected: the capability is an optional interface the built-in
  transport satisfies, not a new method on `HTTPClient`.

## [v3.8.2] — 2026-07-20

### Fixed

- Failures to establish a connection are now terminal instead of being
  retried. A failed DNS lookup or a failed TCP dial (refused, timed out,
  no route) means the request never reached the server, so repeating it
  can only reproduce the same failure at the same cost. Previously an
  unreachable host was dialed four times, so a host that silently drops
  SYNs — a firewall, or a hostname pointing at a reverse proxy that does
  not serve the API port — took four full request timeouts to report an
  error. Such failures now surface immediately as a typed
  `ConnectionError` carrying the host and port that were dialed.
  Transport errors from later phases (a connection dropped mid-response)
  are unchanged and still retry.
- Retry backoff now honors context cancellation. It previously slept with
  `time.Sleep`, so a canceled request — a `context` deadline, or Ctrl-C in
  a CLI built on this library — was held open for the remainder of the
  backoff instead of returning promptly.

## [v3.8.1] — 2026-07-09

### Fixed

- API-token Authorization headers now use the correct separator per
  product. PBS and PDM servers require `TOKENID:TOKENSECRET` (colon),
  while PVE uses `TOKENID=SECRET` (equals). The client now keys the
  separator off the token header name: `PBSAPIToken` and `PDMAPIToken`
  produce `:` separators, all others default to `=`.
- `ParseAPIToken` now accepts either separator style (`=` or `:`) when
  parsing token strings, making it tolerant of varied input formats.

## [v3.8.0] — 2026-07-09

### Fixed

- PDM `GET /pve/remotes/{remote}/updates` (`pdm/pve.ListRemotesUpdates`)
  now decodes the endpoint's real object body. The hand-authored
  returns override previously declared an array of opaque objects, so
  the generated binding failed against a real server's response. The
  schema is transcribed from PDM's `pdm-api-types` `RemoteUpdateSummary`:
  `nodes` (map of node name → per-node update summary), `remote-type`,
  `status`, and optional `status-message`.

### Changed

Breaking correction — compile-time break for consumers of the
previously mis-generated symbol, which could not decode real data:

- `pdm/pve.ListRemotesUpdatesResponse` redefined from an array alias to
  a struct (`Nodes json.RawMessage`, `RemoteType string`,
  `Status string`, `StatusMessage *string`).

### Added

- Runtime test coverage for the generated nil-data decode branches over
  HTTP: tolerant endpoints (`{"data": null}` or a missing `data` key
  return a zero-value response) and strict endpoints (nil data is a
  hard "empty data" error).

## [v3.7.0] — 2026-07-09

### Fixed

- `cmd/pvegen` gained per-endpoint returns-schema overrides
  (`returnsOverrides`, keyed `"VERB /path"` per dialect). Eleven PDM
  endpoints whose apidoc declares `returns: null` but which return data —
  `GET /nodes`, `GET /nodes/{node}/journal`,
  `GET /auto-install/prepared/{id}`, the PVE/PBS remote
  `apt/repositories` proxies, `GET /pve/remotes/{remote}/options`,
  `/updates`, `/cluster-nextid`, `/nodes/{node}/config`, and the qemu/lxc
  `/pending` endpoints — now generate real response types instead of
  discarding the body.
- Two copy-pasted PDM returns schemas corrected:
  `GET /pve/remotes/{remote}/nodes/{node}/subscription` now decodes
  subscription info (was the node-status shape), and
  `GET /pve/remotes/{remote}/tasks/{upid}/log` now decodes an array of
  `{n, t}` log-line objects (was the task-status shape).
- PDM-native `GET /nodes/{node}/tasks/{upid}/log` (`ListTasksLog`) now
  decodes as an array of `{n, t}` line objects; the apidoc documents a
  single element's shape without the wrapping array.
- Endpoints whose returns schema is marked `optional` now tolerate an
  empty response body instead of erroring. Fixes PDM
  `UpdateUsersToken` with `regenerate=false` and PBS
  `ListAccessTfaWebauthn`, which share the same optional-returns pattern.
- `json.RawMessage` and `[]json.RawMessage` request fields are now sent
  as their compact JSON text as single form values. Previously the
  marshal-to-map round-trip mangled them into Proxmox comma
  option-strings or Go `%v` map formatting, so servers expecting nested
  JSON rejected or misparsed the request. Affects PDM
  `auto-install/prepared`, `sdn/vnets`, `sdn/zones`,
  `subscriptions/bulk-assign`, and PVE `cluster/sdn/fabrics/*`.
- Numbered-slot properties in returns schemas (`dev0`–`dev255`, `mp*`,
  `net*`, `unused*`, `scsi*`, and the other guest-config slot families)
  are now treated as implicitly optional and generate pointer fields
  with `omitempty`. Sparse PDM remote LXC/QEMU guest configs no longer
  fabricate hundreds of empty values on re-marshal.

### Changed

Breaking corrections — compile-time breaks for consumers of the
previously mis-generated symbols, which carried no usable data:

- Eleven PDM service methods changed signature from `(ctx, ...) error`
  to `(ctx, ...) (*Response, error)` (the returns-null endpoints above).
- `pdm/nodes.ListTasksLogResponse` redefined from a task-status struct
  to a slice of log-line objects.
- Roughly 1,224 fields in `pdm/pve` LXC/QEMU config response structs
  changed from value types to pointers with `omitempty`.
- `pdm/access.UpdateUsersTokenResponse.Value` changed from `string` to
  `*string` (absent when the token secret is not regenerated).

## [v3.6.0] — 2026-07-08

### Added

- **Proxmox Datacenter Manager support**: generated typed bindings for the
  full PDM JSON API (327 method-operations, PDM 1.1.6) under `pkg/pdm/*` (`access`,
  `autoinstall`, `ceph`, `config`, `nodes`, `pbs`, `ping`, `pve`, `remotes`,
  `resources`, `sdn`, `subscriptions`, `version`), produced by
  `cmd/pvegen --dialect pdm` from the vendored `_data/pdm-apidoc.json`. The
  `/pve` and `/pbs` trees are PDM's proxied per-remote operations against
  managed instances, including cross-cluster guest migration. The
  `/auto-install` namespace is emitted as package `autoinstall` (a hyphen is
  not a legal Go package name).
- `pkg/pdm.NewClient` / `pkg/pdm.DefaultOptions`: a preset over `pkg/client`
  that applies the PDM wire defaults — port 8443, `PDMAPIToken`
  Authorization prefix, `PDMAuthCookie` ticket cookie. Everything else (TLS
  pinning, retries, logging) is the shared client, unchanged.
- `cmd/pvegen --dialect pdm` selecting the PDM spec dialect.

## [v3.5.0] — 2026-07-07

### Added

- Tolerant `client.PVEInt` type for response integer fields the upstream
  API encodes inconsistently (native number or quoted string), following
  the `PVEFloat` pattern from v3.2.5.

## [v3.4.0] — 2026-07-07

### Changed

- **Module renamed** from `github.com/fivetwenty-io/pve-apiclient-go/v3` to `github.com/fivetwenty-io/proxmox-apiclient-go/v3` — the library now covers more than one Proxmox product. Versions up to v3.3.1 remain published under the old path; see MIGRATION.md for the one-line consumer migration. The TOFU fingerprint cache moved to `~/.config/proxmox-apiclient-go/fingerprints.json` (the legacy location is still read if the new one does not exist).

### Added

- **Proxmox Backup Server support**: generated typed bindings for the full PBS JSON API (346 endpoints) under `pkg/pbs/*` (`access`, `admin`, `config`, `nodes`, `tape`, `status`, `version`, `ping`, `pull`, `push`), produced by `cmd/pvegen --dialect pbs` from the vendored `_data/pbs-apidoc.json`. The HTTP/2 chunk-protocol endpoints (`/backup`, `/reader`) are intentionally excluded.
- `pkg/pbs.NewClient` / `pkg/pbs.DefaultOptions`: a preset over `pkg/client` that applies the PBS wire defaults — port 8007, `PBSAPIToken` Authorization prefix, `PBSAuthCookie` ticket cookie. Everything else (TLS pinning, retries, logging, ticket login) is the shared client, unchanged.
- `cmd/pvegen --dialect {pve|pbs}` flag selecting spec dialect, naming overrides, and output/import roots. Spec loading was also hardened: boolean `leaf`/`allowtoken`/`optional` encodings no longer break parsing.

## [v3.2.9] — 2026-06-22

### Added

- `Client.Close()` and `HTTPClient.Close()` release the response-cache cleanup goroutine and close idle HTTP connections. `Close` is idempotent and safe to call more than once.

### Fixed

- The response cache no longer panics on a second `Close()`; the cleanup channel is now closed exactly once.
- Each `Stream` no longer leaks a background goroutine. An inert metrics collector was started per stream and never stopped when the stream was read to EOF; it produced no observable output and has been removed.
- The connection pool's active-connection counter no longer underflows when `Put` is called without a matching `Get` (or twice for one client); the counter is clamped at zero.
- A `401` carrying a static API token is no longer retried. Because API tokens cannot be re-issued by the client, the previous refresh-and-retry replayed the same rejected token; the original `401` now surfaces after a single request. Ticket-based authenticators still refresh and retry as before.
- A malformed body on a `2xx` batch response is now reported as a failure instead of being silently counted as success. An empty body (including `204 No Content`) remains a success.

### Changed

- The unexported mutex was moved off the exported `pool.Stats` and stream metrics value types into private wrappers, so callers can copy a returned snapshot without copying a lock. The exported struct fields are unchanged.
- The unused TLS `Verifier` mode machinery and its dead error sentinels were removed. The behavior is unchanged: every non-`None` `SSLVerifyMode` performs full certificate-chain and hostname verification (fail-secure), which is now documented on the type.

### Internal

- Raised hand-written package test coverage (notably `internal/ssl`, and the cloudinit, network, storage, qemu, and client bindings) with behavior-focused tests, and added regression tests for each fix above.
- Tightened the lint configuration (targeted `varnamelen` short-name allowances and a `recvcheck` exclusion for the `MarshalJSON`/`UnmarshalJSON` pair) and resolved the substantive findings. `go vet`, `staticcheck`, `golangci-lint`, and `go test -race` pass clean.

## [v3.2.8] — 2026-06-22

### Fixed

- Integer request parameters of 1,000,000 or greater are no longer transmitted in scientific notation. The generated bindings build the request body by marshaling the typed `*Params` struct to JSON and unmarshaling into `map[string]interface{}`; the default `encoding/json` decode turned every number into `float64`, and the form encoder's fallback (`fmt.Sprintf("%v", float64)`) rendered any value `>= 1e6` as e.g. `1.048576e+06`, which PVE rejects. This silently broke realistic calls such as `bwlimit=1048576` on backup/vzdump, all UNIX-epoch parameters (`expire`, task `since`/`until`, firewall-log `since`/`until`), and `nf-conntrack-max`. As with the booleans of v3.2.4 and numbers of v3.2.5, this completes the request-side counterpart to those response-side fixes.

### Changed

- The generator (`cmd/pvegen`) now decodes request params with a `json.Decoder` configured via `UseNumber()`, so integers reach the form encoder as `json.Number` and keep their exact digits. The form encoder (`internal/http`) gained `json.Number`, `float64`, and `float32` cases that emit plain decimal notation (never an exponent), which also hardens any hand-written body map that still decodes without `UseNumber`. Regenerated bindings change only the param-decode line in each method; no exported symbols changed and encoding of every other type is identical.

## [v3.2.5] — 2026-06-03

### Fixed

- Response numbers now decode the JSON encodings the Proxmox VE API emits. As with booleans in v3.2.4, PVE renders documented numbers inconsistently — most notably the pressure-stall (PSI) metrics on container and VM status (`pressurecpusome`, `pressureiofull`, …) arrive as JSON strings rather than numbers — which caused typed status responses (for example `ListLxcStatusCurrent`) to fail with `cannot unmarshal string into Go struct field ... of type float64` against real payloads. A new tolerant `client.PVEFloat` type accepts both the numeric and string forms (and an empty string as `0`) and marshals back out as a native JSON number.

### Changed

- The generator (`cmd/pvegen`) now emits `*client.PVEFloat` for floating-point fields in response structs; request parameter structs keep plain `float64` so query encoding is unchanged. Regenerated bindings retype 24 response float fields. `PVEFloat` has an underlying type of `float64` and a `Float()` accessor; call sites reading these fields convert with `float64(*field)`, `field.Float()`, or — for a pointer — the direct conversion `(*float64)(field)`.

## [v3.2.4] — 2026-06-03

### Fixed

- Response booleans now decode the several JSON encodings the Proxmox VE API emits. PVE renders booleans inconsistently across endpoints — as a JSON boolean (`true`/`false`), a number (`1`/`0`), or a string (`"1"`/`"0"`, `"true"`/`"false"`, `"yes"`/`"no"`, `""`) — which caused typed get-by-id responses (for example QEMU status `agent`, user `enable`, role privileges) to fail with `cannot unmarshal number into Go struct field ... of type bool` against real payloads. A new tolerant `client.PVEBool` type accepts every form and marshals back out as a native JSON boolean.

### Changed

- The generator (`cmd/pvegen`) now emits `*client.PVEBool` for boolean fields in response structs; request parameter structs keep plain `bool` so query encoding is unchanged. Regenerated bindings retype 150 response boolean fields. `PVEBool` has an underlying type of `bool` and a `Bool()` accessor; call sites reading these fields convert with `bool(*field)` or `field.Bool()`.

## [v3.2.1] — 2026-06-01

### Changed

- Wrapped WebSocket transport errors (`Conn.ReadMessage`/`WriteMessage`/`Close` and proxy POST) so failures carry package context, and promoted ad-hoc error strings to named sentinels in the streaming and generator code paths. No exported symbols changed; behavior is identical.
- Extracted repeated content-type, log-field, realm, and protocol literals into named constants; reduced the complexity of several transport, streaming, and disk-attach helpers by extracting sub-functions. Pure refactor, no API or behavior change.

### Internal

- Scoped `gosec` and `golangci-lint` to exclude generator-emitted bindings and domain-inherent rules (request structs carry credentials, deliberate certificate pinning, opt-in `InsecureSkipVerify`, and issuing HTTP to a caller-configured host), keeping the Makefile and CI in sync. The full lint and security suite (`go vet`, `staticcheck`, `golangci-lint`, `gosec`, `govulncheck`, `go test -race`) now passes clean.
- Hardened the test suite: descriptive variable names, parallel-safe subtests, closed response bodies, two-value type assertions, and deduplicated fixtures. Generated bindings remain owned by `cmd/pvegen`; none were hand-edited.

## [v3.2.0] — 2026-06-01

### Added

- Full Proxmox VE 9.2 API surface. Bindings regenerated from the official 9.2 `apidoc`. New operations: cluster-wide QEMU listing (`Cluster().ListQemu`), QEMU CPU flags (`Cluster().ListQemuCpuFlags`), custom CPU model CRUD (`Cluster().ListQemuCustomCpuModels`, `CreateQemuCustomCpuModels`, `GetQemuCustomCpuModels`, `UpdateQemuCustomCpuModels`, `DeleteQemuCustomCpuModels`), and `Nodes().DeleteCephFs`. New optional parameters for SDN fabrics, controllers, and zones (`Redistribute`, `BgpMode`, `Nodes`, `PeerGroupName`, `SecondaryControllers`) and for access domains (`Audiences`). All additions are backward compatible — no exported symbols were removed or renamed.

- `Tasks().WaitForUPID(ctx, upid, opts)`, `ParseUPID`, and a typed `UPID` struct for awaiting asynchronous PVE tasks. Task `Status.Warned` distinguishes a warning-completion (`OK: WARNINGS`) from a clean exit.

- Ordered option-string and indexed-array encoding helpers in the form encoder: `OptionString`, `NewOptionString`, `OptionStringOf`, `IndexedSlice`, and `IndexedSliceOf`. PVE option strings such as `virtio,bridge=vmbr0` and indexed array parameters such as `key0`/`key1` now serialize in the exact order PVE expects, with a positional leading token and booleans encoded as `1`/`0`.

- Ticket-only authentication via `NewTicketAuthenticatorFromTicket`. `Client.UpdateTicket` and `Client.UpdateCSRFToken` now propagate to the active authenticator.

### Fixed

- Write requests (POST, PUT, DELETE) are no longer silently auto-retried. Only idempotent methods (GET, HEAD, OPTIONS) retry automatically; opt a non-idempotent call into retry explicitly with `WithForceRetry`. This prevents duplicate side effects — for example, duplicate VM creation — when a write succeeds on the server but the response is lost to a transient failure.

- A retried request now re-buffers its body correctly via `Request.GetBody`, so second and later attempts resend the original payload instead of an empty body.

- An HTTP 401 response now forces re-authentication and a single retry of the original request.

- A 2xx response carrying a non-empty `errors` map is now surfaced as an `APIError` instead of being treated as success. This applies to `ResponseParser`. The client itself returns a 2xx reply that has an `errors` map and no message as a normal response.

- `IsRetryableCode` no longer treats HTTP 423 (Locked) as retryable. 429, 502, 503, and 504 still retry.

### Changed

- `_data/apidoc.json` refreshed to the Proxmox VE 9.2 specification (444 endpoints / 675 method-operations).

## [v3.1.6] — 2026-05-20

### Added

- `Storage().DeleteVolumeAsync(ctx, node, storage, volume) (upid, err)` and `Storage().DeleteVolumeIfExistsAsync(ctx, node, storage, volume) (existed, upid, err)` — return the queued `imgdel` task UPID so callers can await completion via `Tasks()` before re-uploading to the same volume name. Existing `DeleteVolume` and `DeleteVolumeIfExists` are unchanged (they now delegate to the async variants and discard the UPID); their doc comments now warn that under per-storage lock contention, the queued `imgdel` task can run *after* the call returns, silently removing a subsequently-uploaded replacement. Any caller pattern of *delete-then-immediately-upload-same-name* must migrate to the async variant.

## [v3.1.5] — 2026-05-19

### Fixed

- `Cloudinit().Attach` and `Cloudinit().AttachWithNetwork` no longer send `filename` as a duplicate form field. Same root cause and fix as the `Storage().Upload` change in v3.1.4.

## [v3.1.4] — 2026-05-19

### Fixed

- `Storage().Upload` no longer sends `filename` as a duplicate form field. PVE rejects the request with HTTP 400 when `filename` appears both as a form field and as the multipart file part name; the file part already carries the destination name via its `filename` attribute.

## [v3.1.3] — 2026-05-19

### Added

- `Storage().Upload(ctx, node, storage, content, filename, body) (upid, err)` — uploads a file to a named storage pool as the given content type; returns the upload UPID for the caller to await via Tasks if synchronous semantics are required.

- `Storage().DeleteVolumeIfExists(ctx, node, storage, volume) (existed, err)` — deletes a volume and reports whether it was present; returns `(false, nil)` on 404, `(true, nil)` on success, and a wrapped error on any other failure. Distinct from `DeleteVolume`, which silently swallows 404.

## [v3.1.0] — 2026-05-18

### Fixed

- **API token authentication** — the `Authorization` header was being
  constructed incorrectly. The token is now formatted as
  `PVEAPIToken=USER@REALM!TOKENID=SECRET` and validated at construction
  time; malformed tokens return an error immediately instead of silently
  producing a bad header.

- **TicketAuthenticator data race** — concurrent requests that triggered
  a ticket refresh simultaneously could race on the internal ticket field.
  All reads and writes are now guarded by `sync.RWMutex`.

- **Form encoding** — slices, booleans, and nested maps were not serialized
  correctly for PVE form-encoded POST/PUT bodies. Booleans now encode as
  `1`/`0` (not `true`/`false`), slices expand to repeated keys, and nested
  maps flatten with the dot-notation PVE expects.

- **WebSocket race conditions** — four distinct races in `pkg/websocket`:
  concurrent writes to the gorilla connection (not concurrency-safe),
  the pong handler racing with the write serializer, the read loop and
  ping loop overlapping, and a lock-order inversion in `Disconnect`.
  All four are resolved; `writeMu` now serializes every frame write.

### Added

- **Typed API bindings** — `cmd/pvegen` generates typed `Service` interfaces
  and request/response structs for all 667 PVE 9.x endpoints from
  `_data/apidoc.json`. Generated files carry `// Code generated by
  cmd/pvegen. DO NOT EDIT.` headers.

- **New generated packages**:
  - `pkg/api/access` — users, roles, ACLs, TFA, API tokens (`/access`)
  - `pkg/api/cluster` — HA, ACME, firewall, replication, SDN (`/cluster`)
  - `pkg/api/clusterstorage` — cluster-wide storage configuration (`/storage`)
  - `pkg/api/nodes` — per-node resources, VMs, containers, tasks (`/nodes`)
  - `pkg/api/pools` — resource pool management (`/pools`)

- **`pkg/websocket` ProxyClient** — typed methods for obtaining proxy tickets
  and opening console WebSocket connections: `VMVNCProxy`, `VMVNCConnect`,
  `VMTermProxy`, `VMTermConnect`, `VMSpiceProxy`, `NodeVNCShell`,
  `NodeTermShell`, `NodeSpiceShell`, `LXCVNCProxy`, `LXCVNCConnect`,
  `LXCTermProxy`, `LXCTermConnect`, and migration-tunnel variants
  (`MTunnelWebSocket`, `MTunnelWebSocketVM`).

- **Error sentinels** in `pkg/errors`: `ErrUnauthorized`, `ErrForbidden`,
  `ErrNotFound`, `ErrConflict`, `ErrServer`. All wrap `*APIError` and support
  `errors.Is` matching.

- **Make targets** `generate` and `verify-generated` for managing the
  code-generation lifecycle.

### Improved

- **Test coverage**:
  - `internal/http`: 6.6% → 87%
  - `pkg/auth`: 38.8% → 87.8%
  - `pkg/errors`: 83.8% → 97.8%
  - `pkg/api/tasks`: 68.9% → 97.3%

### Notes

- Indexed parameter families (`net[n]`, `scsi[n]`, `ide[n]`, etc.) are
  modeled as `map[int]string` in generated request types. `MarshalJSON`
  expands them in sorted key order.
- Path parameters are escaped with `url.PathEscape` in all generated
  service methods.

## [0.1.0] - TBD

### Added

- Initial alpha release
- Core functionality for PVE API interaction
- Basic documentation and examples

[Unreleased]: https://github.com/fivetwenty-io/proxmox-apiclient-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/fivetwenty-io/proxmox-apiclient-go/releases/tag/v0.1.0
