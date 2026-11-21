<?php

/**
 * MaburVM server manager for FOSSBilling (hosting product server managers).
 *
 * Provisions and manages KVM VMs on a MaburVM panel through the panel's
 * signed billing webhook (POST {host}/webhooks/billing), the same one the
 * WHMCS module uses: create, suspend, unsuspend, cancel (terminate), change
 * package, password change and one-click sign-in to the panel.
 *
 * FOSSBilling keeps no field of its own for the VM, so the panel finds it by
 * the account's reference (fossbilling-<account username>) together with its
 * owner, which the panel finds by the client's email.
 *
 * Copy this file to <fossbilling>/library/Server/Manager/Maburvm.php. Built
 * against FOSSBilling's documented server manager interface and tested against
 * stubs of it only: see README.md before relying on it.
 *
 * Server settings: Hostname = the panel's address, Username = anything,
 * Password = the billing API key, Access hash = the billing webhook secret.
 * Hosting plan custom values: template_id (required), plan_id, or cpu, memory
 * (MB) and disk (GB), and region.
 */
class Server_Manager_Maburvm extends Server_Manager
{
    /** @var callable|null Replaces the HTTP transport in tests: fn($url, $body, $headers, $timeout): [status, body]. */
    public static $transport = null;

    public function init(): void
    {
        if (empty($this->_config['host'])) {
            throw new Server_Exception('MaburVM: the server has no hostname (the panel\'s address).');
        }
        if (empty($this->_config['password']) || empty($this->_config['accesshash'])) {
            throw new Server_Exception('MaburVM: set Password to the billing API key and Access hash to the billing webhook secret.');
        }
    }

    public static function getForm(): array
    {
        return [
            'label' => 'MaburVM',
            'form' => [
                'credentials' => [
                    'fields' => [
                        ['name' => 'password', 'type' => 'password', 'label' => 'Billing API key',
                            'placeholder' => 'System settings, Panel, Billing webhook: the API key', 'required' => true],
                        ['name' => 'accesshash', 'type' => 'password', 'label' => 'Billing webhook secret',
                            'placeholder' => 'System settings, Panel, Billing webhook: the signing secret', 'required' => true],
                    ],
                ],
            ],
        ];
    }

