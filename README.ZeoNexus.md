# ZeoNexus Gateway 1.1

This fork packages One API as the ZeoNexus data plane. With `ZEO_NEXUS_ENABLED=true`, the original dashboard and management API are not mounted. The process exposes only health probes, the versioned Nexus control API, model discovery and chat completions.

## Profiles

- `ZEO_NEXUS_PROFILE=aggregation`: commercial provider channels, `sk-nx-agg-*` credentials.
- `ZEO_NEXUS_PROFILE=inference`: private or ZeoNexus GPU endpoints, `sk-nx-cmp-*` credentials.

Run two isolated instances with separate databases and Redis namespaces. `deploy/docker-compose.1.1.yml` is the reference topology.

## Local development domains

The optional Apache templates in `deploy/apache` map the local development domains to the two loopback-only Gateway ports:

```text
oneapi.kevin.com   -> 127.0.0.1:3101 (aggregation)
stack.kevin.com -> 127.0.0.1:3102 (inference)
```

After adding both names to `/etc/hosts`, run `deploy/apache/install-local-vhosts.sh` with administrator privileges. The script enables Apache `mod_proxy_http`, installs both virtual hosts, validates the Apache configuration and performs a graceful reload.

## Public API

Production public endpoints:

```text
https://oneapi.zeotrue.com/v1   -> Aggregation Gateway
https://stack.zeotrue.com/v1 -> Inference Gateway
```

These public domains terminate at Nginx/WAF and proxy to the loopback-only ports in the reference Compose file. They are not model-upstream allowlist entries.

The release includes `deploy/nginx/zeonexus-gateway.conf.example`. Copy it into the production Nginx configuration, add the production certificate paths and management-network addresses, run `nginx -t`, then reload Nginx. The public virtual hosts expose only health probes and `/v1/`; the signed control API stays on its private management listener.

```text
GET  /healthz
GET  /readyz
GET  /v1/models
GET  /v1/models/:model
POST /v1/chat/completions
POST /v1/messages
POST /v1/messages/count_tokens
```

Model endpoints require the matching ZeoNexus Bearer key. Messages endpoints also accept that same gateway key in `x-api-key`, as sent by Anthropic SDKs. Chat completions and Messages support non-streaming JSON and SSE. Unsupported One API routes are not registered in ZeoNexus mode.

### Anthropic Messages and Claude Code

Both gateway profiles expose the Anthropic Messages API. Publish the model's existing route with `mode: chat`; the gateway applies the same tenant grants, model mapping, concurrency limits and usage settlement to either client protocol.

- An **Anthropic channel (type 14)** forwards native JSON fields, system content blocks, cache controls, thinking/signatures, tool schemas and SSE events without converting them through OpenAI structures. The channel's base URL may end at the host or `/v1`. Query parameters and `anthropic-*` headers are forwarded, and the gateway replaces the client credential with the channel credential. Native provider errors, retry hints and rate-limit headers are preserved.
- An **OpenAI-compatible channel** translates system/text messages, tool definitions, tool choices, assistant tool calls and user tool results. JSON responses and streamed text/tool argument deltas are returned in Anthropic format. Features with no portable OpenAI equivalent, including signed thinking, enabled/adaptive thinking configuration, server tools and `output_config`, return an explicit error. Use a native Anthropic channel for those features. Prompt-cache hints do not provide Anthropic caching guarantees on an OpenAI upstream.
- `/v1/messages/count_tokens` forwards exact counts on native Anthropic channels and performs no generation quota or cash reservation. Other upstreams return an explicit unsupported-operation error rather than an estimated Anthropic count.

