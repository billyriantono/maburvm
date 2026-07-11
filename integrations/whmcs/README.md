# MaburVM WHMCS Provisioning Module

Provision and manage MaburVM KVM VMs from WHMCS. On WHMCS lifecycle events the
module calls the panel's signed **billing webhook** (`POST /webhooks/billing`):

| WHMCS action        | Webhook event  |
|---------------------|----------------|
| Create/Accept order | `vm.create`    |
| Suspend             | `vm.suspend`   |
| Unsuspend           | `vm.unsuspend` |
| Terminate           | `vm.destroy`   |

Requests are authenticated with a shared **API key** (`X-API-Key`) and an
**HMAC-SHA256 signature** (`X-Webhook-Signature: sha256=<hex>`) over the raw JSON
body, plus a `X-Webhook-Timestamp` (rejected if older than 5 minutes) and an
`X-Idempotency-Key` so retries don't double-provision.

## Install

1. Copy `modules/servers/maburvm/` into your WHMCS install at
   `<whmcs>/modules/servers/maburvm/`.
2. **Setup → Products/Services → Servers → Add New Server**:
   - *Hostname*: the panel host (e.g. `panel.example.com`), *Secure*: checked for HTTPS.
   - *Module*: `MaburVM`.
   - *Password* field → the panel **billing API key** (`BILLING_API_KEY`).
   - *Access Hash* field → the panel **webhook secret** (`BILLING_WEBHOOK_SECRET`).
   - Click **Test Connection** (hits `/webhooks/billing/docs`).
3. Create a **Product** using module `MaburVM` and set the module fields:
   - *Template ID* — a MaburVM OS template UUID.
   - *vCPU*, *Memory (MB)*, *Disk (GB)* — the plan's resources.
4. Add a **custom field** named exactly `Panel User ID` to the product
   (Product → Custom Fields). Store the panel **User UUID** that owns the
   service's VMs there. The webhook assigns the created VM to this user.

> The panel must be started with `BILLING_API_KEY` and `BILLING_WEBHOOK_SECRET`
> set (the same values entered above), otherwise the webhook rejects requests.

## Mapping WHMCS clients to panel users

The webhook sets the new VM's owner to `data.user_id`. Each WHMCS service must
therefore carry the panel User UUID it belongs to, via the `Panel User ID`
custom field. Create the panel user once (admin UI or API) and paste its UUID
into the service. Auto-provisioning the panel user from WHMCS is a possible
future enhancement (would require an admin user-management API call before
`vm.create`).

## What the module stores

After a successful `vm.create` the returned `vm_id` is saved on the WHMCS
service (`serviceProperties['vm_id']`) so suspend/unsuspend/terminate can
reference it. A hidden `VM ID` custom field is used as a fallback if service
properties are unavailable.

## Client area

The service detail page (`templates/clientarea.tpl`) deep-links customers to the
MaburVM **client portal** (`/client/vms/<id>` and `/client/vms/<id>/console`)
for day-to-day operation (start/stop/reboot/console). The webhook deliberately
exposes only billing lifecycle events, so real-time VM control lives in the
portal rather than being duplicated in WHMCS.

## Notes / limitations

- **Idempotency on the panel side is currently in-memory** and is cleared on
  panel restart. Retried WHMCS events are still safe (the operations are
  effectively idempotent), but for exactly-once semantics across restarts the
  panel should persist idempotency records (planned).
- The module targets WHMCS 7.7+ (uses `serviceProperties`). `logModuleCall` and
  `serviceProperties` are WHMCS runtime globals — static PHP linters flag them as
  undefined; that is expected.
