<?php

/**
 * MaburVM provisioning module for WHMCS.
 *
 * Provisions and manages KVM VMs on a MaburVM panel from WHMCS product
 * lifecycle events (create / suspend / unsuspend / terminate). It talks to the
 * panel's signed billing webhook:
 *
 *   POST {API URL}/webhooks/billing
 *   Headers:
 *     X-API-Key:            <shared api key>
 *     X-Webhook-Signature:  sha256=<hex HMAC-SHA256(rawBody, webhook secret)>
 *     X-Webhook-Timestamp:  <RFC3339, within 5 minutes>
 *     X-Idempotency-Key:    <unique per logical operation>
 *   Body: {"event","timestamp","nonce","data":{...}}
 *
 * The panel checks the signed body: its timestamp must be within 5 minutes of
 * the panel's clock, and each signed request is accepted once. The nonce makes
 * every request distinct, so a deliberate repeat is never taken for a replay.
 *
 * The panel account that owns a service's VMs is found or created from the
 * WHMCS client (user.ensure) and remembered on the service. A "Panel User ID"
 * custom field, when filled in, is used instead (see README).
 *
 * Copy this folder to  <whmcs>/modules/servers/maburvm/  and add MaburVM as a
 * Server + Product (Module Settings -> MaburVM).
 */

if (!defined('WHMCS')) {
    die('This file cannot be accessed directly');
}

/** Seconds Create waits for the VM when the product does not say. */
const MABURVM_DEFAULT_WAIT = 45;
/** Seconds between two status checks while Create waits. */
const MABURVM_POLL_INTERVAL = 5;

/**
 * Module metadata.
 */
function maburvm_MetaData()
{
    return [
        'DisplayName' => 'MaburVM',
        'APIVersion' => '1.1',
        'RequiresServer' => true,
    ];
}

/**
 * Per-product configuration shown in the WHMCS product's Module Settings tab.
 * The order is fixed: WHMCS stores these by position (configoption1..7), so new
 * fields are only ever added at the end. Every field is marked for WHMCS's
 * simple view, so the three lists and the plain fields show together.
 */
function maburvm_ConfigOptions()
{
    return [
        'Template ID' => [
            'Type' => 'text',
            'Size' => '40',
            'Loader' => 'maburvm_loadTemplates',
            'SimpleMode' => true,
            'Description' => 'OS template to install (a template UUID)',
        ],
        'vCPU' => [
            'SimpleMode' => true,
            'Type' => 'text',
            'Size' => '5',
            'Default' => '1',
            'Description' => 'Number of virtual CPUs',
        ],
        'Memory (MB)' => [
            'SimpleMode' => true,
            'Type' => 'text',
            'Size' => '8',
            'Default' => '1024',
            'Description' => 'RAM in megabytes',
        ],
        'Disk (GB)' => [
            'SimpleMode' => true,
            'Type' => 'text',
            'Size' => '8',
            'Default' => '20',
            'Description' => 'Primary disk in gigabytes',
        ],
        'Plan ID' => [
            'Type' => 'text',
            'Size' => '40',
            'Loader' => 'maburvm_loadPlans',
            'SimpleMode' => true,
            'Description' => 'Optional. MaburVM plan UUID. When set, the plan decides CPU, memory, disk, speed cap and transfer quota, and the three fields above are ignored',
        ],
        'Region' => [
            'Type' => 'text',
            'Size' => '20',
            'Loader' => 'maburvm_loadRegions',
            'SimpleMode' => true,
            'Description' => 'Optional. Region to place the VM in (its short name). Empty uses the panel\'s default region',
        ],
        'Provisioning wait (seconds)' => [
            'SimpleMode' => true,
            'Type' => 'text',
            'Size' => '5',
            'Default' => (string) MABURVM_DEFAULT_WAIT,
            'Description' => 'How long Create waits for the VM to finish, so a failed installation is reported as a failure. 0 does not wait',
        ],
    ];
}

/**
 * Loaders for the product settings: WHMCS offers what they return as a list
 * and stores the key. When one fails, WHMCS falls back to a plain text field.
 */
function maburvm_loadTemplates(array $params)
{
    $out = [];
    foreach (maburvm_catalog($params)['templates'] as $t) {
        $out[$t['id']] = trim($t['name'] . ' ' . $t['version']) . (!empty($t['licensed']) ? ' (licensed)' : '');
    }
    return $out;
}

function maburvm_loadPlans(array $params)
{
    $out = ['' => 'None (use vCPU, memory and disk above)'];
    foreach (maburvm_catalog($params)['plans'] as $p) {
        $out[$p['id']] = sprintf('%s (%d vCPU, %d MB, %d GB)', $p['name'], $p['cpu'], $p['memory'], $p['disk']);
    }
    return $out;
}

function maburvm_loadRegions(array $params)
{
    $out = ['' => 'Panel default'];
    foreach (maburvm_catalog($params)['regions'] as $r) {
        $out[$r['slug']] = $r['name'];
    }
    return $out;
}

/**
 * The panel's templates, plans and regions.
 *
 * @throws \Exception when the panel cannot be asked
 */
