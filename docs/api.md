# API

The panel's REST API is what its own web app, the WHMCS module and your
scripts use. Every route is described in the panel's OpenAPI 3.1 document:

- **`/docs/api`** on your panel: the reference, readable in a browser, with a
  filter by path, summary or permission. Open to everyone.
- **`/api/v1/openapi.json`**: the same document for tools (code generators,
  API clients). Public: it describes the API's shape, which is the same on
  every install and already readable in this repository, and holds no data,
  address or secret, so integrators can read it before they have credentials.

The document is generated from the routes the panel registers and the Go types
its handlers use. A test (`TestOpenAPICoversEveryRoute`) fails when a route is
served but not documented, or documented but gone, and another
(`TestOpenAPIAccessMatchesMiddleware`) when a route's documented permissions are
not what it enforces.

## Signing in

| Method | How | For |
|---|---|---|
| Session cookie | `POST /api/v1/auth/login` sets `accessToken` (HttpOnly, SameSite=Lax). A browser write carrying it from another origin (`Sec-Fetch-Site` cross-site or same-site) is refused. | The web app. |
| Bearer token | The same login answers `token`; send `Authorization: Bearer <token>`. It lasts the session length (Settings, System, Security). | Short scripts. |
| API key | Settings, API Keys. Send `X-API-Key: mvk_...` or `Authorization: Bearer mvk_...`. | Automation. |

An API key acts as its owner, with the owner's staff role as it is at each
call. A **read-only** key may only read; an **own resources only** key makes a
staff owner act as a customer. Neither may manage API keys or account
settings. Two-factor, IP whitelists and suspension apply to every method.
Operators can turn API keys off (Settings, System, API).

## Who may call what

