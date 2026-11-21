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
call, and never more. Customers make their own keys under Settings, API Keys in
the customer area; staff under Settings, API Keys. Each key carries:

- a **scope**, which narrows what it may do within the owner's role:

  | Scope | May |
  |---|---|
  | `full` | Everything the owner may. |
  | `read_only` | Only read (`GET`). |
  | `vm_power` | List and read VMs, and start, stop, force-stop and restart them. Nothing else. |
  | `billing` | Billing, plans, customer accounts, reports, usage and VM size changes: for a billing system's staff key. It may not create staff accounts, change a role, or change a staff account. |

  A request outside the scope answers 403. Keys made before scopes keep acting
  as they did: `read_only` when they were read-only, else `full`.
- an optional list of **addresses** (IPs or CIDR ranges, up to 50) it works
  from; a request from anywhere else answers 403 with `code: api_key_address`.
  The owner's own IP whitelist applies as well.
- an optional **expiry**, after which it answers 401.

An **own resources only** key (`role: client`) makes a staff owner act as a
customer. A key that is narrower than its owner in any way, or has its own
address list, may not manage API keys, account settings or the account's team
(`/api/v1/account/*`). Two-factor, IP whitelists and suspension apply to
every method. Operators can turn API keys off (Settings, System, API).

```bash
curl -sS https://panel.example.com/api/v1/api-keys -H "Authorization: Bearer mvk_..." \
  -H "Content-Type: application/json" \
  -d '{"name":"restart-bot","scope":"vm_power","allowed_ips":["203.0.113.10","198.51.100.0/24"],"expires_at":"2027-01-01T00:00:00Z"}'
```

## Who may call what