`context_management` accepts the clear-only strategies `clear_thinking_20251015` and `clear_tool_uses_20250919`, including the `clear_thinking` / `keep: "all"` configuration sent by Claude Code. Native Anthropic channels preserve these settings unchanged. The OpenAI bridge handles empty/no-op settings and can apply supported history clearing locally while retaining tool-call/result IDs. Token-based clearing is accepted without changes only when the conservative input bound proves the trigger cannot fire; cases requiring exact provider token counts return a capability error. Reservations always use the original full request. Summary-generating compaction (`compact_20260112` or top-level `compaction`) remains unsupported because it needs separate generation budgeting and iteration-aware usage settlement. See [Anthropic context editing](https://platform.claude.com/docs/en/build-with-claude/context-editing).

ZeoNexus retains its conservative text/tool-input reservation policy. Image/document blocks, hosted tools and external execution contexts are rejected for generation because their token budget cannot be bounded by serialized text size. This restriction also applies to nested tool results. Native `max_tokens` is required and may be zero for cache prewarming. Requests without authoritative final usage, including interrupted streams, remain pending reconciliation; the gateway never retries a stream after sending its first event.

For example, use a gateway model alias that has a published chat route:

```bash
curl https://oneapi.zeotrue.com/v1/messages \
  -H "x-api-key: $ZEO_NEXUS_API_KEY" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'content-type: application/json' \
  -d '{"model":"your-model-alias","max_tokens":256,"messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

For Claude Code, set its gateway base URL to the host **without `/v1`** and select the published model alias:

```bash
export ANTHROPIC_BASE_URL=https://oneapi.zeotrue.com
export ANTHROPIC_AUTH_TOKEN="$ZEO_NEXUS_API_KEY"
export ANTHROPIC_MODEL=your-model-alias
```

Use `https://stack.zeotrue.com` and the corresponding `sk-nx-cmp-*` key for the inference profile. Native thinking and provider-specific capabilities require a compatible native channel and model.

## Control API

The internal contract is documented in `docs/openapi-zeonexus-control.yaml`. Every request signs:

```text
METHOD + "\n" + REQUEST_TARGET_WITH_QUERY + "\n" + UNIX_TIMESTAMP + "\n" + NONCE + "\n" + SHA256(BODY)
```

Send the lowercase hex HMAC-SHA256 in `X-Zeo-Signature`, with `X-Zeo-Timestamp` and a unique `X-Zeo-Nonce`. The gateway rejects stale timestamps, reused nonces, changed query parameters and changed bodies.

## Required production configuration

```text
ZEO_NEXUS_ENABLED=true
ZEO_NEXUS_PROFILE=aggregation|inference
ZEO_NEXUS_CONTROL_SECRET=<32+ chars>
ZEO_NEXUS_MASTER_KEY=<independent 32+ chars>
ZEO_NEXUS_ALLOWED_UPSTREAM_HOSTS=<exact comma-separated hosts>
ZEO_NEXUS_ALLOW_INSECURE_HTTP=false
SQL_DSN=<profile-specific database>
REDIS_CONN_STRING=<profile-specific namespace>
SYNC_FREQUENCY=30
ENFORCE_INCLUDE_USAGE=true
CHANNEL_TEST_FREQUENCY=5
RELAY_TIMEOUT=300
ZEO_NEXUS_MAX_REQUEST_BODY_BYTES=4194304
ZEO_NEXUS_MAX_RESERVE_TOKENS=262144
```

Use HTTPS upstreams. A GPUStack endpoint on a trusted management network may use HTTP during local acceptance only when the inference instance explicitly sets `ZEO_NEXUS_ALLOW_INSECURE_HTTP=true`; the aggregation instance remains HTTPS-only. All hosts must match the exact allowlist, aggregation hosts must resolve only to public address ranges, and redirects are rejected. Channel credentials are encrypted with AES-GCM before they enter the One API channel table; changing the master key makes existing credentials unreadable. ZeoNexus mode also fixes usage enforcement, retry policy, and automatic channel disable/recovery independently of legacy dashboard options.

## Verification

```bash
go test ./router -run TestZeoNexusGatewayControlAndRelay -v
go test ./router -run Anthropic -v
go test ./relay/anthropic ./relay/controller ./relay/adaptor/anthropic ./middleware ./common/helper
go test . ./model ./middleware ./controller ./relay/controller
go build .
docker compose --env-file deploy/.env -f deploy/docker-compose.1.1.yml config -q
```

The integration test uses a local OpenAI-compatible upstream and validates HMAC, replay and query tamper rejection, encrypted channel storage, tenant/model isolation, stream and non-stream relay, quota settlement, usage export, balance reconciliation and revocation.