Each operation in the reference states its access as `x-access` and in words:
`public`, `signed-in` (any account, acting on itself), `staff` (any staff
account), or the staff permissions it needs, all of them, `a|b` meaning
either. `x-customer: true` means customers may call it too, on their own
resources; another tenant's resource answers 404, not 403. The permission
catalog is `GET /api/v1/staff-roles` and is described in the
[administrator guide](admin-guide.md#82-staff-roles).

## Answers and errors

Answers are JSON, but **not yet one shape**: most are
`{"success": true, "data": ...}`, some older ones `{"message": ..., "data": ...}`
or `{"data": ...}` or the object alone, and lists are paginated four different
ways. The reference documents each operation's real shape and says where an
answer is not wrapped; making them consistent is planned, and will come with a
release note.

Errors carry `error` and usually a human `message`, sometimes a machine `code`;
read `message` when present.

| Status | Meaning |
|---|---|
| 400 | A missing or malformed field, or an action the resource's state does not allow. |
| 401 | No valid session, token or key. A suspended account gets `code: account_suspended`. |
| 403 | A missing permission, a read-only or narrowed key, an IP not on the whitelist, a cross-site browser write, two-factor setup required (`two_factor_required`), or a step staff may not take while viewing as a customer (`impersonation_forbidden`). |
| 404 | No such resource, or one the caller may not see. |
| 409 | The resource is busy or in conflict (a job already running, a duplicate). |
| 429 | Over a rate limit; `Retry-After` gives the seconds to wait. |
| 503 | A dependency is unavailable: a node, the job queue, or the billing webhook before it is configured. |

Writes that start work on a node run as background jobs and answer at once
(often 202, or with a job ID); read the resource, or
`GET /api/v1/vms/{id}/operation`, for progress.

## Rate limits

Every signed-in account and every API key gets "Requests per minute"
(Settings, System, API; default 1200, 0 turns it off), with ten seconds' worth
allowed at once. Sign-in, password reset, single sign-on, first-run and the
node installer endpoints are limited per client address. The billing webhook
is not counted. Counts live in the panel's memory and start over when it
restarts.

## MCP

`POST /api/v1/mcp` speaks the Model Context Protocol (JSON-RPC 2.0 over
Streamable HTTP) for AI agents. Only API keys are accepted, and each tool call
runs through the REST route it maps to, so permissions, tenant isolation and
the audit log apply. A read-only key lists only the monitoring tools.

```bash
claude mcp add --transport http maburvm https://panel.example.com/api/v1/mcp \
  --header "Authorization: Bearer mvk_..."
```

## Webhooks

### Inbound: the billing webhook

`POST /webhooks/billing` is how a billing system drives the panel. Each request
carries:

- `X-API-Key`: the billing API key;
- `X-Webhook-Signature`: the hex HMAC-SHA256 of the raw body with the billing
  webhook secret (a `sha256=` prefix is accepted);
- optionally `X-Idempotency-Key`: a retried `vm.create` or `vm.destroy` with
  the same key answers with the stored result for 24 hours.

The body has `event`, `timestamp` (RFC 3339, within 5 minutes of the panel's
clock), `nonce` (new for every request) and `data`. Each signed request is
accepted once; a repeat answers 409. The events and their fields are in the
reference under "Billing webhook" and at `GET /webhooks/billing/docs`.

```bash
body='{"event":"catalog.list","timestamp":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'","nonce":"'"$(openssl rand -hex 8)"'","data":{}}'
sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$BILLING_WEBHOOK_SECRET" -hex | sed 's/^.* //')
curl -sS https://panel.example.com/webhooks/billing \
  -H "Content-Type: application/json" -H "X-API-Key: $BILLING_API_KEY" \
  -H "X-Webhook-Signature: sha256=$sig" -d "$body"
```

### Outbound: billing events

With a webhook URL saved under Settings, System, API, the panel posts an event
when something a billing system cares about happens:

| Event | Sent when |
|---|---|
| `vm.created` | A VM finished installing. |
| `vm.failed` | A VM's installation failed: once at the first failed try (`retrying: true`) and once more if every retry fails. |
| `vm.suspended`, `vm.unsuspended`, `vm.destroyed` | A VM was suspended, restored, or its deletion accepted, by anyone. |
| `bandwidth.overage` | A VM on an `overage` plan crossed its monthly transfer quota. |
| `floating_ip.usage` | An account's floating IP count changed. `total`, `free` and `billable` are a snapshot, not a change. |

Headers: `X-Webhook-Event`, `X-Webhook-Id` (the same as the body's `id`; it
repeats on a retry, so use it to de-duplicate) and, when a secret is saved,
`X-Webhook-Signature`: the hex HMAC-SHA256 of the raw body, with no prefix. The
secret is the HMAC secret saved on the same page, or the billing webhook secret
when none is. A receiver must answer 2xx; anything else is retried up to 10
times over about four hours, and each attempt is listed under Billing
deliveries.

```json
{
  "id": "6c2f...",
  "event": "vm.created",
  "sent_at": "2026-01-01T10:00:00Z",
  "vm_id": "0b6f...",
  "user_id": "a1d3...",
  "user_ref": "whmcs-client-42",
  "external_ref": "whmcs-1234",
  "hostname": "web-1",
  "status": "running",
  "ip": "192.0.2.10",
  "ips": ["192.0.2.10"],
  "username": "root",
  "plan_id": "5e7a..."
}
```

Checking the signature, in Python:

```python
import hashlib, hmac

def valid(raw_body: bytes, header: str, secret: str) -> bool:
    expected = hmac.new(secret.encode(), raw_body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header)
```

Always compute it over the raw body as received, before parsing it.

### Outbound: alerts

The alert webhook (Alerts page) posts this body in the JSON format; the Slack
and Discord formats post a line of text instead. With a signing secret saved,
`X-Webhook-Signature` is the hex HMAC-SHA256 of the raw body. A receiver must
answer 2xx within ten seconds.

```json
{
  "version": "1",
  "source": "MaburVM",
  "panel_url": "https://panel.example.com",
  "sent_at": "2026-01-01T10:00:00Z",
  "text": "Critical: Node node1 is offline\nIt has missed 3 checks in a row. ...",
  "alerts": [
    {
      "id": "0b6f...",
      "key": "node_offline:<node id>",
      "kind": "node_offline",
      "status": "firing",
      "severity": "critical",
      "title": "Node node1 is offline",
      "detail": "It has missed 3 checks in a row. ...",
      "resource": { "type": "node", "id": "<node id>", "name": "node1" },
      "fired_at": "2026-01-01T09:59:00Z",
      "resolved_at": null
    }
  ]
}
```

`status` is `firing`, `resolved` or `event` (a failure with no "resolved");
`kind` is `node_offline`, `node_silent`, `storage_pool`, `firewall_off`,
`backup_failed`, `job_failed` or `network_drift`; `key` is the same for the firing and resolved
message of one alert. A test alert has `"test": true`. Both outbound payloads
gain fields over time; none are renamed or removed.

## Stability

This is version 1 of the API. Fields may be added to answers; existing ones are
not renamed or removed without a note in the release.
