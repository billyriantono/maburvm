<?php

/**
 * MaburVM server extension for Paymenter (v1 extension API).
 *
 * Provisions and manages KVM VMs on a MaburVM panel through the panel's
 * signed billing webhook (POST {host}/webhooks/billing), the same one the
 * WHMCS module uses: create, suspend, unsuspend, terminate, upgrade (change
 * package) and one-click sign-in to the panel.
 *
 * Copy this folder to <paymenter>/extensions/Servers/Maburvm/. Built against
 * Paymenter's documented server extension interface and tested against stubs
 * of it only: see README.md before relying on it.
 */

namespace Paymenter\Extensions\Servers\Maburvm;

use App\Classes\Extension\Server;

class Maburvm extends Server
{
    /** Service property keys the extension keeps on each service. */
    public const VM_ID = 'maburvm_vm_id';
    public const USER_ID = 'maburvm_user_id';

    /** @var callable|null Replaces the HTTP transport in tests: fn($url, $body, $headers, $timeout): [status, body]. */
    public static $transport = null;

    public function getConfig($values = []): array
    {
        return [
            ['name' => 'host', 'label' => 'Panel URL', 'type' => 'text', 'required' => true, 'validation' => 'url:http,https',
                'description' => 'The address of your MaburVM panel, for example https://panel.example.com'],
            ['name' => 'api_key', 'label' => 'Billing API key', 'type' => 'text', 'required' => true, 'encrypted' => true,
                'description' => 'System settings, Panel, Billing webhook: the API key'],
            ['name' => 'webhook_secret', 'label' => 'Billing webhook secret', 'type' => 'text', 'required' => true, 'encrypted' => true,
                'description' => 'System settings, Panel, Billing webhook: the signing secret'],
        ];
    }

    public function getProductConfig($values = []): array
    {
        return [
            ['name' => 'template_id', 'label' => 'Template ID', 'type' => 'text', 'required' => true,
                'description' => 'The operating system to install (a template UUID from the panel)'],
            ['name' => 'plan_id', 'label' => 'Plan ID', 'type' => 'text', 'required' => false,
                'description' => 'Optional. A panel plan UUID: it decides CPU, memory, disk, speed cap and transfer quota, and the three fields below are ignored'],
            ['name' => 'cpu', 'label' => 'vCPU', 'type' => 'text', 'required' => false, 'default' => '1'],
            ['name' => 'memory', 'label' => 'Memory (MB)', 'type' => 'text', 'required' => false, 'default' => '1024'],
            ['name' => 'disk', 'label' => 'Disk (GB)', 'type' => 'text', 'required' => false, 'default' => '20'],
            ['name' => 'region', 'label' => 'Region', 'type' => 'text', 'required' => false,
                'description' => 'Optional. The region\'s short name; empty uses the panel\'s default region'],
        ];
    }

    /** Proves the URL, the API key, the secret and the clock with a signed read. */
    public function testConfig(): bool|string
    {
        try {
            [$code, $resp] = $this->send('catalog.list', new \stdClass());
        } catch (\Throwable $e) {
            return $e->getMessage();
        }
        return $code >= 200 && $code < 300 && !empty($resp['success']) ? true : self::errorMessage($resp, $code);
    }

    public function createServer($service, $settings, $properties): array
    {
        $vmId = (string) ($properties[self::VM_ID] ?? '');
        if ($vmId !== '') {
            // A second create on a service that has a VM finishes the first.
            return ['vm_id' => $vmId];
        }
        $templateId = self::value($settings, $properties, 'template_id');
        if ($templateId === '') {
            throw new \Exception('MaburVM: the product has no Template ID.');
        }
        $userId = $this->panelUser($service, $properties, true);
        $data = [
            'user_id' => $userId,
            'hostname' => self::value($settings, $properties, 'hostname') ?: ('svc-' . $service->id),
            'template_id' => $templateId,
            'external_ref' => self::serviceRef($service),
        ] + self::size($settings, $properties);
        $region = self::value($settings, $properties, 'region');
        if ($region !== '') {
            $data['region'] = $region;
        }
        [$code, $resp] = $this->send('vm.create', $data, 'paymenter-create-' . $service->id);
        if ($code < 200 || $code >= 300 || empty($resp['success']) || empty($resp['vm_id'])) {
            throw new \Exception('MaburVM create failed: ' . self::errorMessage($resp, $code));
        }
        $service->properties()->updateOrCreate(['key' => self::VM_ID], ['name' => 'MaburVM VM ID', 'value' => $resp['vm_id']]);
        return ['vm_id' => $resp['vm_id']];
    }

