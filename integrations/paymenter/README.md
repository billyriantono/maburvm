# MaburVM for Paymenter

A server extension that lets [Paymenter](https://paymenter.org) create and
manage KVM VMs on a MaburVM panel.

| Path | What it is |
|------|------------|
| `extensions/Servers/Maburvm/Maburvm.php` | The server extension, copied into Paymenter |
| `tests/` | Development only; never copied into Paymenter |

> **Unverified against a real Paymenter install.** The extension is written
> against Paymenter's documented server extension interface (`getConfig`,
> `getProductConfig`, `testConfig`, `createServer`, `suspendServer`,
> `unsuspendServer`, `terminateServer`, `upgradeServer`, `getActions`) and is
> covered by unit tests that run it against stand-ins for Paymenter's classes,
> and by a panel test that runs it against the real webhook handler. It has not
> been run inside Paymenter. Walk through the
> [checklist](#checklist-for-a-first-run) on a throwaway Paymenter before
> relying on it, and treat anything on it that fails as a bug in the extension.

## What it does

Every action is a signed request to the panel's billing webhook
(`POST /webhooks/billing`), the same one the WHMCS module uses:

| Paymenter action | Webhook event |
|------------------|---------------|
| Create | `user.ensure` (finds or makes the customer's panel account by email), then `vm.create` |
| Suspend / Unsuspend | `vm.suspend` / `vm.unsuspend` |
| Terminate | `vm.destroy` (a VM already gone counts as terminated) |
| Upgrade (change of product) | `vm.resize` |
| Service page button "Open control panel" | `user.sso`: a sign-in link that lasts a minute and works once |
| Test configuration | `catalog.list` |

Every event about a VM names the VM, its owner's panel account and the
service (`external_ref` `paymenter-<service id>`); the panel refuses one whose
VM belongs to another account or another service. The panel account and VM
are kept on the service as the properties `maburvm_user_id` and
`maburvm_vm_id`. A second Create on a service that already has a VM orders
nothing, and the create request carries an idempotency key, so a retry never
provisions twice.

## Install

1. Copy `extensions/Servers/Maburvm/` to `<paymenter>/extensions/Servers/Maburvm/`.
2. In the panel, under System settings, Panel, Billing webhook, save a billing
   API key and a signing secret (or set `BILLING_API_KEY` and
   `BILLING_WEBHOOK_SECRET`).
3. In Paymenter's admin area, add a server using the **Maburvm** extension:
   - *Panel URL*: the panel's address, for example `https://panel.example.com`.
   - *Billing API key* and *Billing webhook secret*: the two values from step 2.
   - Test it. The test sends a signed request, so it passes only when the URL,
     both values and the clock (within five minutes of the panel's) are right.
4. On each product, set *Template ID* (a template UUID from the panel) and
   either *Plan ID* (a plan UUID: it decides CPU, memory, disk, speed cap and
   transfer quota) or *vCPU*, *Memory (MB)* and *Disk (GB)*. *Region* is
   optional. A checkout option with the same name (`template_id`, `plan_id`,
   `region`, `hostname`...) wins over the product's value.

## Tests

```bash
cd integrations/paymenter/tests
composer install
vendor/bin/phpunit
```

The panel's Go test `TestPaymenterAndFOSSBillingTalkToTheRealWebhook`
(`internal/panel/handler/billing_modules_integration_test.go`) also runs the
extension against the real webhook handler; it needs `php` on the path.

## Checklist for a first run

Use a throwaway Paymenter and a panel with a test node.

- [ ] The extension appears under Servers, and its test passes; it fails with a
      wrong secret.
- [ ] An order creates a panel account with the customer's email and a VM on
      it; the service has `maburvm_user_id` and `maburvm_vm_id`.
- [ ] Ordering again from the same customer lands in the same panel account.
- [ ] Suspend powers the VM off and the customer cannot start it; Unsuspend
      boots it.
- [ ] Changing the product resizes the VM.
- [ ] Terminate removes the VM; terminating again does not fail.
- [ ] The service page shows "Open control panel", and it signs the customer
      in to the VM in the panel once.
