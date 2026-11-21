# MaburVM for WHMCS

Two modules, each copied into a WHMCS install:

| Folder | What it is | What it does |
|--------|------------|--------------|
| `modules/servers/maburvm/` | Provisioning module | Orders, suspends, resizes and removes VMs on a MaburVM panel as WHMCS services change |
| `modules/addons/maburvm_events/` | Addon module (optional) | Receives the events the panel sends back: bills floating IP addresses, fills in a new VM's address |

`tests/` is for development only and is never copied into WHMCS.

> **Not yet proven inside WHMCS.** Both modules are covered by unit tests that
> run their code against stand-ins for WHMCS and against the panel's real
> webhook handler, but neither has been run inside a WHMCS install. Walk
> through the [checklist](#checklist-for-a-first-run-in-whmcs) on a throwaway
> WHMCS before relying on them, and treat anything on it that fails as a bug in
> the module.

## The provisioning module

On WHMCS lifecycle events the module calls the panel's signed **billing
webhook** (`POST /webhooks/billing`):

| WHMCS action        | Webhook event  |
|---------------------|----------------|
| Create/Accept order | `user.ensure`, then `vm.create`, then `vm.status` until the VM is ready |
| Suspend             | `vm.suspend`   |
| Unsuspend           | `vm.unsuspend` |
| Terminate           | `vm.destroy`   |
| Upgrade/Downgrade   | `vm.resize`    |
| Change Password     | `vm.reset_password` |
| Usage update (cron) | `vm.usage` (and `vm.status` for a service with no address yet) |
| Start / Stop / Reboot buttons | `vm.start` / `vm.stop` / `vm.restart` |
| Test Connection, product settings lists | `catalog.list` |

A created VM gets a public address from the node's pools and a root password:
the service's own password, or one the module generates and saves on the
service when that one is shorter than 8 characters. An order fails with a clear
message when no pool on the chosen node has a free address.

Suspend powers the VM off and the customer cannot start it until it is
unsuspended. An upgrade shuts a running VM down, applies the new CPU and memory,
and boots it again; the disk only ever grows. With a plan on the product an
upgrade also applies the plan's speed cap and transfer quota, and a change of
those alone does not restart the VM. Change Password needs a running VM with
the guest agent. The usage update fills bandwidth used and allowed and the disk
allowance; disk used is not reported.

Requests are authenticated with a shared **API key** (`X-API-Key`) and an
**HMAC-SHA256 signature** (`X-Webhook-Signature: sha256=<hex>`) over the raw JSON
body, and an `X-Idempotency-Key` so retries don't double-provision.

Replay protection is always on. The signed body carries a `timestamp`, which
the panel requires and refuses when it is more than 5 minutes from its own
clock, and a random `nonce`. Each signed request is accepted once; a second
copy answers `409`. Keep the WHMCS server's clock synchronised (NTP): a clock
more than 5 minutes off makes every action fail with a message that says so.

### Install

1. Copy `modules/servers/maburvm/` into your WHMCS install at
   `<whmcs>/modules/servers/maburvm/`.
2. **Setup → Products/Services → Servers → Add New Server**:
   - *Hostname*: the panel host (e.g. `panel.example.com`), *Secure*: checked for HTTPS.
   - *Module*: `MaburVM`.
   - *Password* field → the panel **billing API key** (`BILLING_API_KEY`).
   - *Access Hash* field → the panel **webhook secret** (`BILLING_WEBHOOK_SECRET`).
   - Click **Test Connection**. It sends a signed request, so it passes only
     when the API key, the secret and the clock are all right.
3. Create a **Product** using module `MaburVM` and set the module fields. Where
   the panel can be reached, *Template ID*, *Plan ID* and *Region* are lists of
   what the panel offers; otherwise they are text fields that take the same
   values.
   - *Template ID*: a MaburVM OS template UUID.
   - *vCPU*, *Memory (MB)*, *Disk (GB)*: the size, when no plan is set.
   - *Plan ID* (optional): a MaburVM plan UUID. When set, the plan decides
     CPU, memory and disk as well as the speed cap and monthly transfer quota,
     and the three fields above are ignored. Without it the VM has no speed
     cap and no transfer quota.
   - *Region* (optional): the short name of a region. Empty places the VM in
     the panel's default region when one is set (System settings), and
     anywhere when none is.
   - *Provisioning wait (seconds)*: how long Create waits for the VM to finish
     installing. See [What WHMCS learns about the VM](#what-whmcs-learns-about-the-vm).

> The panel must be started with `BILLING_API_KEY` and `BILLING_WEBHOOK_SECRET`
> set (the same values entered above), otherwise the webhook rejects requests.

### Letting the customer choose

A **configurable option** on the product overrides the product's own setting
when its name is one of these (case does not matter). The option's values are
what the panel takes, so name them `value|What the customer sees`, for example
an option `Region` with the choices `jakarta|Jakarta` and `singapore|Singapore`.

| Option name | Value | Overrides |
|-------------|-------|-----------|
| `Template` or `Operating System` | template UUID | Template ID |
| `Plan` | plan UUID | Plan ID |
| `Region` or `Location` | region short name | Region |
| `vCPU`, `Memory`, `Disk` | a number (cores, MB, GB) | the size, when no plan is set |
| `Bandwidth` | Mbps | the plan's speed cap |
| `Traffic` | GB per month | the plan's transfer quota |

`Bandwidth` and `Traffic` apply at Create, and on an upgrade only together
with a plan. Extra IP addresses are not ordered through a configurable option:
customers add floating IP addresses in the panel's portal, and the events addon
bills them.

### Licensed systems and plan-only templates

A template in the panel can be marked **Needs a paid licence** and can be
limited to chosen plans (Templates, a template, Edit).

- A template limited to plans is refused, for customers and for WHMCS orders
  alike, on any other plan. The order fails with "this operating system is not
  offered on this plan", so offer such a template only on products or options
  that use one of its plans.
- A licensed template with no plans ticked cannot be picked by customers in
  the portal at all (new order, reinstall or clone). WHMCS can still order it:
  price it in WHMCS, on its own product or as a priced `Operating System`
  option. A customer whose VM already runs it may reinstall the same system.

The product settings list marks licensed templates "(licensed)".

### Panel accounts for WHMCS clients

Nothing is pasted per order. At Create the module asks the panel for the
customer account of the WHMCS client (`user.ensure`): the panel looks for the
client's own reference (`whmcs-client-<client id>`), then for an account with
the client's email, and otherwise creates one. A new account is emailed a link
to choose its password (the "Welcome (billing order)" email, which needs SMTP
and `PANEL_PUBLIC_URL` set on the panel; without it the customer uses "Forgot
password" on the panel's sign-in page). The account's ID is kept on the service
as `panel_user_id`.

- Because the reference is tried first, a client who later changes their email
  in WHMCS keeps the same panel account.
- An email that belongs to a panel administrator is refused: staff accounts
  are never given to a billing order.
- Matching by email means a client who registers in WHMCS with somebody
  else's address gets VMs placed in that person's panel account. Turn on email
  verification in WHMCS if signups are open.

**Override.** A service custom field named exactly `Panel User ID`, when
filled in, is used as it is and the panel is not asked. This is how services
created before this version keep working, and how staff place a VM in a chosen
account. Make the field *Admin Only*: a customer must not be able to type
another account's ID at checkout.

### What the module stores on a service

| Where | Value |
|-------|-------|
| service property `vm_id` | the panel's VM ID |
| service property `panel_user_id` | the panel account the VM belongs to |
| Dedicated IP, Assigned IPs, Username | the VM's first address, its others, and `root` / `Administrator` / `admin` |
| Password | unchanged, unless it was shorter than 8 characters |

Service properties are kept by WHMCS in custom fields of the same name, which
it creates on first use. A custom field named `VM ID` is still read as a
fallback for the VM.

Every action on an existing VM (suspend, unsuspend, terminate, package change,
password change, start, stop, restart) is sent with the service's panel user
ID. The panel refuses the action, and writes an audit entry, when that user
does not own the VM, so a wrong or copied VM ID cannot touch another customer's
VM. A service with a VM but no panel user (neither stored nor in
`Panel User ID`) cannot act on its VM until the field is filled in; an action
on an existing VM never creates an account.

### What WHMCS learns about the VM

Create does not report success the moment the order is queued. It writes the
VM's address and login user into the service, then asks the panel for the
VM's state every 5 seconds for up to *Provisioning wait* seconds (45 by
default):

- **Ready**: Create succeeds.
- **Failed**: Create returns the panel's reason as an error, so the service
  stays Pending and the failure is in the module queue. The panel retries a
  failed installation by itself, with growing pauses, and the message says
  when it is still trying: run Create again later to see how it ended (it
  never orders a second VM), or Terminate the service to give up. When the
  panel has given up, Terminate the service to remove the failed VM, then run
  Create again.
- **Still installing when the wait ends**: Create succeeds; the address is
  already saved. A failure after that point is not seen by Create. With the
  events addon it is written to the client's activity log at the first failed
  try; without the addon nothing tells WHMCS, and the VM shows as failed in
  the panel and in the client area.

A VM that was installed and later left in an error state by some other
operation (a start or a reinstall that failed) is not a failed order. Create
on such a service says the VM needs attention on the panel and changes nothing.

Running Create again on a service that already has a VM never orders a second
one: it reads that VM's state and finishes the first order.

Keep the wait below your web server's request timeout (a stock nginx with
PHP-FPM gives up after 60 seconds), or Create started from the admin area
times out in the browser while it is still waiting. Large images such as
Windows can take longer than any sensible wait; use the events addon for them.

### The VM's link to its service

The panel keeps `whmcs-<service id>` on the VM as its billing reference, and
the plan when the product names one. Administrators see both on the VM's page
and can correct them there. From then on:

- An action from a different service of the same customer is refused (`403`)
  and audited, as an action for another customer's VM already was. Events for a
  VM with no reference on record are checked against the owner only.
- The customer cannot delete the VM in the portal: the service would go on
  being charged. They cancel in WHMCS, and Terminate removes it. To let
  customers delete such VMs anyway, turn on "Delete servers that billing
  manages" under System settings, Quotas.

VMs created before this, or by hand, have no reference. Set one on the VM's
page (`whmcs-<service id>`) to bring it under these rules.

Hostnames need only be unique among one customer's own VMs, and an order from
WHMCS is never refused over a name.

### Suspending a customer account

Two more events lock a customer out of the panel without touching a single
service: `user.suspend` and `user.unsuspend`, with `data.user_id` and optional
`data.with_vms` (true also suspends, or restores, all of the customer's VMs).
A suspended account cannot sign in, and its open sessions and API keys stop
working at once. The module does not send them (WHMCS has no per-client action
on a server module); call them from a hook such as `ClientClose`, or use the
Suspend account button on the user's page in the panel.

### Orders and the account quota

A VM this module orders is never held back by the customer's account quota in
the panel: the quota limits what the customer adds on their own in the client
portal. What customers may do there on their own (order, clone, add disks) is
switched under System settings, Quotas.

### Client area

The service detail page (`templates/clientarea.tpl`) shows the VM's state,
address and login user, read live from the panel, and deep-links customers to
the MaburVM **client portal** (`/client/vms/<id>` and
`/client/vms/<id>/console`) for day-to-day operation (start/stop/reboot/
console). Real-time VM control lives in the portal rather than being
duplicated in WHMCS.

## The events addon

The panel sends an event to a webhook URL when something a billing system
cares about happens. The addon is the receiving end.

| Event | Sent when | What the addon does |
|-------|-----------|---------------------|
| `floating_ip.usage` | a customer orders, releases, attaches or detaches a floating IP | keeps one recurring billable item per client at the current billable count |
| `vm.created` | a VM has finished installing | writes its address and login user into the service |
| `vm.failed` | a VM's installation fails: once at the first failed try (`retrying: true`, the panel tries again by itself) and once more if every retry fails | adds the reason to the client's activity log |
| `bandwidth.overage` | a VM on an "overage" plan crosses its transfer quota | adds it to the client's activity log |
| `vm.suspended`, `vm.unsuspended`, `vm.destroyed` | a VM is suspended, restored, or its deletion is accepted, by anyone | recorded only |

Each event is a JSON `POST` with `X-Webhook-Signature: <hex HMAC-SHA256 of the
body>`, `X-Webhook-Event` and `X-Webhook-Id`. The body always has `id`,
`event` and `sent_at`. VM events add `vm_id`, `user_id`, `hostname`, `status`,
`ip`, `ips`, `username`, and when known `external_ref` (`whmcs-<service id>`),
`plan_id` and `user_ref` (`whmcs-client-<client id>`).

The panel stores every event before sending it and retries one that is not
answered with a 2xx, up to 10 times over about four hours. Administrators see
each event and its fate under System settings, API, "Events sent to your
billing system", and can send a failed one again. A delivery that fails for
good also raises an operator alert.

### Install

1. Copy `modules/addons/maburvm_events/` to
   `<whmcs>/modules/addons/maburvm_events/`.
2. **Setup → Addon Modules**: activate *MaburVM events*, then configure:
   - *Floating IP price per month*: charged per billable address. `0` (the
     default) records the count and charges nothing.
   - *Floating IP invoice line*: the text of the billable item.
   - *Signing secret*: leave empty when the panel signs with
     `BILLING_WEBHOOK_SECRET` (the Access Hash of the MaburVM server). Fill it
     in only when an HMAC secret of its own is saved in the panel under System
     settings, API.
3. Open the addon's page (Addons → MaburVM events). It shows the address to
   save as the **webhook URL** in the panel under System settings, API:
   `https://<whmcs>/modules/addons/maburvm_events/callback.php`.

### How charges are made

- **Floating IPs.** One address attached to a VM is free; the panel reports
  the rest as `billable`. The addon keeps a single recurring *billable item*
  per client ("Floating IP addresses x N", monthly) at `N × price`, updates it
  when the count changes and deletes it at zero. The event is a snapshot, not a
  change, so a repeated or missed event cannot charge twice or leave a stale
  count; an older snapshot arriving late is ignored. There is no proration, and
  the price is used as it stands in each client's currency. A price changed in
  the addon's settings reaches a client's item the next time their count
  changes; set back to `0`, it removes the item then.
- **Transfer overage.** Not charged by the addon. WHMCS's own overage billing
  on the product does it, from the usage figures the provisioning module's
  daily usage update writes. The event is only a record that the quota was
  crossed.
- A panel account with no WHMCS client (one staff made by hand) is
  acknowledged and not charged; the activity log says so.

Events are applied once: each event's ID is remembered in `mod_maburvm_events`
and a repeat is answered `200` without doing anything. The addon's two tables
are kept when it is deactivated.

## Notes / limitations

- **Create and terminate are idempotent**: the panel stores their result
  (Postgres `billing_idempotency` table) for 24 hours, so a retried webhook
  returns the original response even across panel restarts. Every other event
  runs each time it is sent. A stored create result is dropped once its VM has
  been destroyed, so Terminate followed by Create on the same service
  provisions a new VM.
- The modules target WHMCS 7.7+ (`serviceProperties`, config option loaders).
  `logModuleCall`, `logActivity`, `localAPI`, `encrypt` and `Capsule` are WHMCS
  runtime globals; static PHP linters flag them as undefined, which is expected.
- There is no single sign-on from WHMCS into the panel: the customer signs in
  to the panel with the account described above.
- Module settings are stored by position. New settings are only ever added
  after the existing ones, so a product saved by an earlier version keeps its
  values.

## Tests

```bash
cd integrations/whmcs/tests
composer install
vendor/bin/phpunit
```

The tests load both modules with stand-ins for WHMCS's functions, database
and service properties (`bootstrap.php`) and a scripted panel that checks every
request's API key, signature, timestamp and nonce the way the real one does
(`src/FakePanel.php`). The panel's own Go tests complete the loop from the
other side: they run the module's PHP against the real webhook handler, and
feed events exactly as the panel signs them to the addon's PHP
(`internal/panel/handler/billing_integration_test.go`,
`internal/panel/service/billing_integration_test.go`; both need `php` on the
path and are skipped without it).

What these cannot show is how WHMCS itself behaves: when it calls each
function, what it passes, how it stores service properties, and what its cron
does with a recurring billable item. That is what the checklist is for.

## Checklist for a first run in WHMCS

Use a throwaway WHMCS and a panel with a test node. Tick each line only when
you have seen the result yourself.

Setup
- [ ] The server's **Test Connection** passes, and fails with a wrong Access Hash.
- [ ] The product's Module Settings show Template, Plan and Region as lists of
      the panel's own; saving and reopening keeps the choices.
- [ ] A product saved before this version still shows its Template ID, size
      and Plan ID in the same fields.

Create
- [ ] An order from a client who has no panel account: the account appears in
      the panel (Users) with the client's email, and the client receives the
      "choose a password" email.
- [ ] The service has `vm_id` and `panel_user_id` (custom fields WHMCS created,
      visible to staff only), and Dedicated IP, Username and Password are
      filled. The password signs in to the VM.
- [ ] A second order from the same client lands in the same panel account,
      also after changing the client's email in WHMCS.
- [ ] A product with a Region puts the VM on a node of that region; an unknown
      region fails the order with "region not found".
- [ ] Configurable options named `Operating System`, `Plan` and `Region`
      override the product's settings.
- [ ] An order the panel cannot fulfil (no free address, template not offered
      on the plan) leaves the service Pending with the panel's reason in the
      module queue.
- [ ] Stop the node's agent, order, and confirm Create reports the failure, or
      that after the wait the events addon logs it. Terminate, start the agent,
      Create again: a new VM is made.
- [ ] Click Create twice on one service: there is still one VM.
- [ ] The client area of a service loads within a few seconds while the panel
      is unreachable, and says the status could not be read.

Lifecycle
- [ ] Suspend powers the VM off and the customer cannot start it; Unsuspend
      boots it.
- [ ] Upgrade to a product with a larger plan: the VM restarts with the new
      size, speed cap and quota.
- [ ] Change Password sets the root password on a running VM.
- [ ] Terminate removes the VM; its address returns to the pool.
- [ ] A service created by the previous version of the module (with
      `Panel User ID` filled in) can still be suspended, unsuspended and
      terminated.
- [ ] The daily cron fills bandwidth usage on the service.
- [ ] The client area shows the VM's state and address, and its buttons open
      the portal.

Events addon
- [ ] After activation the addon's page shows the callback URL, and a `curl`
      POST to it without a signature answers `401`.
- [ ] With the URL saved in the panel, ordering a second floating IP makes one
      recurring billable item for the client; a third changes its amount;
      releasing down to one deletes it.
- [ ] The next invoice run invoices the billable item once, and the following
      month again. (This is the part no test here covers: how WHMCS's cron
      treats the recurring item.)
- [ ] The panel's "Events sent to your billing system" list shows each event
      as Delivered. Put WHMCS in maintenance mode, cause an event, and confirm
      it shows Retrying and is delivered after WHMCS is back.
- [ ] A new VM's address appears on its service even when Create stopped
      waiting before the VM was ready.