    public function suspendServer($service, $settings, $properties): bool
    {
        return $this->vmEvent('vm.suspend', $service, $properties);
    }

    public function unsuspendServer($service, $settings, $properties): bool
    {
        return $this->vmEvent('vm.unsuspend', $service, $properties);
    }

    public function terminateServer($service, $settings, $properties): bool
    {
        return $this->vmEvent('vm.destroy', $service, $properties, [], true);
    }

    /** A package change: the new product's plan or size; a running VM is restarted by the panel. */
    public function upgradeServer($service, $settings, $properties): bool
    {
        return $this->vmEvent('vm.resize', $service, $properties, self::size($settings, $properties));
    }

    /**
     * The service page's button into the panel. The sign-in link is made when
     * the page is shown, lasts a minute and works once; reloading the page
     * makes a new one.
     */
    public function getActions($service, $settings, $properties): array
    {
        $url = $this->ssoLink($service, $properties);
        return $url === '' ? [] : [['type' => 'button', 'label' => 'Open control panel', 'url' => $url]];
    }

    /** A one-time sign-in link for the service's panel account, landing on its VM; '' when there is none. */
    public function ssoLink($service, $properties, string $page = ''): string
    {
        $userId = (string) ($properties[self::USER_ID] ?? '');
        if ($userId === '') {
            return '';
        }
        $data = ['user_id' => $userId, 'external_ref' => self::serviceRef($service)];
        if (!empty($properties[self::VM_ID])) {
            $data['vm_id'] = $properties[self::VM_ID];
        }
        if ($page !== '') {
            $data['page'] = $page;
        }
        try {
            [$code, $resp] = $this->send('user.sso', $data, '', 10);
        } catch (\Throwable $e) {
            return '';
        }
        if ($code < 200 || $code >= 300 || empty($resp['metadata']['path'])) {
            return '';
        }
        return !empty($resp['metadata']['url']) ? $resp['metadata']['url'] : $this->baseUrl() . $resp['metadata']['path'];
    }