function maburvm_catalog(array $params)
{
    list($code, $resp) = maburvm_sendEvent($params, 'catalog.list', new \stdClass(), 'whmcs-catalog-' . uniqid('', true));
    if ($code < 200 || $code >= 300 || empty($resp['success']) || !is_array($resp['data'] ?? null)) {
        throw new \Exception('MaburVM: could not read the catalog: ' . maburvm_errorMessage($resp, $code));
    }
    return $resp['data'] + ['templates' => [], 'plans' => [], 'regions' => []];
}

/**
 * One setting of the order. A configurable option the customer chose at
 * checkout (matched by name, ignoring case) wins over the product's own
 * setting at $index. Empty when neither is set.
 */
function maburvm_option(array $params, array $names, $index = 0)
{
    $chosen = isset($params['configoptions']) && is_array($params['configoptions']) ? $params['configoptions'] : [];
    foreach ($chosen as $name => $value) {
        if (in_array(strtolower(trim((string) $name)), $names, true) && trim((string) $value) !== '') {
            return trim((string) $value);
        }
    }
    return $index && isset($params['configoption' . $index]) ? trim((string) $params['configoption' . $index]) : '';
}

/**
 * The product's size as the panel takes it: the plan when one is set (it then
 * also carries the speed cap and transfer quota), otherwise the three numbers.
 * Bandwidth and Traffic configurable options override the plan's own.
 */
function maburvm_sizeData(array $params)
{
    $planId = maburvm_option($params, ['plan', 'plan id'], 5);
    $data = $planId !== '' ? ['plan_id' => $planId] : [
        'vm_specs' => [
            'cpu' => (int) maburvm_option($params, ['vcpu', 'cpu'], 2),
            'memory' => (int) maburvm_option($params, ['memory', 'memory (mb)', 'ram'], 3),
            'disk' => (int) maburvm_option($params, ['disk', 'disk (gb)'], 4),
        ],
    ];
    $bandwidth = (int) maburvm_option($params, ['bandwidth', 'bandwidth (mbps)']);
    if ($bandwidth > 0) {
        $data['bandwidth_mbps'] = $bandwidth;
    }
    $traffic = (int) maburvm_option($params, ['traffic', 'traffic (gb)']);
    if ($traffic > 0) {
        $data['data_quota_gb'] = $traffic;
    }
    return $data;
}

/**
 * Base URL of the panel (from the WHMCS Server row). Honours the "Secure"
 * checkbox and any hostname/IP configured on the server.
 */
function maburvm_apiBaseUrl(array $params)
{
    $scheme = !empty($params['serversecure']) ? 'https' : 'http';
    $host = $params['serverhostname'] !== '' ? $params['serverhostname'] : $params['serverip'];
    return rtrim($scheme . '://' . $host, '/');
}

/**
 * One value the module keeps on the service (a service property, which WHMCS
 * stores in a custom field of that name).
 */
function maburvm_property(array $params, $name)
{
    if (isset($params['model']) && $params['model'] !== null) {
        try {
            $value = $params['model']->serviceProperties->get($name);
            if (!empty($value)) {
                return trim((string) $value);
            }
        } catch (\Throwable $e) {
            // fall through to the custom fields WHMCS passed in
        }
    }
    return !empty($params['customfields'][$name]) ? trim((string) $params['customfields'][$name]) : '';
}

function maburvm_saveProperty(array $params, $name, $value)
{
    if (isset($params['model']) && $params['model'] !== null) {
        try {
            $params['model']->serviceProperties->save([$name => $value]);
        } catch (\Throwable $e) {
            logModuleCall('maburvm', 'saveProperty', [$name => $value], $e->getMessage(), $e->getTraceAsString());
        }
    }
}

/**
 * The panel account that owns this service's VMs: the "Panel User ID" custom
 * field when staff filled it in, else the one remembered on the service, else
 * (with $ensure) the account the panel finds or creates for the WHMCS client.
 * Only Create asks for an account: an action on a VM that exists must never
 * make a new account as a side effect.
 *
 * @throws \Exception when the panel refuses or cannot be reached
 */
function maburvm_panelUserId(array $params, $ensure = false)
{
    if (!empty($params['customfields']['Panel User ID'])) {
        return trim($params['customfields']['Panel User ID']);
    }
    $stored = maburvm_property($params, 'panel_user_id');
    if ($stored !== '' || !$ensure) {
        return $stored;
    }

    $client = isset($params['clientsdetails']) && is_array($params['clientsdetails']) ? $params['clientsdetails'] : [];
    $email = isset($client['email']) ? trim($client['email']) : '';
    if ($email === '') {
        throw new \Exception('MaburVM: the client has no email address, so no panel account can be made for this service.');
    }
    $clientId = !empty($params['userid']) ? $params['userid'] : ($client['userid'] ?? ($client['id'] ?? ''));
    $data = [
        'email' => $email,
        'name' => trim(($client['firstname'] ?? '') . ' ' . ($client['lastname'] ?? '')),
    ];
    if ($clientId !== '') {
        $data['external_ref'] = 'whmcs-client-' . $clientId;
    }
    list($code, $resp) = maburvm_sendEvent($params, 'user.ensure', $data, 'whmcs-ensure-' . uniqid('', true));
    if ($code < 200 || $code >= 300 || empty($resp['success']) || empty($resp['metadata']['user_id'])) {
        throw new \Exception('MaburVM: no panel account for this client: ' . maburvm_errorMessage($resp, $code));
    }
    maburvm_saveProperty($params, 'panel_user_id', $resp['metadata']['user_id']);
    return $resp['metadata']['user_id'];
}

