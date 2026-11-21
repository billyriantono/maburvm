<?php

/**
 * Receiver for the events a MaburVM panel sends (System settings, API, webhook
 * URL). Each event is a signed JSON POST:
 *
 *   X-Webhook-Signature: <hex HMAC-SHA256(rawBody, secret)>
 *   Body: {"id","event","sent_at",...}
 *
 * What is done with each event:
 *   floating_ip.usage  keeps one recurring billable item per client at the
 *                      current count of billable addresses
 *   vm.created         writes the VM's address and login user into the service
 *   vm.failed          records the failure in the activity log (sent at the
 *                      first failure, and again if every retry fails)
 *   bandwidth.overage  records it in the activity log (the charge is WHMCS's
 *                      own overage billing, fed by the module's usage sync)
 *   anything else      is recorded and acknowledged
 *
 * An event is applied once: its id is remembered, and a repeat is answered 200.
 */

if (!defined('WHMCS')) {
    die('This file cannot be accessed directly');
}

use WHMCS\Database\Capsule;

const MABURVM_EVENTS_MODULE = 'maburvm_events';

/** One of the addon's settings. */
function maburvm_events_setting($name, $default = '')
{
    $value = Capsule::table('tbladdonmodules')->where('module', MABURVM_EVENTS_MODULE)->where('setting', $name)->value('value');
    return $value === null || trim((string) $value) === '' ? $default : trim((string) $value);
}

/** Creates the addon's two tables when they are missing. */
function maburvm_events_ensureTables()
{
    $schema = Capsule::schema();
    if (!$schema->hasTable('mod_maburvm_events')) {
        $schema->create('mod_maburvm_events', function ($table) {
            $table->string('id', 64)->primary();
            $table->string('event', 64);
            $table->text('summary');
            $table->dateTime('received_at');
        });
    }
    if (!$schema->hasTable('mod_maburvm_floating')) {
        $schema->create('mod_maburvm_floating', function ($table) {
            $table->integer('client_id')->primary();
            $table->integer('billable_item_id')->default(0);
            $table->integer('quantity')->default(0);
            $table->integer('sent_at')->default(0);
        });
    }
}

/**
 * The secrets an event may be signed with: the addon's own when set, else the
 * Access Hash of every MaburVM server (the secret the module signs with).
 */
function maburvm_events_secrets()
{
    $own = maburvm_events_setting('webhook_secret');
    if ($own !== '') {
        return [$own];
    }
    $secrets = [];
    foreach (Capsule::table('tblservers')->where('type', 'maburvm')->get() as $server) {
        $hash = trim((string) $server->accesshash);
        if ($hash !== '') {
            $secrets[] = $hash;
        }
    }
    return $secrets;
}

function maburvm_events_verify($raw, $signature)
{
    $given = strtolower(preg_replace('/^sha256=/i', '', trim((string) $signature)));
    if ($given === '') {
        return false;
    }
    foreach (maburvm_events_secrets() as $secret) {
        if (hash_equals(hash_hmac('sha256', $raw, $secret), $given)) {
            return true;
        }
    }
    return false;
}

/**
 * Check and apply one event.
 *
 * @return array [int $httpStatus, array $body]
 */
function maburvm_events_handle($raw, $signature)
{
    if (!maburvm_events_verify($raw, $signature)) {
        return [401, ['success' => false, 'message' => 'Invalid signature']];
    }
    $event = json_decode($raw, true);
    if (!is_array($event) || empty($event['event']) || !is_string($event['event'])) {
        return [400, ['success' => false, 'message' => 'Not an event']];
    }
    maburvm_events_ensureTables();
    // A panel from before delivery ids sends none: the body stands in for it.
    $id = !empty($event['id']) && is_string($event['id']) ? substr($event['id'], 0, 64) : hash('sha256', $raw);
    if (Capsule::table('mod_maburvm_events')->where('id', $id)->exists()) {
        return [200, ['success' => true, 'duplicate' => true, 'message' => 'Already received']];
    }
    try {
        $summary = maburvm_events_apply($event);
    } catch (\Throwable $e) {
        // Not remembered, so the panel's retry applies it.
        logActivity('MaburVM events: ' . $event['event'] . ' could not be applied: ' . $e->getMessage());
        return [500, ['success' => false, 'message' => $e->getMessage()]];
    }
    Capsule::table('mod_maburvm_events')->insert([
        'id' => $id,
        'event' => substr($event['event'], 0, 64),
        'summary' => $summary,
        'received_at' => date('Y-m-d H:i:s'),
    ]);
    return [200, ['success' => true, 'message' => $summary]];
}

