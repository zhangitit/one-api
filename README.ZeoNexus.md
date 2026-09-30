# ZeoNexus Gateway 1.1

This fork packages One API as the ZeoNexus data plane. With `ZEO_NEXUS_ENABLED=true`, the original dashboard and management API are not mounted. The process exposes only health probes, the versioned Nexus control API, model discovery and chat completions.

## Profiles

- `ZEO_NEXUS_PROFILE=aggregation`: commercial provider channels, `sk-nx-agg-*` credentials.
- `ZEO_NEXUS_PROFILE=inference`: private or ZeoNexus GPU endpoints, `sk-nx-cmp-*` credentials.

Run two isolated instances with separate databases and Redis namespaces. `deploy/docker-compose.1.1.yml` is the reference topology.

## Local development domains

The optional Apache templates in `deploy/apache` map the local development domains to the two loopback-only Gateway ports:

```text
api.kevin.com     -> 127.0.0.1:3101 (aggregation)
compute.kevin.com -> 127.0.0.1:3102 (inference)
```

After adding both names to `/etc/hosts`, run `deploy/apache/install-local-vhosts.sh` with administrator privileges. The script enables Apache `mod_proxy_http`, installs both virtual hosts, validates the Apache configuration and performs a graceful reload.

## Public API

Production public endpoints:

```text
https://stack.zeotrue.com/v1   -> Aggregation Gateway
https://compute.zeotrue.com/v1 -> Inference Gateway
```

These public domains terminate at Nginx/WAF and proxy to the loopback-only ports in the reference Compose file. They are not model-upstream allowlist entries.

The release includes `deploy/nginx/zeonexus-gateway.conf.example`. Copy it into the production Nginx configuration, add the production certificate paths and management-network addresses, run `nginx -t`, then reload Nginx. The public virtual hosts expose only health probes and `/v1/`; the signed control API stays on its private management listener.

```text
GET  /healthz
GET  /readyz
GET  /v1/models
GET  /v1/models/:model
POST /v1/chat/completions
```

Model endpoints require the matching ZeoNexus Bearer key. Chat completions support non-streaming JSON and SSE. Unsupported One API routes are not registered in ZeoNexus mode.

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
go test . ./model ./middleware ./controller ./relay/controller
go build .
docker compose --env-file deploy/.env -f deploy/docker-compose.1.1.yml config -q
```

The integration test uses a local OpenAI-compatible upstream and validates HMAC, replay and query tamper rejection, encrypted channel storage, tenant/model isolation, stream and non-stream relay, quota settlement, usage export, balance reconciliation and revocation.