/**
 * Persist the created VM's panel ID on the WHMCS service so later lifecycle
 * calls (suspend/unsuspend/terminate) can reference it.
 */
function maburvm_storeVmId(array $params, $vmId)
{
    maburvm_saveProperty($params, 'vm_id', $vmId);
}

/**
 * Retrieve the stored panel VM ID for this service.
 */
function maburvm_vmId(array $params)
{
    $vmId = maburvm_property($params, 'vm_id');
    if ($vmId !== '') {
        return $vmId;
    }
    return !empty($params['customfields']['VM ID']) ? trim($params['customfields']['VM ID']) : '';
}

/**
 * POST a body and return [$httpCode, $responseBody]. Tests replace the
 * transport through $GLOBALS['maburvm_http'].
 *
 * @throws \Exception on transport failure
 */
function maburvm_http($url, $body, array $headers, $timeout = 30)
{
    if (isset($GLOBALS['maburvm_http']) && is_callable($GLOBALS['maburvm_http'])) {
        return call_user_func($GLOBALS['maburvm_http'], $url, $body, $headers, $timeout);
    }
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_POST => true,
        CURLOPT_POSTFIELDS => $body,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => $timeout,
        CURLOPT_HTTPHEADER => $headers,
    ]);
    $response = curl_exec($ch);
    if ($response === false) {
        $transportError = curl_error($ch);
        curl_close($ch);
        throw new \Exception('Failed to reach MaburVM panel: ' . $transportError);
    }
    $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return [$httpCode, $response];
}

/**
 * Send a signed billing-webhook event to the panel.
 *
 * @param array|object $data
 * @return array [$httpCode, $decodedBody]
 * @throws \Exception on transport failure
 */
function maburvm_sendEvent(array $params, $event, $data, $idempotencyKey, $timeout = 30)
{
    $apiKey = $params['serverpassword'];             // stored as the Server "Password/Hash"
    $webhookSecret = $params['serveraccesshash'];    // stored as the Server "Access Hash"

    if ($apiKey === '' || $webhookSecret === '') {
        throw new \Exception('MaburVM server is missing API Key (Password) or Webhook Secret (Access Hash).');
    }

    $timestamp = gmdate('Y-m-d\TH:i:s\Z');
    $payload = [
        'event' => $event,
        'timestamp' => $timestamp,
        'nonce' => bin2hex(random_bytes(16)),
        'data' => $data,
    ];
    // The panel verifies the signature over the exact bytes it receives, so we
    // sign the same string we send. Do not re-encode after signing.
    $body = json_encode($payload, JSON_UNESCAPED_SLASHES);
    $signature = 'sha256=' . hash_hmac('sha256', $body, $webhookSecret);

    list($httpCode, $response) = maburvm_http(maburvm_apiBaseUrl($params) . '/webhooks/billing', $body, [
        'Content-Type: application/json',
        'X-API-Key: ' . $apiKey,
        'X-Webhook-Signature: ' . $signature,
        'X-Webhook-Timestamp: ' . $timestamp,
        'X-Idempotency-Key: ' . $idempotencyKey,
    ], $timeout);

    $decoded = json_decode($response, true);
    return [$httpCode, is_array($decoded) ? $decoded : ['raw' => $response]];
}

/**
 * What the panel said went wrong: the error envelope's error.message, the
 * top-level message panels before the envelope sent, or the HTTP status.
 */
function maburvm_errorMessage($resp, $code)
{
    if (is_array($resp)) {
        if (!empty($resp['error']['message']) && is_string($resp['error']['message'])) {
            return $resp['error']['message'];
        }
        if (!empty($resp['message']) && is_string($resp['message'])) {
            return $resp['message'];
        }
    }
    return 'HTTP ' . $code;
}

/**
 * The VM's provisioning state and how it is reached, from the panel:
 * ['state' => provisioning|ready|failed|error|deleting|missing, 'ip', 'ips',
 * 'username', 'error', 'retrying', ...]. "failed" is an installation that
 * failed; "error" a VM that was installed and is broken now. Null when the panel could not say, or could not
 * be reached: a read must never fail the action that asked.
 */
function maburvm_vmStatus(array $params, $vmId, $userId, $timeout = 30)
{
    $data = ['vm_id' => $vmId, 'external_ref' => 'whmcs-' . $params['serviceid']];
    if ($userId !== '') {
        $data['user_id'] = $userId;
    }
    try {
        list($code, $resp) = maburvm_sendEvent($params, 'vm.status', $data, 'whmcs-status-' . uniqid('', true), $timeout);
    } catch (\Throwable $e) {
        return null;
    }
    if ($code == 404) {
        return ['state' => 'missing'];
    }
    if ($code < 200 || $code >= 300 || empty($resp['success']) || empty($resp['metadata']['state'])) {
        return null;
    }
    return $resp['metadata'];
}