/** Applies an event and says, in one line, what was done. */
function maburvm_events_apply(array $event)
{
    switch ($event['event']) {
        case 'floating_ip.usage':
            return maburvm_events_floatingIps($event);
        case 'vm.created':
            return maburvm_events_vmCreated($event);
        case 'vm.failed':
            $service = maburvm_events_service($event);
            $what = $service ? 'service #' . $service->id : 'VM ' . ($event['vm_id'] ?? '?');
            $reason = isset($event['reason']) ? (string) $event['reason'] : 'no reason given';
            $still = !empty($event['retrying']) ? ' (the panel is still trying)' : '';
            logActivity('MaburVM: the VM of ' . $what . ' could not be installed: ' . $reason . $still, $service ? (int) $service->userid : 0);
            return 'recorded the failed installation of ' . $what;
        case 'bandwidth.overage':
            $service = maburvm_events_service($event);
            $what = $service ? 'service #' . $service->id : 'VM ' . ($event['vm_id'] ?? '?');
            logActivity(sprintf('MaburVM: %s is over its transfer quota (%s GB used of %s GB)', $what,
                $event['used_gb'] ?? '?', $event['quota_gb'] ?? '?'), $service ? (int) $service->userid : 0);
            return 'recorded the overage of ' . $what;
    }
    return 'recorded';
}

/**
 * The WHMCS service an event is about, from its external_ref (whmcs-<service
 * id>). Null unless it is a service of a MaburVM product.
 */
function maburvm_events_service(array $event)
{
    if (empty($event['external_ref']) || !preg_match('/^whmcs-(\d+)$/', (string) $event['external_ref'], $m)) {
        return null;
    }
    $service = Capsule::table('tblhosting')->where('id', (int) $m[1])->first();
    if (!$service) {
        return null;
    }
    $module = Capsule::table('tblproducts')->where('id', $service->packageid)->value('servertype');
    return $module === 'maburvm' ? $service : null;
}

/**
 * The WHMCS client behind a panel account: the reference the module gave the
 * panel (whmcs-client-<id>), else a service that carries the panel user id.
 */
function maburvm_events_clientId(array $event)
{
    if (!empty($event['user_ref']) && preg_match('/^whmcs-client-(\d+)$/', (string) $event['user_ref'], $m)
        && Capsule::table('tblclients')->where('id', (int) $m[1])->exists()) {
        return (int) $m[1];
    }
    if (empty($event['user_id'])) {
        return 0;
    }
    $fieldIds = Capsule::table('tblcustomfields')->where('type', 'product')
        ->whereIn('fieldname', ['Panel User ID', 'panel_user_id'])->pluck('id');
    $fieldIds = is_array($fieldIds) ? $fieldIds : $fieldIds->all();
    if (!$fieldIds) {
        return 0;
    }
    $value = Capsule::table('tblcustomfieldsvalues')->whereIn('fieldid', $fieldIds)->where('value', (string) $event['user_id'])->first();
    if (!$value) {
        return 0;
    }
    return (int) Capsule::table('tblhosting')->where('id', $value->relid)->value('userid');
}

/**
 * Keeps the client's recurring "floating IP addresses" billable item at the
 * count the panel reports. The event is a snapshot, so applying it twice, or
 * missing one, leaves the right charge.
 * ponytail: no proration and no currency conversion; the monthly invoice takes
 * the count and the price as they stand that day.
 */