Each operation in the reference states its access as `x-access` and in words:
`public`, `signed-in` (any account, acting on itself), `staff` (any staff
account), or the staff permissions it needs, all of them, `a|b` meaning
either. `x-customer: true` means customers may call it too, on their own
resources; another tenant's resource answers 404, not 403. The permission
catalog is `GET /api/v1/staff-roles` and is described in the
[administrator guide](admin-guide.md#82-staff-roles).

A customer's "own resources" include the VMs of accounts they are a team
member of, as their member role allows: any role reads them, and only Admin
and Technical change them, open their console or reveal their passwords (403
`member_read_only` for the others). `GET /api/v1/vms` lists them beside the
caller's own; `account_id` narrows the list to one account, and `account_id`
on `POST /api/v1/vms` orders into one. Teams, projects and invitations are
under `/api/v1/account/` (group "Teams and customer area" in the reference).

## Answers and errors

A successful answer is `{"success": true, "data": ...}`. Some older routes
still answer `{"message": ..., "data": ...}`, `{"data": ...}` alone or the
object itself; the reference documents each operation's real shape, and those
are kept as they are for the clients that read them.

**Every error answer has one shape**, whichever route or check refused it:

```json
{
  "success": false,
  "error": { "code": "not_found", "message": "VM not found", "details": {} },
  "message": "VM not found"
}
```

- `error.code` is a stable machine code: the route's own where it has one
  (`two_factor_required`, `account_suspended`, `maintenance`, `api_key_address`,
  `delete_locked`, `captcha_required`...), else one for the status:
  `bad_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`,
  `validation_failed`, `rate_limited`, `unavailable`, `internal_error`...
- `error.message` says what went wrong in words; show it to people.
- `error.details` is there only when the route has more to say.
- The top-level `message` repeats `error.message`, and other top-level fields a
  route sent before the envelope (`code`, `request_id`, `provider`...) stay
  where they were, so clients written for older panels keep working. Before
  this version `error` was a string; read `error.message` (or `message`)
  rather than `error`.
- The MCP endpoint answers in JSON-RPC's own error shape.

**Every paginated list carries `meta`** beside its data:

```json
{ "success": true, "data": [ ... ], "meta": { "page": 2, "per_page": 50, "total": 312 } }
```

Ask for a page with `page` (from 1) and `per_page`; `page_size` is the older
name of `per_page` and wins when both are sent. Lists read by `limit` and
`offset` still take them, and their `meta` gives the page those make. Lists
that answer whole unless a window is asked for (nodes, IP pools, a pool's
addresses) say so in `meta` too. Older fields beside `meta` (`total`, `page`,
`page_size`, `limit`, `offset`, `has_more`, `totalPages`) stay for the clients
that read them. Where a list sits inside `data` (alerts, deliveries, emails),
`meta` is still at the top.

| Status | Meaning |
|---|---|
| 400 | A missing or malformed field, or an action the resource's state does not allow. |
| 401 | No valid session, token or key. A suspended account gets `code: account_suspended`. |
| 403 | A missing permission, a read-only or narrowed key, an IP not on the whitelist, a cross-site browser write, two-factor setup required (`two_factor_required`), or a step staff may not take while viewing as a customer (`impersonation_forbidden`). |
| 404 | No such resource, or one the caller may not see. |
| 409 | The resource is busy or in conflict (a job already running, a duplicate), or locked against deletion and reinstall (`delete_locked`). |
| 429 | Over a rate limit; `Retry-After` gives the seconds to wait. |
| 503 | A dependency is unavailable: a node, the job queue, or the billing webhook before it is configured. In maintenance mode, a change by a customer, a customer's API key or the billing webhook answers 503 with `code: maintenance`, the reason in `message` and a `Retry-After`; reads still work, and staff are not held. `GET /api/v1/maintenance` says whether it is on. |

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
runs through the REST route it maps to, so permissions, the key's scope and
address list, tenant isolation and the audit log apply. `tools/list` lists only
the tools the key may use: a read-only key sees the monitoring tools, a
`vm_power` key the VM list and power tools, and a customer's key never the
staff ones.

| Area | Tools | Who |
|---|---|---|
| VMs | `list_vms`, `get_vm`, `get_vm_metrics`, `get_vm_operation`, `get_vm_activity`, `vm_power`, `create_vm`, `update_vm`, `rebuild_vm`, `delete_vm`, `reset_password` | Customers on their own VMs; staff with the VM permissions |
| Snapshots | `list_snapshots`, `create_snapshot`, `restore_snapshot` | The same |
| Backups | `list_backups`, `create_backup`, `restore_backup` | The same, with `backup:*` |
| Firewall | `list_firewall_rules`, `add_firewall_rule`, `delete_firewall_rule` | The same |
| Addresses | `list_vm_networks`, `list_floating_ips`; `list_ip_pools` | Customers on their own; pools for staff with `network:read` |
| Catalog | `list_templates`, `list_plans` | Everyone |
| Staff | `list_nodes`, `get_node`, `get_node_metrics`, `list_users`, `get_user`, `list_alerts`, `get_vm_migration` | Staff with `node:read`, `user:read`, `ops:read` or `vm:migrate` |

Destructive tools carry `destructiveHint` and act at once; an agent should
confirm with its user first.

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
reference under "Billing webhook" and at `GET /webhooks/billing/docs`. While
the panel is in maintenance mode every event except `vm.status`, `vm.usage`,
`vm.metrics`, `catalog.list` and `user.sso` answers 503 with `code: maintenance`
and a `Retry-After`; send it again later. `vm.destroy` is not stopped by a customer's own delete lock, but is by a
staff lock (409, with the reason in `message`).

`vm.usage` reports, in `metadata` (strings) and `data` (numbers), what a VM
uses for a usage sync: `bandwidth_used_mb` and `bandwidth_limit_mb` this
transfer period, `disk_limit_mb` (its disk) and `disk_allocated_mb` (with its
data disks; disks are thin, so what the guest wrote is not measured),
`snapshot_count`, `backup_count`, `backup_size_mb`, `extra_ips` (addresses
beyond the first), `floating_ips`, and `uptime_hours`: the clock hours since
`uptime_since` (the start of the period, at most seven days back) in which the
VM was seen running. `vm.metrics` returns up to 48 averaged readings of the
last `hours` (default 24) for graphs; `vm.rebuild` reinstalls a stopped VM
with a system its plan offers.

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

### Outbound: webhooks an operator adds

Under Settings, System, API, Outbound webhooks, staff with `webhook:update`
add any number of webhooks, each with its own URL, signing secret (stored
encrypted, never shown again) and the events it wants, or every event
including ones added later (`*`). The same API is
`/api/v1/webhooks` (see the reference, "Operations").

| Group | Events |
|---|---|
| VMs | `vm.created`, `vm.failed`, `vm.suspended`, `vm.unsuspended`, `vm.destroyed` |
| Backups | `backup.completed`, `backup.failed` (failed for good, after the queue's retries) |
| Alerts | `alert.raised` (an alert opened, or an event alert fired), `alert.resolved` |
| Billing | `bandwidth.overage`, `floating_ip.usage` |
| Accounts | `user.created`, `user.suspended`, `user.unsuspended`, `user.deleted` |

Each event is a JSON object with `id`, `event`, `sent_at` and the event's own
fields (the VM events carry the same fields as the billing events above; an
alert carries `alert_id`, `key`, `kind`, `severity`, `state`, `title`,
`detail`, `resource_type`, `resource_id`, `resource_name` and `fired_at`; an
account `user_id`, `email`, `name`, `role` and, for a billing client,
`user_ref`; a backup the VM's fields plus `backup_id`, `backup_type`, `mode`,
`size_bytes` and, when it failed, `reason`). Headers and
retries are those of the billing events: `X-Webhook-Event`, `X-Webhook-Id`
(repeats on a retry), and with a secret `X-Webhook-Signature`, the hex
HMAC-SHA256 of the raw body with that webhook's secret. A receiver must answer
2xx within ten seconds; anything else is tried again up to 10 times over about
four hours. Each webhook keeps its own delivery log for 90 days, where a failed
event can be sent again, and **Send test** posts a `webhook.test` event at once
and shows how it went. A switched-off webhook is sent nothing new; an event
still queued for it fails with that reason and can be sent again once it is
back on. Removing a webhook removes its log and drops what was queued for it.

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