/**
 * Write what the customer needs to reach the VM into the WHMCS service, so the
 * client area and the welcome email show it. A null value leaves a field alone.
 */
function maburvm_saveAccess($serviceId, $status, $password = null)
{
    $fields = [];
    if (!empty($status['ip'])) {
        $fields['dedicatedip'] = $status['ip'];
        // The other addresses, one per line, which is how WHMCS lists them.
        $others = array_values(array_diff(array_filter(explode(',', (string) ($status['ips'] ?? ''))), [$status['ip']]));
        $fields['assignedips'] = implode("\n", $others);
    }
    if (!empty($status['username'])) {
        $fields['username'] = $status['username'];
    }
    if ($password !== null) {
        $fields['password'] = encrypt($password);
    }
    if ($fields) {
        \WHMCS\Database\Capsule::table('tblhosting')->where('id', $serviceId)->update($fields);
    }
}

/**
 * Wait until the VM has left "provisioning", or the product's wait runs out.
 * Returns the last status the panel gave (null when it gave none).
 */
function maburvm_awaitProvisioning(array $params, $vmId, $userId)
{
    $configured = isset($params['configoption7']) ? trim((string) $params['configoption7']) : '';
    $wait = $configured === '' ? MABURVM_DEFAULT_WAIT : max(0, (int) $configured);
    if ($wait > 0 && function_exists('set_time_limit')) {
        @set_time_limit($wait + 60);
    }
    $sleep = isset($GLOBALS['maburvm_sleep']) && is_callable($GLOBALS['maburvm_sleep']) ? $GLOBALS['maburvm_sleep'] : 'sleep';
    $waited = 0;
    while (true) {
        $status = maburvm_vmStatus($params, $vmId, $userId);
        if (($status !== null && $status['state'] !== 'provisioning') || $waited >= $wait) {
            return $status;
        }
        call_user_func($sleep, MABURVM_POLL_INTERVAL);
        $waited += MABURVM_POLL_INTERVAL;
    }
}

/**
 * Create (provision) a VM, and report how it went: the service gets the VM's
 * address, login user and password, and an installation that fails within the
 * product's wait is an error, not a success.
 */
function maburvm_CreateAccount(array $params)
{
    try {
        $userId = maburvm_panelUserId($params, true);

        $templateId = maburvm_option($params, ['template', 'template id', 'operating system', 'os'], 1);
        if ($templateId === '') {
            return 'MaburVM: Template ID is not configured on the product.';
        }

        // A second Create on a service that already has a VM finishes the
        // first one; it never provisions another.
        $vmId = maburvm_vmId($params);
        $existing = $vmId !== '' ? maburvm_vmStatus($params, $vmId, $userId) : ['state' => 'missing'];
        if ($existing === null) {
            return 'MaburVM: this service already has a VM (' . $vmId . ') and the panel could not report its state. Try again.';
        }
        $password = null;
        if (in_array($existing['state'], ['missing', 'deleting'], true)) {
            $hostname = $params['domain'] !== '' ? $params['domain'] : ('svc-' . $params['serviceid']);
            // The service's own password becomes the root password, so the one
            // WHMCS shows the customer works. One too short to set is replaced.
            $rootPassword = isset($params['password']) ? (string) $params['password'] : '';
            if (strlen($rootPassword) < 8) {
                $rootPassword = $password = bin2hex(random_bytes(12));
            }
            $data = array_merge([
                'user_id' => $userId,
                'hostname' => $hostname,
                'template_id' => $templateId,
                'external_ref' => 'whmcs-' . $params['serviceid'],
                'password' => $rootPassword,
            ], maburvm_sizeData($params));
            $region = maburvm_option($params, ['region', 'location'], 6);
            if ($region !== '') {
                $data['region'] = $region;
            }

            // Idempotency key keyed on the service so a retried create doesn't
            // double-provision.
            list($code, $resp) = maburvm_sendEvent($params, 'vm.create', $data, 'whmcs-create-' . $params['serviceid']);
            if ($code < 200 || $code >= 300 || empty($resp['success']) || empty($resp['vm_id'])) {
                return 'MaburVM create failed: ' . (maburvm_errorMessage($resp, $code));
            }
            $vmId = $resp['vm_id'];
            maburvm_storeVmId($params, $vmId);
            // Before the wait: nothing after this point may lose the password the VM was given.
            if ($password !== null) {
                maburvm_saveAccess($params['serviceid'], [], $password);
            }
        }

        $status = maburvm_awaitProvisioning($params, $vmId, $userId);
        if ($status !== null) {
            maburvm_saveAccess($params['serviceid'], $status);
        }
        if ($status !== null && $status['state'] === 'failed') {
            $reason = !empty($status['error']) ? $status['error'] : 'the panel gave no reason';
            if (!empty($status['retrying'])) {
                return 'MaburVM: the VM is not installed yet: ' . $reason . '. The panel is still trying. Run Create again to see'
                    . ' how it ended, or Terminate this service to give up and remove the VM.';
            }
            return 'MaburVM: the VM could not be installed: ' . $reason
                . '. Terminate this service to remove the failed VM, then run Create again.';
        }
        if ($status !== null && $status['state'] === 'error') {
            // Installed earlier and broken since: not this order's failure, and not a VM to replace unasked.
            return 'MaburVM: this service already has a VM (' . $vmId . ') and it is in an error state on the panel. Check it there.';
        }
        if ($status !== null && $status['state'] === 'provisioning') {
            logModuleCall('maburvm', 'CreateAccount', $params['serviceid'], 'The VM is still being installed after the provisioning wait; a later failure is reported by the panel, not here.');
        }
        return 'success';
    } catch (\Throwable $e) {
        logModuleCall('maburvm', 'CreateAccount', $params['serviceid'] ?? '', $e->getMessage(), $e->getTraceAsString());
        return $e->getMessage();
    }
}