    /**
     * One event about the service's VM, named by its stored ID, or by the
     * service's reference when none is stored. A VM already gone counts as
     * terminated.
     */
    private function vmEvent(string $event, $service, $properties, array $extra = [], bool $goneIsDone = false): bool
    {
        $data = ['user_id' => $this->panelUser($service, $properties, false), 'external_ref' => self::serviceRef($service)];
        if (!empty($properties[self::VM_ID])) {
            $data['vm_id'] = $properties[self::VM_ID];
        }
        [$code, $resp] = $this->send($event, $data + $extra, 'paymenter-' . $event . '-' . $service->id . '-' . bin2hex(random_bytes(4)));
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            return true;
        }
        if ($goneIsDone && $code == 404) {
            return true; // nothing left to remove
        }
        throw new \Exception('MaburVM ' . $event . ' failed: ' . self::errorMessage($resp, $code));
    }

    /**
     * The panel account that owns the service's VMs: the one remembered on the
     * service, else (on create only) the one the panel finds or makes for the
     * Paymenter user.
     */
    private function panelUser($service, $properties, bool $ensure): string
    {
        $stored = (string) ($properties[self::USER_ID] ?? '');
        if ($stored !== '' || !$ensure) {
            if ($stored === '') {
                throw new \Exception('MaburVM: this service has no panel account yet. Create it first.');
            }
            return $stored;
        }
        $user = $service->user;
        $email = trim((string) ($user->email ?? ''));
        if ($email === '') {
            throw new \Exception('MaburVM: the customer has no email address.');
        }
        $name = trim(($user->first_name ?? '') . ' ' . ($user->last_name ?? ''));
        [$code, $resp] = $this->send('user.ensure', ['email' => $email, 'name' => $name, 'external_ref' => 'paymenter-user-' . $user->id]);
        if ($code < 200 || $code >= 300 || empty($resp['metadata']['user_id'])) {
            throw new \Exception('MaburVM: no panel account for this customer: ' . self::errorMessage($resp, $code));
        }
        $service->properties()->updateOrCreate(['key' => self::USER_ID], ['name' => 'MaburVM user ID', 'value' => $resp['metadata']['user_id']]);
        return $resp['metadata']['user_id'];
    }

    private static function serviceRef($service): string
    {
        return 'paymenter-' . $service->id;
    }

    /** A setting the customer chose at checkout wins over the product's own. */
    private static function value($settings, $properties, string $name): string
    {
        foreach ([$properties, $settings] as $source) {
            if (is_array($source) && isset($source[$name]) && trim((string) $source[$name]) !== '') {
                return trim((string) $source[$name]);
            }
        }
        return '';
    }

    /** The size as the panel takes it: the plan when one is set, otherwise the three numbers. */
    private static function size($settings, $properties): array
    {
        $plan = self::value($settings, $properties, 'plan_id');
        if ($plan !== '') {
            return ['plan_id' => $plan];
        }
        return ['vm_specs' => [
            'cpu' => (int) (self::value($settings, $properties, 'cpu') ?: 1),
            'memory' => (int) (self::value($settings, $properties, 'memory') ?: 1024),
            'disk' => (int) (self::value($settings, $properties, 'disk') ?: 20),
        ]];
    }

    private function baseUrl(): string
    {
        return rtrim((string) $this->config('host'), '/');
    }

    /** What the panel said went wrong: error.message, else message, else the status. */
    public static function errorMessage($resp, $code): string
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

    /** Send one signed event and return [status, decoded body]. */
    private function send(string $event, $data, string $idempotencyKey = '', int $timeout = 30): array
    {
        $apiKey = (string) $this->config('api_key');
        $secret = (string) $this->config('webhook_secret');
        if ($apiKey === '' || $secret === '') {
            throw new \Exception('MaburVM: the extension has no billing API key or webhook secret.');
        }
        $timestamp = gmdate('Y-m-d\TH:i:s\Z');
        // The panel checks the signature over the exact bytes sent: sign what is sent.
        $body = json_encode(['event' => $event, 'timestamp' => $timestamp, 'nonce' => bin2hex(random_bytes(16)), 'data' => $data], JSON_UNESCAPED_SLASHES);
        $headers = [
            'Content-Type: application/json',
            'X-API-Key: ' . $apiKey,
            'X-Webhook-Signature: sha256=' . hash_hmac('sha256', $body, $secret),
            'X-Webhook-Timestamp: ' . $timestamp,
        ];
        if ($idempotencyKey !== '') {
            $headers[] = 'X-Idempotency-Key: ' . $idempotencyKey;
        }
        $url = $this->baseUrl() . '/webhooks/billing';
        if (self::$transport !== null) {
            [$code, $raw] = call_user_func(self::$transport, $url, $body, $headers, $timeout);
        } else {
            $ch = curl_init($url);
            curl_setopt_array($ch, [CURLOPT_POST => true, CURLOPT_POSTFIELDS => $body, CURLOPT_RETURNTRANSFER => true,
                CURLOPT_TIMEOUT => $timeout, CURLOPT_HTTPHEADER => $headers]);
            $raw = curl_exec($ch);
            if ($raw === false) {
                $error = curl_error($ch);
                curl_close($ch);
                throw new \Exception('MaburVM: the panel could not be reached: ' . $error);
            }
            $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
            curl_close($ch);
        }
        $decoded = json_decode((string) $raw, true);
        return [(int) $code, is_array($decoded) ? $decoded : ['raw' => $raw]];
    }
}
