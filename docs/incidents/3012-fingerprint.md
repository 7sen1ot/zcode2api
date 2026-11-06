# 3012 Fingerprint Incident

## Symptom

Downstream inference returned HTTP 502 with upstream code `3012`:

```text
request has been blocked due to unusual activity
```

The same prompt had succeeded earlier, then failed across multiple accounts.

## Root Cause

The protocol translation was semantically valid but not indistinguishable from
the official ZCode 3.17.2 client.

Observed differences from the official `model_io` request:

- `x-zcode-app-version` was `0.17.0` instead of `3.17.2`.
- `User-Agent` used a provider utility string instead of `ZCode/3.17.2`.
- `x-request-id` used the local `resp_...` response id instead of a raw UUID.
- `x-query-id` was UUIDv4; the official client uses UUIDv7.
- `metadata.user_id` was absent. Official shape:
  `{"device_id":"...","account_uuid":"","session_id":"..."}`
- Platform headers described Linux instead of the official Windows client.
- The system prompt used an older simplified two-block template.
- The visualization-directory placeholder was not tied to the request session.

Immediate account rotation after `3012` also turned one request-level risk
decision into a pool-wide failure cascade. This was reverted; the solution is
fingerprint alignment, not fail-closed behavior.

## Fix

- Build headers from `app_version`, not `http.client_version`.
- Send `User-Agent: ZCode/<app_version>`.
- Send a raw UUID as `x-request-id`.
- Generate UUIDv7 for `x-query-id`.
- Send `metadata.user_id` with device/account/session alignment.
- Use the official three-block ZCode 3.17.2 system template.
- Merge downstream system prompts into the official third block.
- Generate the visualization directory id from device and session ids.
- Default platform fingerprint to `win32-x64`, `windows`, and
  `10.0.22631`, while allowing config overrides.

Patch: `patches/upstream-fingerprint.patch`

## Evidence

Official request:

```text
User-Agent: ZCode/3.17.2
x-zcode-app-version: 3.17.2
x-platform: win32-x64
x-os-category: windows
x-query-id: UUIDv7
metadata.user_id:
  {"device_id":"...","account_uuid":"","session_id":"..."}
system blocks: 3
```

After the fix, live downstream checks returned:

- Non-stream chat: HTTP 200, content `OK`
- Streaming chat: HTTP 200, 8 SSE chunks, terminal `[DONE]`
- Tool call: HTTP 200, `finish_reason=tool_calls`, valid `ping({"value":"ok"})`

## Rule

A relay is not compatible merely because its JSON shape is accepted. Every
credential version, request id, query id, metadata identity, platform header,
and system template must match the observed official client.

## Detection

Search for old fingerprint values:

```text
http.client_version in upstreamHeaders
User-Agent: ZCode/0.17.0
x-request-id: resp_
metadata.user_id absent
systemPrefix length != 3
```

Re-test non-stream, stream, and tool calls after any upstream-related change.