/**
 * Suspend a VM.
 */
function maburvm_SuspendAccount(array $params)
{
    return maburvm_lifecycle($params, 'vm.suspend', 'suspend');
}

/**
 * Unsuspend a VM.
 */
function maburvm_UnsuspendAccount(array $params)
{
    return maburvm_lifecycle($params, 'vm.unsuspend', 'unsuspend');
}

/**
 * Terminate (destroy) a VM.
 */
function maburvm_TerminateAccount(array $params)
{
    return maburvm_lifecycle($params, 'vm.destroy', 'terminate');
}

/**
 * Shared suspend/unsuspend/terminate implementation.
 */
function maburvm_lifecycle(array $params, $event, $action)
{
    try {
        $vmId = maburvm_vmId($params);
        if ($vmId === '') {
            return 'MaburVM: no VM ID is stored for this service; cannot ' . $action . '.';
        }
        $data = [
            'user_id' => maburvm_panelUserId($params),
            'vm_id' => $vmId,
            'external_ref' => 'whmcs-' . $params['serviceid'],
        ];
        $idem = 'whmcs-' . $action . '-' . $params['serviceid'] . '-' . $vmId;

        list($code, $resp) = maburvm_sendEvent($params, $event, $data, $idem);
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            return 'success';
        }
        return 'MaburVM ' . $action . ' failed: ' . (maburvm_errorMessage($resp, $code));
    } catch (\Throwable $e) {
        logModuleCall('maburvm', ucfirst($action), $params['serviceid'] ?? '', $e->getMessage(), $e->getTraceAsString());
        return $e->getMessage();
    }
}

/**
 * Send one event for this service's VM. $extra is merged into the event data.
 * Returns 'success' or an error string, or the decoded reply when $raw is set.
 */
function maburvm_vmEvent(array $params, $event, array $extra = [], $raw = false)
{
    try {
        $vmId = maburvm_vmId($params);
        if ($vmId === '') {
            return 'MaburVM: no VM ID is stored for this service.';
        }
        $data = array_merge([
            'user_id' => maburvm_panelUserId($params),
            'vm_id' => $vmId,
            'external_ref' => 'whmcs-' . $params['serviceid'],
        ], $extra);
        list($code, $resp) = maburvm_sendEvent($params, $event, $data, 'whmcs-' . $event . '-' . uniqid('', true));
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            return $raw ? $resp : 'success';
        }
        return 'MaburVM ' . $event . ' failed: ' . (maburvm_errorMessage($resp, $code));
    } catch (\Throwable $e) {
        logModuleCall('maburvm', $event, $params['serviceid'], $e->getMessage(), $e->getTraceAsString());
        return $e->getMessage();
    }
}

/**
 * Upgrade or downgrade: apply the new product's CPU, memory and disk, and with
 * a plan its speed cap and transfer quota. A running VM is shut down and
 * booted again by the panel when its size changes. Disk only grows.
 */
function maburvm_ChangePackage(array $params)
{
    return maburvm_vmEvent($params, 'vm.resize', maburvm_sizeData($params));
}

/**
 * Set the root password to the one saved on the service. The VM must be running.
 */
function maburvm_ChangePassword(array $params)
{
    return maburvm_vmEvent($params, 'vm.reset_password', ['password' => $params['password']]);
}

/**
 * Staff power buttons on the service page.
 */
function maburvm_AdminCustomButtonArray()
{
    return ['Start' => 'start', 'Stop' => 'stop', 'Reboot' => 'restart'];
}

function maburvm_start(array $params)
{
    return maburvm_vmEvent($params, 'vm.start');
}

function maburvm_stop(array $params)
{
    return maburvm_vmEvent($params, 'vm.stop');
}

function maburvm_restart(array $params)
{
    return maburvm_vmEvent($params, 'vm.restart');
}

