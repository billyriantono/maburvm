# MaburVM for FOSSBilling

A server manager that lets [FOSSBilling](https://fossbilling.org) create and
manage KVM VMs on a MaburVM panel through its hosting products.

| Path | What it is |
|------|------------|
| `library/Server/Manager/Maburvm.php` | The server manager, copied into FOSSBilling |
| `tests/` | Development only; never copied into FOSSBilling |

> **Unverified against a real FOSSBilling install.** The manager is written
> against FOSSBilling's documented server manager interface
> (`Server_Manager`: `testConnection`, `synchronizeAccount`, `createAccount`,
> `suspendAccount`, `unsuspendAccount`, `cancelAccount`,
> `changeAccountPackage`, `changeAccountPassword`, `getLoginUrl`...) and is
> covered by unit tests that run it against stand-ins for FOSSBilling's
> classes, and by a panel test that runs it against the real webhook handler.
> It has not been run inside FOSSBilling. Walk through the
> [checklist](#checklist-for-a-first-run) on a throwaway FOSSBilling before
> relying on it, and treat anything on it that fails as a bug in the manager.

## What it does

Every action is a signed request to the panel's billing webhook
(`POST /webhooks/billing`), the same one the WHMCS module uses:

| FOSSBilling action | Webhook event |
|--------------------|---------------|
| Create (activate the order) | `user.ensure` (finds or makes the client's panel account by email), then `vm.create` |
| Suspend / Unsuspend | `vm.suspend` / `vm.unsuspend` |
| Cancel | `vm.destroy` (a VM already gone counts as cancelled) |
| Change package | `vm.resize` |
| Change password | `vm.reset_password` (the VM must be running) |
| Synchronize | `vm.status`: fills in the account's IP address |
| Control panel login | `user.sso`: a sign-in link that lasts a minute and works once |
| Test connection | `catalog.list` |

FOSSBilling keeps no field of its own for the VM, so each event names it by
the account's reference (`external_ref` `fossbilling-<account username>`)
together with its owner, which the panel finds by the client's email (and by
`fossbilling-client-<id>` where FOSSBilling passes the client's id). The panel
refuses an event whose VM belongs to another account. The account's password
becomes the VM's root password when it is at least 8 characters; otherwise the
panel makes one, shown to the customer in the panel. Changing an account's
username, domain or IP is refused: do that in the panel.

## Install

1. Copy `library/Server/Manager/Maburvm.php` to
   `<fossbilling>/library/Server/Manager/Maburvm.php`.
2. In the panel, under System settings, Panel, Billing webhook, save a billing
   API key and a signing secret (or set `BILLING_API_KEY` and
   `BILLING_WEBHOOK_SECRET`).
3. In FOSSBilling, add a hosting server with the **MaburVM** manager:
   - *Hostname*: the panel's address (`panel.example.com`, or a full
     `https://panel.example.com`); tick *Secure* for HTTPS, and set *Port*
     only when it is not the default.
   - *Username*: anything; it is not used.
   - *Password*: the billing API key. *Access hash*: the billing webhook secret.
   - Test the connection. It sends a signed request, so it passes only when the
     address, both values and the clock (within five minutes of the panel's)
     are right.
4. On each hosting plan, add the custom values `template_id` (a template UUID
   from the panel) and either `plan_id` (a plan UUID: it decides CPU, memory,
   disk, speed cap and transfer quota) or `cpu`, `memory` (MB) and `disk`
   (GB). `region` is optional.

## Tests

```bash
cd integrations/fossbilling/tests
composer install
vendor/bin/phpunit
```

The panel's Go test `TestPaymenterAndFOSSBillingTalkToTheRealWebhook`
(`internal/panel/handler/billing_modules_integration_test.go`) also runs the
manager against the real webhook handler; it needs `php` on the path.

## Checklist for a first run

Use a throwaway FOSSBilling and a panel with a test node.

- [ ] The server's connection test passes, and fails with a wrong access hash.
- [ ] Activating an order creates a panel account with the client's email and
      a VM carrying the reference `fossbilling-<username>`.
- [ ] A second order from the same client lands in the same panel account.
- [ ] Synchronize fills in the account's IP address.
- [ ] Suspend powers the VM off; Unsuspend boots it.
- [ ] Changing the package resizes the VM; changing the password sets the root
      password on the running VM.
- [ ] Cancel removes the VM; cancelling again does not fail.
- [ ] The client area's control panel login signs the client in to the VM in
      the panel once.