function maburvm_events_floatingIps(array $event)
{
    $clientId = maburvm_events_clientId($event);
    if (!$clientId) {
        logActivity('MaburVM events: floating IP usage for panel account ' . ($event['user_id'] ?? '?') . ' matches no client; nothing was charged');
        return 'ignored: no client matches this panel account';
    }
    // One event at a time per client: two applied together would each add a charge.
    if (!Capsule::table('mod_maburvm_floating')->where('client_id', $clientId)->exists()) {
        try {
            Capsule::table('mod_maburvm_floating')->insert(['client_id' => $clientId, 'billable_item_id' => 0, 'quantity' => 0, 'sent_at' => 0]);
        } catch (\Throwable $e) {
            // another event for this client made the row first
        }
    }
    return Capsule::connection()->transaction(function () use ($event, $clientId) {
        $row = Capsule::table('mod_maburvm_floating')->where('client_id', $clientId)->lockForUpdate()->first();
        $count = max(0, (int) ($event['billable'] ?? 0));
        $sentAt = !empty($event['sent_at']) && strtotime((string) $event['sent_at']) ? strtotime((string) $event['sent_at']) : time();
        if ($row && (int) $row->sent_at > $sentAt) {
            return 'ignored: a newer count is already applied for client #' . $clientId;
        }
        $itemId = $row ? (int) $row->billable_item_id : 0;
        if ($itemId && !Capsule::table('tblbillableitems')->where('id', $itemId)->exists()) {
            $itemId = 0; // staff removed it; a new one is made when there is something to charge
        }

        $price = (float) maburvm_events_setting('floating_ip_price', '0');
        if ($price <= 0 || $count === 0) {
            // Also when the price was set back to 0: an item left behind would go on charging.
            if ($itemId) {
                Capsule::table('tblbillableitems')->where('id', $itemId)->delete();
                $itemId = 0;
            }
            $summary = $price <= 0
                ? sprintf('client #%d has %d billable floating IP address(es); no price is set, so nothing is charged', $clientId, $count)
                : sprintf('client #%d has no billable floating IP addresses; the recurring charge is removed', $clientId);
        } else {
            $description = sprintf('%s x %d', maburvm_events_setting('floating_ip_description', 'Floating IP addresses'), $count);
            $amount = round($price * $count, 2);
            if ($itemId) {
                Capsule::table('tblbillableitems')->where('id', $itemId)->update(['description' => $description, 'amount' => $amount]);
            } else {
                $result = localAPI('AddBillableItem', [
                    'clientid' => $clientId,
                    'description' => $description,
                    'amount' => $amount,
                    'invoiceaction' => 'recur',
                    'recur' => 1,
                    'recurcycle' => 'Months',
                    'recurfor' => 999,
                    'duedate' => date('Y-m-d'),
                ]);
                if (!is_array($result) || ($result['result'] ?? '') !== 'success' || empty($result['billableid'])) {
                    throw new \RuntimeException('AddBillableItem failed: ' . (is_array($result) && isset($result['message']) ? $result['message'] : 'no answer'));
                }
                $itemId = (int) $result['billableid'];
            }
            $summary = sprintf('client #%d is charged %s monthly for %d floating IP address(es)', $clientId, number_format($amount, 2, '.', ''), $count);
        }

        Capsule::table('mod_maburvm_floating')->where('client_id', $clientId)
            ->update(['billable_item_id' => $itemId, 'quantity' => $count, 'sent_at' => $sentAt]);
        logActivity('MaburVM: ' . $summary, $clientId);
        return $summary;
    });
}

/** Writes the new VM's address and login user into its service. */
function maburvm_events_vmCreated(array $event)
{
    $service = maburvm_events_service($event);
    if (!$service) {
        return 'ignored: not a VM of a MaburVM service here';
    }
    $fields = [];
    if (!empty($event['ip'])) {
        $fields['dedicatedip'] = (string) $event['ip'];
        $ips = isset($event['ips']) && is_array($event['ips']) ? $event['ips'] : [];
        $fields['assignedips'] = implode("\n", array_values(array_diff(array_map('strval', $ips), [$fields['dedicatedip']])));
    }
    if (!empty($event['username'])) {
        $fields['username'] = (string) $event['username'];
    }
    if ($fields) {
        Capsule::table('tblhosting')->where('id', $service->id)->update($fields);
    }
    return 'service #' . $service->id . ' has its address and login user';
}