/** Usage figures kept on the service (as custom fields), by what the panel calls them. */
const MABURVM_USAGE_FIELDS = [
    'snapshot_count' => 'Snapshots',
    'backup_count' => 'Backups',
    'backup_size_mb' => 'Backup size (MB)',
    'extra_ips' => 'Extra IPs',
    'floating_ips' => 'Floating IPs',
    'uptime_hours' => 'Uptime hours',
];

/**
 * Daily usage sync, for every active service on this server: traffic used and
 * allowed this period into the bandwidth columns, the disk the VM holds (with
 * its data disks) and its allowance into the disk columns, and snapshots,
 * backups, extra and floating addresses and running hours into service fields
 * of those names. Disks are thin, so what the guest wrote is not measured. A
 * service still without an address (its VM outlasted the provisioning wait)
 * gets it here.
 */
function maburvm_UsageUpdate(array $params)
{
    $services = \WHMCS\Service\Service::where('server', $params['serverid'])
        ->where('domainstatus', 'Active')->get();
    foreach ($services as $service) {
        try {
            $vmId = $service->serviceProperties->get('vm_id');
            if (empty($vmId)) {
                continue;
            }
            list($code, $resp) = maburvm_sendEvent($params, 'vm.usage', ['vm_id' => $vmId],
                'whmcs-usage-' . uniqid('', true));
            if ($code < 200 || $code >= 300 || empty($resp['success'])) {
                continue;
            }
            $m = $resp['metadata'];
            $row = [
                'bwusage' => (int) $m['bandwidth_used_mb'],
                'bwlimit' => (int) $m['bandwidth_limit_mb'],
                'disklimit' => (int) $m['disk_limit_mb'],
                'lastupdate' => \WHMCS\Database\Capsule::raw('now()'),
            ];
            // A panel from before the full usage report sends no disk figure.
            if (isset($m['disk_allocated_mb'])) {
                $row['diskusage'] = (int) $m['disk_allocated_mb'];
            }
            \WHMCS\Database\Capsule::table('tblhosting')->where('id', $service->id)->update($row);
            $fields = [];
            foreach (MABURVM_USAGE_FIELDS as $key => $label) {
                if (isset($m[$key])) {
                    $fields[$label] = (string) (int) $m[$key];
                }
            }
            if ($fields) {
                $service->serviceProperties->save($fields);
            }
            if (empty($service->dedicatedip)) {
                $status = maburvm_vmStatus($params + ['serviceid' => $service->id], $vmId, '');
                if ($status !== null) {
                    maburvm_saveAccess($service->id, $status);
                }
            }
        } catch (\Throwable $e) {
            logModuleCall('maburvm', 'UsageUpdate', $service->id, $e->getMessage(), $e->getTraceAsString());
        }
    }
}

/**
 * "Test Connection" button in the Server configuration. Sends a signed,
 * read-only request, so it proves the API key, the webhook secret and the
 * clock, not only that the host answers.
 */
function maburvm_TestConnection(array $params)
{
    try {
        list($code, $resp) = maburvm_sendEvent($params, 'catalog.list', new \stdClass(), 'whmcs-test-' . uniqid('', true));
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            return ['success' => true, 'error' => ''];
        }
        // A panel from before catalog.list answers 400 "Invalid event type"
        // once the credentials have been accepted.
        if ($code == 400 && isset($resp['message']) && stripos($resp['message'], 'Invalid event type') !== false) {
            return ['success' => true, 'error' => ''];
        }
        return ['success' => false, 'error' => maburvm_errorMessage($resp, $code)];
    } catch (\Throwable $e) {
        return ['success' => false, 'error' => $e->getMessage()];
    }
}

/**
 * Single sign-on: WHMCS calls this when the customer (or staff, from the
 * service page) opens the control panel. The panel returns a link for the
 * service's panel account that lasts a minute and works once; it opens the
 * service's VM, and still asks for the panel's own two-factor code when the
 * account has one. No account is ever made here.
 */
function maburvm_ServiceSingleSignOn(array $params)
{
    try {
        $userId = maburvm_panelUserId($params);
        if ($userId === '') {
            return ['success' => false, 'errorMsg' => 'MaburVM: this service has no panel account yet. Run Create first, or fill in its Panel User ID.'];
        }
        $data = ['user_id' => $userId, 'external_ref' => 'whmcs-' . $params['serviceid']];
        $vmId = maburvm_vmId($params);
        if ($vmId !== '') {
            $data['vm_id'] = $vmId;
        }
        list($code, $resp) = maburvm_sendEvent($params, 'user.sso', $data, 'whmcs-sso-' . uniqid('', true), 10);
        if ($code >= 200 && $code < 300 && !empty($resp['success']) && !empty($resp['metadata']['path'])) {
            // The panel's own public address when it knows one, else the server's.
            $url = !empty($resp['metadata']['url']) ? $resp['metadata']['url'] : maburvm_apiBaseUrl($params) . $resp['metadata']['path'];
            return ['success' => true, 'redirectTo' => $url];
        }
        $message = maburvm_errorMessage($resp, $code);
        if ($code == 400 && stripos($message, 'Invalid event type') !== false) {
            $message = 'this panel does not offer single sign-on yet; update the panel';
        }
        return ['success' => false, 'errorMsg' => 'MaburVM: ' . $message];
    } catch (\Throwable $e) {
        logModuleCall('maburvm', 'ServiceSingleSignOn', $params['serviceid'] ?? '', $e->getMessage(), $e->getTraceAsString());
        return ['success' => false, 'errorMsg' => $e->getMessage()];
    }
}

