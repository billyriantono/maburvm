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
 *   Body: {"event","timestamp","data":{...}}
 *
 * The webhook maps data.user_id straight onto the created VM's owner, so each
 * WHMCS service must carry the panel User UUID it belongs to. This module reads
 * it from a service custom field named "Panel User ID" (see README).
 *
 * Copy this folder to  <whmcs>/modules/servers/maburvm/  and add MaburVM as a
 * Server + Product (Module Settings -> MaburVM).
 */

if (!defined('WHMCS')) {
    die('This file cannot be accessed directly');
}

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
 */
function maburvm_ConfigOptions()
{
    return [
        'Template ID' => [
            'Type' => 'text',
            'Size' => '40',
            'Description' => 'MaburVM OS template UUID to install',
        ],
        'vCPU' => [
            'Type' => 'text',
            'Size' => '5',
            'Default' => '1',
            'Description' => 'Number of virtual CPUs',
        ],
        'Memory (MB)' => [
            'Type' => 'text',
            'Size' => '8',
            'Default' => '1024',
            'Description' => 'RAM in megabytes',
        ],
        'Disk (GB)' => [
            'Type' => 'text',
            'Size' => '8',
            'Default' => '20',
            'Description' => 'Primary disk in gigabytes',
        ],
    ];
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
 * Read the panel User UUID for this service from the "Panel User ID" custom field.
 */
function maburvm_panelUserId(array $params)
{
    if (!empty($params['customfields']['Panel User ID'])) {
        return trim($params['customfields']['Panel User ID']);
    }
    return '';
}

/**
 * Persist the created VM's panel ID on the WHMCS service so later lifecycle
 * calls (suspend/unsuspend/terminate) can reference it.
 */
function maburvm_storeVmId(array $params, $vmId)
{
    if (isset($params['model']) && $params['model'] !== null) {
        try {
            $params['model']->serviceProperties->save(['vm_id' => $vmId]);
        } catch (\Throwable $e) {
            // Custom-field storage (a hidden "VM ID" field) can be used as a fallback.
        }
    }
}

/**
 * Retrieve the stored panel VM ID for this service.
 */
function maburvm_vmId(array $params)
{
    if (isset($params['model']) && $params['model'] !== null) {
        try {
            $vmId = $params['model']->serviceProperties->get('vm_id');
            if (!empty($vmId)) {
                return $vmId;
            }
        } catch (\Throwable $e) {
            // ignore
        }
    }
    if (!empty($params['customfields']['VM ID'])) {
        return trim($params['customfields']['VM ID']);
    }
    return '';
}

/**
 * Send a signed billing-webhook event to the panel.
 *
 * @return array [$httpCode, $decodedBody]
 * @throws \Exception on transport failure
 */
function maburvm_sendEvent(array $params, $event, array $data, $idempotencyKey)
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
        'data' => $data,
    ];
    // The panel verifies the signature over the exact bytes it receives, so we
    // sign the same string we send. Do not re-encode after signing.
    $body = json_encode($payload, JSON_UNESCAPED_SLASHES);
    $signature = 'sha256=' . hash_hmac('sha256', $body, $webhookSecret);

    $url = maburvm_apiBaseUrl($params) . '/webhooks/billing';

    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_POST => true,
        CURLOPT_POSTFIELDS => $body,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => 30,
        CURLOPT_HTTPHEADER => [
            'Content-Type: application/json',
            'X-API-Key: ' . $apiKey,
            'X-Webhook-Signature: ' . $signature,
            'X-Webhook-Timestamp: ' . $timestamp,
            'X-Idempotency-Key: ' . $idempotencyKey,
        ],
    ]);

    $response = curl_exec($ch);
    if ($response === false) {
        $transportError = curl_error($ch);
        curl_close($ch);
        throw new \Exception('Failed to reach MaburVM panel: ' . $transportError);
    }
    $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);

    $decoded = json_decode($response, true);
    return [$httpCode, is_array($decoded) ? $decoded : ['raw' => $response]];
}

/**
 * Create (provision) a VM.
 */
function maburvm_CreateAccount(array $params)
{
    try {
        $userId = maburvm_panelUserId($params);
        if ($userId === '') {
            return 'MaburVM: the "Panel User ID" custom field is empty. Set the panel User UUID for this service.';
        }

        $templateId = trim($params['configoption1']);
        if ($templateId === '') {
            return 'MaburVM: Template ID is not configured on the product.';
        }

        $hostname = $params['domain'] !== '' ? $params['domain'] : ('svc-' . $params['serviceid']);

        $data = [
            'user_id' => $userId,
            'hostname' => $hostname,
            'template_id' => $templateId,
            'vm_specs' => [
                'cpu' => (int) $params['configoption2'],
                'memory' => (int) $params['configoption3'],
                'disk' => (int) $params['configoption4'],
            ],
            'external_ref' => 'whmcs-' . $params['serviceid'],
        ];

        // Idempotency key keyed on the service so a retried create doesn't
        // double-provision.
        $idem = 'whmcs-create-' . $params['serviceid'];

        list($code, $resp) = maburvm_sendEvent($params, 'vm.create', $data, $idem);
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            if (!empty($resp['vm_id'])) {
                maburvm_storeVmId($params, $resp['vm_id']);
            }
            return 'success';
        }
        return 'MaburVM create failed: ' . (isset($resp['message']) ? $resp['message'] : ('HTTP ' . $code));
    } catch (\Throwable $e) {
        logModuleCall('maburvm', 'CreateAccount', $params, $e->getMessage(), $e->getTraceAsString());
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
        return 'MaburVM ' . $action . ' failed: ' . (isset($resp['message']) ? $resp['message'] : ('HTTP ' . $code));
    } catch (\Throwable $e) {
        logModuleCall('maburvm', ucfirst($action), $params, $e->getMessage(), $e->getTraceAsString());
        return $e->getMessage();
    }
}

/**
 * "Test Connection" button in the Server configuration. Hits the public docs
 * endpoint to confirm the panel is reachable at the configured URL.
 */
function maburvm_TestConnection(array $params)
{
    try {
        $url = maburvm_apiBaseUrl($params) . '/webhooks/billing/docs';
        $ch = curl_init($url);
        curl_setopt_array($ch, [
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_TIMEOUT => 15,
        ]);
        $response = curl_exec($ch);
        $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
        $transportError = curl_error($ch);
        curl_close($ch);

        if ($response === false) {
            return ['success' => false, 'error' => 'Could not reach panel: ' . $transportError];
        }
        if ($code >= 200 && $code < 300) {
            return ['success' => true, 'error' => ''];
        }
        return ['success' => false, 'error' => 'Panel returned HTTP ' . $code];
    } catch (\Throwable $e) {
        return ['success' => false, 'error' => $e->getMessage()];
    }
}

/**
 * Client-area link. The webhook layer intentionally exposes only lifecycle
 * billing events; day-to-day operation (start/stop/reboot/console) lives in the
 * MaburVM client portal, so we deep-link there rather than duplicating it.
 */
function maburvm_ClientArea(array $params)
{
    return [
        'templatefile' => 'clientarea',
        'vars' => [
            'panelUrl' => maburvm_apiBaseUrl($params),
            'vmId' => maburvm_vmId($params),
        ],
    ];
}