    /** Proves the address, the API key, the secret and the clock with a signed read. */
    public function testConnection(): bool
    {
        [$code, $resp] = $this->send('catalog.list', new stdClass());
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            return true;
        }
        throw new Server_Exception('MaburVM: ' . self::errorMessage($resp, $code));
    }

    /** The panel's view of the VM: its address. */
    public function synchronizeAccount($account): Server_Account
    {
        [$code, $resp] = $this->send('vm.status', $this->vmData($account));
        if ($code >= 200 && $code < 300 && !empty($resp['metadata']['ip'])) {
            $account->setIp($resp['metadata']['ip']);
        }
        return $account;
    }

    public function createAccount($account): bool
    {
        $package = $account->getPackage();
        $template = self::custom($package, 'template_id');
        if ($template === '') {
            throw new Server_Exception('MaburVM: the hosting plan has no template_id custom value.');
        }
        $data = [
            'user_id' => $this->panelUser($account),
            'hostname' => $account->getDomain() ?: ('svc-' . $account->getUsername()),
            'template_id' => $template,
            'external_ref' => self::accountRef($account),
        ] + self::size($package);
        // The account's own password becomes the root password when it is long enough to set.
        if (strlen((string) $account->getPassword()) >= 8) {
            $data['password'] = $account->getPassword();
        }
        $region = self::custom($package, 'region');
        if ($region !== '') {
            $data['region'] = $region;
        }
        [$code, $resp] = $this->send('vm.create', $data, 'fossbilling-create-' . $account->getUsername());
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            return true;
        }
        throw new Server_Exception('MaburVM create failed: ' . self::errorMessage($resp, $code));
    }

    public function suspendAccount($account): bool
    {
        return $this->vmEvent('vm.suspend', $account);
    }

    public function unsuspendAccount($account): bool
    {
        return $this->vmEvent('vm.unsuspend', $account);
    }

    public function cancelAccount($account): bool
    {
        return $this->vmEvent('vm.destroy', $account, [], true);
    }

    public function changeAccountPackage($account, $package): bool
    {
        return $this->vmEvent('vm.resize', $account, self::size($package));
    }

    /** Sets the root password of the running VM. */
    public function changeAccountPassword($account, $newPassword): bool
    {
        return $this->vmEvent('vm.reset_password', $account, ['password' => (string) $newPassword]);
    }

    public function changeAccountUsername($account, $newUsername): bool
    {
        throw new Server_Exception('MaburVM: the username names the VM on the panel and cannot be changed.');
    }

    public function changeAccountDomain($account, $newDomain): bool
    {
        throw new Server_Exception('MaburVM: rename the VM in the panel.');
    }

    public function changeAccountIp($account, $newIp): bool
    {
        throw new Server_Exception('MaburVM: change the VM\'s addresses in the panel.');
    }

    /**
     * A one-time sign-in link to the VM in the panel, made when the page is
     * shown; it lasts a minute and works once. The panel's own address when
     * none can be made.
     */
    public function getLoginUrl($account = null): string
    {
        if ($account === null) {
            return $this->baseUrl() . '/login';
        }
        try {
            [$code, $resp] = $this->send('user.sso', $this->vmData($account), '', 10);
        } catch (\Throwable $e) {
            return $this->baseUrl() . '/login';
        }
        if ($code >= 200 && $code < 300 && !empty($resp['metadata']['path'])) {
            return !empty($resp['metadata']['url']) ? $resp['metadata']['url'] : $this->baseUrl() . $resp['metadata']['path'];
        }
        return $this->baseUrl() . '/login';
    }

    public function getResellerLoginUrl($account = null): string
    {
        return $this->baseUrl() . '/login';
    }

    /** The account's VM named by its reference and owner. */
    private function vmData($account): array
    {
        return ['user_id' => $this->panelUser($account), 'external_ref' => self::accountRef($account)];
    }

    private function vmEvent(string $event, $account, array $extra = [], bool $goneIsDone = false): bool
    {
        [$code, $resp] = $this->send($event, $this->vmData($account) + $extra);
        if ($code >= 200 && $code < 300 && !empty($resp['success'])) {
            return true;
        }
        if ($goneIsDone && $code == 404) {
            return true; // nothing left to remove
        }
        throw new Server_Exception('MaburVM ' . $event . ' failed: ' . self::errorMessage($resp, $code));
    }

    /**
     * The panel account of the client: found by the client's reference when
     * FOSSBilling passes its ID, else by email, and made on first use.
     */
    private function panelUser($account): string
    {
        $client = $account->getClient();
        $email = trim((string) $client->getEmail());
        if ($email === '') {
            throw new Server_Exception('MaburVM: the client has no email address.');
        }
        $data = ['email' => $email, 'name' => trim((string) $client->getFullName())];
        if (method_exists($client, 'getId') && $client->getId()) {
            $data['external_ref'] = 'fossbilling-client-' . $client->getId();
        }
        [$code, $resp] = $this->send('user.ensure', $data);
        if ($code < 200 || $code >= 300 || empty($resp['metadata']['user_id'])) {
            throw new Server_Exception('MaburVM: no panel account for this client: ' . self::errorMessage($resp, $code));
        }
        return $resp['metadata']['user_id'];
    }

    private static function accountRef($account): string
    {
        return 'fossbilling-' . $account->getUsername();
    }

    private static function custom($package, string $name): string
    {
        $value = $package !== null && method_exists($package, 'getCustomValue') ? $package->getCustomValue($name) : null;
        return trim((string) $value);
    }

    /** The plan when the hosting plan names one, otherwise its cpu, memory and disk custom values. */
    private static function size($package): array
    {
        $plan = self::custom($package, 'plan_id');
        if ($plan !== '') {
            return ['plan_id' => $plan];
        }
        return ['vm_specs' => [
            'cpu' => (int) (self::custom($package, 'cpu') ?: 1),
            'memory' => (int) (self::custom($package, 'memory') ?: 1024),
            'disk' => (int) (self::custom($package, 'disk') ?: 20),
        ]];
    }

    private function baseUrl(): string
    {
        $host = rtrim((string) $this->_config['host'], '/');
        if (preg_match('#^https?://#i', $host)) {
            return $host;
        }
        $scheme = !empty($this->_config['secure']) ? 'https' : 'http';
        $port = !empty($this->_config['port']) ? ':' . (int) $this->_config['port'] : '';
        return $scheme . '://' . $host . $port;
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
        $timestamp = gmdate('Y-m-d\TH:i:s\Z');
        // The panel checks the signature over the exact bytes sent: sign what is sent.
        $body = json_encode(['event' => $event, 'timestamp' => $timestamp, 'nonce' => bin2hex(random_bytes(16)), 'data' => $data], JSON_UNESCAPED_SLASHES);
        $headers = [
            'Content-Type: application/json',
            'X-API-Key: ' . $this->_config['password'],
            'X-Webhook-Signature: sha256=' . hash_hmac('sha256', $body, (string) $this->_config['accesshash']),
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
                throw new Server_Exception('MaburVM: the panel could not be reached: ' . $error);
            }
            $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
            curl_close($ch);
        }
        $decoded = json_decode((string) $raw, true);
        return [(int) $code, is_array($decoded) ? $decoded : ['raw' => $raw]];
    }
}