/**
 * Buttons WHMCS shows the customer on the service page; the client-area
 * template has its own as well. Each acts on this service's VM only, and the
 * panel checks the VM belongs to this service's account and service.
 */
function maburvm_ClientAreaCustomButtonArray()
{
    return ['Start' => 'clientStart', 'Shut down' => 'clientStop', 'Restart' => 'clientRestart'];
}

/** What the client-area template may call besides the buttons. */
function maburvm_ClientAreaAllowedFunctions()
{
    return ['clientStart', 'clientStop', 'clientRestart', 'clientReinstall', 'clientResetPassword', 'clientConsole'];
}

/** Refuses a customer action on a service that is not active (suspended, cancelled). */
function maburvm_clientGuard(array $params)
{
    $status = isset($params['status']) ? (string) $params['status'] : 'Active';
    if ($status !== 'Active') {
        return 'This service is ' . strtolower($status) . ', so the server cannot be changed from here.';
    }
    return '';
}

function maburvm_clientStart(array $params)
{
    return maburvm_clientGuard($params) ?: maburvm_vmEvent($params, 'vm.start');
}

function maburvm_clientStop(array $params)
{
    return maburvm_clientGuard($params) ?: maburvm_vmEvent($params, 'vm.stop');
}

function maburvm_clientRestart(array $params)
{
    return maburvm_clientGuard($params) ?: maburvm_vmEvent($params, 'vm.restart');
}

/** The request field a client-area form sent; tests set $GLOBALS['maburvm_request']. */
function maburvm_requestField($name)
{
    $request = isset($GLOBALS['maburvm_request']) && is_array($GLOBALS['maburvm_request']) ? $GLOBALS['maburvm_request'] : $_POST;
    return isset($request[$name]) ? trim((string) $request[$name]) : '';
}

/**
 * Reinstall the server with the template the customer chose (one the panel
 * offers on this product's plan). The panel makes a new root password, which
 * is saved on the service so the customer sees it there. The server must be
 * stopped, and the customer must tick the confirmation.
 */
function maburvm_clientReinstall(array $params)
{
    if ($refused = maburvm_clientGuard($params)) {
        return $refused;
    }
    if (maburvm_requestField('confirm') === '') {
        return 'Tick the box to confirm: reinstalling erases everything on the server.';
    }
    $templateId = maburvm_requestField('template_id');
    if ($templateId === '' || !preg_match('/^[A-Fa-f0-9-]{36}$/', $templateId)) {
        return 'Choose an operating system.';
    }
    $resp = maburvm_vmEvent($params, 'vm.rebuild', ['template_id' => $templateId, 'regenerate_password' => true], true);
    if (!is_array($resp)) {
        return $resp;
    }
    if (!empty($resp['metadata']['password'])) {
        maburvm_saveAccess($params['serviceid'], [], $resp['metadata']['password']);
    }
    return 'success';
}

/**
 * Give the server a new root password, saved on the service where the
 * customer reads it. The server must be running.
 */
function maburvm_clientResetPassword(array $params)
{
    if ($refused = maburvm_clientGuard($params)) {
        return $refused;
    }
    $password = rtrim(strtr(base64_encode(random_bytes(15)), '+/', 'Kx'), '=') . 'Aa7';
    $result = maburvm_vmEvent($params, 'vm.reset_password', ['password' => $password]);
    if ($result === 'success') {
        maburvm_saveAccess($params['serviceid'], [], $password);
    }
    return $result;
}

/** Send the browser on; tests replace it through $GLOBALS['maburvm_redirect']. */
function maburvm_redirect($url)
{
    if (isset($GLOBALS['maburvm_redirect']) && is_callable($GLOBALS['maburvm_redirect'])) {
        return call_user_func($GLOBALS['maburvm_redirect'], $url);
    }
    header('Location: ' . $url);
    exit;
}

/**
 * Open the server's console in the panel: a one-time sign-in link for this
 * service's account that lands on the console. The panel still asks for its
 * own two-factor code when the account has one.
 */
function maburvm_clientConsole(array $params)
{
    if ($refused = maburvm_clientGuard($params)) {
        return $refused;
    }
    $vmId = maburvm_vmId($params);
    if ($vmId === '') {
        return 'MaburVM: no VM ID is stored for this service.';
    }
    try {
        $data = ['user_id' => maburvm_panelUserId($params), 'vm_id' => $vmId, 'page' => 'console',
            'external_ref' => 'whmcs-' . $params['serviceid']];
        list($code, $resp) = maburvm_sendEvent($params, 'user.sso', $data, 'whmcs-console-' . uniqid('', true), 10);
        if ($code >= 200 && $code < 300 && !empty($resp['success']) && !empty($resp['metadata']['path'])) {
            $url = !empty($resp['metadata']['url']) ? $resp['metadata']['url'] : maburvm_apiBaseUrl($params) . $resp['metadata']['path'];
            return maburvm_redirect($url);
        }
        return 'MaburVM: the console could not be opened: ' . maburvm_errorMessage($resp, $code);
    } catch (\Throwable $e) {
        logModuleCall('maburvm', 'clientConsole', $params['serviceid'] ?? '', $e->getMessage(), $e->getTraceAsString());
        return $e->getMessage();
    }
}

/**
 * One graph for the template: an SVG polyline over a 300 x 60 box, scaled to
 * the series' own peak (or to 100 for percentages), and the latest value.
 */
function maburvm_graph($label, array $values, $unit, $percent)
{
    $peak = $percent ? 100.0 : max(1.0, (float) max($values));
    $n = count($values);
    $points = [];
    foreach (array_values($values) as $i => $v) {
        $x = $n > 1 ? round($i * 300 / ($n - 1), 1) : 0;
        $y = round(60 - min(1.0, max(0.0, $v / $peak)) * 58 - 1, 1);
        $points[] = $x . ',' . $y;
    }
    $last = (float) end($values);
    if ($percent) {
        $now = round($last) . '%';
    } elseif ($last >= 1048576) {
        $now = round($last / 1048576, 1) . ' MB/s';
    } elseif ($last >= 1024) {
        $now = round($last / 1024, 1) . ' KB/s';
    } else {
        $now = round($last) . ' B/s';
    }
    return ['label' => $label, 'points' => implode(' ', $points), 'now' => $now, 'unit' => $unit];
}

/**
 * Client-area panel: the VM's state and address, read live from the panel,
 * power buttons, the console, a reinstall form, a new root password and the
 * last day's graphs. The panel checks every action names this service's VM.
 */
function maburvm_ClientArea(array $params)
{
    $vmId = maburvm_vmId($params);
    $status = null;
    $userId = '';
    if ($vmId !== '') {
        try {
            $userId = maburvm_panelUserId($params);
            // A short wait: this runs on every view of the customer's service page.
            $status = maburvm_vmStatus($params, $vmId, $userId, 5);
        } catch (\Throwable $e) {
            // The links below still work when the panel cannot be asked.
        }
    }
    $states = [
        'provisioning' => 'Being installed',
        'ready' => 'Ready',
        'failed' => 'Installation failed',
        'error' => 'Needs attention',
        'deleting' => 'Being removed',
        'missing' => 'Removed',
    ];
    $graphs = [];
    $templates = [];
    $ready = $status !== null && $status['state'] === 'ready';
    if ($ready) {
        try {
            list($code, $resp) = maburvm_sendEvent($params, 'vm.metrics',
                ['vm_id' => $vmId, 'user_id' => $userId, 'external_ref' => 'whmcs-' . $params['serviceid'], 'hours' => 24],
                'whmcs-metrics-' . uniqid('', true), 5);
            $points = $code >= 200 && $code < 300 && !empty($resp['data']['points']) ? $resp['data']['points'] : [];
            if (count($points) > 1) {
                $graphs[] = maburvm_graph('CPU', array_column($points, 'cpu_percent'), '%', true);
                $graphs[] = maburvm_graph('Memory', array_column($points, 'memory_percent'), '%', true);
                $graphs[] = maburvm_graph('Network in', array_column($points, 'network_in_bps'), 'B/s', false);
                $graphs[] = maburvm_graph('Network out', array_column($points, 'network_out_bps'), 'B/s', false);
            }
        } catch (\Throwable $e) {
            // No graphs, the rest of the page still works.
        }
        try {
            $planId = maburvm_option($params, ['plan', 'plan id'], 5);
            foreach (maburvm_catalog($params)['templates'] as $t) {
                $plans = isset($t['plan_ids']) && is_array($t['plan_ids']) ? $t['plan_ids'] : [];
                if (!$plans || ($planId !== '' && in_array($planId, $plans, true))) {
                    $templates[] = ['id' => $t['id'], 'name' => trim($t['name'] . ' ' . $t['version'])];
                }
            }
        } catch (\Throwable $e) {
            // No reinstall list when the catalog cannot be read.
        }
    }
    return [
        'templatefile' => 'clientarea',
        'vars' => [
            'panelUrl' => maburvm_apiBaseUrl($params),
            'serviceId' => $params['serviceid'],
            'serviceActive' => !isset($params['status']) || $params['status'] === 'Active',
            'vmId' => $vmId,
            'vmState' => $status !== null ? $status['state'] : '',
            'vmStateLabel' => $status !== null && isset($states[$status['state']]) ? $states[$status['state']] : '',
            'vmPower' => $status !== null && !empty($status['status']) ? $status['status'] : '',
            'vmIp' => $status !== null && !empty($status['ip']) ? $status['ip'] : '',
            'vmUsername' => $status !== null && !empty($status['username']) ? $status['username'] : '',
            'graphs' => $graphs,
            'templates' => $templates,
        ],
    ];
}
