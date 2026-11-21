<?php

/**
 * Stand-ins for the parts of FOSSBilling the server manager uses, written from
 * its documented server manager interface (library/Server/*.php), plus a
 * scripted panel. The abstract methods carry the strictest signatures recent
 * FOSSBilling releases declare, so an incompatible override fails here.
 * Passing does not prove the manager runs inside a real FOSSBilling.
 */

namespace {

    class Server_Exception extends Exception
    {
    }

    abstract class Server_Manager
    {
        protected array $_config = [
            'ip' => null, 'host' => null, 'port' => null, 'config' => null, 'secure' => false,
            'username' => null, 'password' => null, 'accesshash' => null, 'passwordLength' => null,
        ];

        public function __construct(array $options)
        {
            foreach (array_keys($this->_config) as $key) {
                if (isset($options[$key])) {
                    $this->_config[$key] = $options[$key];
                }
            }
            $this->init();
        }

        abstract protected function init();

        public static function getForm(): array
        {
            return [];
        }

        abstract public function getLoginUrl(?Server_Account $account): string;

        abstract public function getResellerLoginUrl(?Server_Account $account): string;

        abstract public function testConnection(): bool;

        abstract public function synchronizeAccount(Server_Account $account): Server_Account;

        abstract public function createAccount(Server_Account $account): bool;

        abstract public function suspendAccount(Server_Account $account): bool;

        abstract public function unsuspendAccount(Server_Account $account): bool;

        abstract public function cancelAccount(Server_Account $account): bool;

        abstract public function changeAccountPackage(Server_Account $account, Server_Package $package): bool;

        abstract public function changeAccountUsername(Server_Account $account, string $newUsername): bool;

        abstract public function changeAccountDomain(Server_Account $account, string $newDomain): bool;

        abstract public function changeAccountPassword(Server_Account $account, string $newPassword): bool;

        abstract public function changeAccountIp(Server_Account $account, string $newIp): bool;
    }

    class Server_Package
    {
        public function __construct(private array $custom = [])
        {
        }

        public function getCustomValue($param)
        {
            return $this->custom[$param] ?? null;
        }
    }

    class Server_Client
    {
        public function __construct(private int $id, private string $email, private string $fullName)
        {
        }

        public function getId()
        {
            return $this->id;
        }

        public function getEmail()
        {
            return $this->email;
        }

        public function getFullName()
        {
            return $this->fullName;
        }
    }

    class Server_Account
    {
        private $ip = null;

        public function __construct(private string $username, private string $password, private string $domain,
            private ?Server_Package $package, private object $client)
        {
        }

        public function getUsername()
        {
            return $this->username;
        }

        public function getPassword()
        {
            return $this->password;
        }

        public function getDomain()
        {
            return $this->domain;
        }

        public function getPackage()
        {
            return $this->package;
        }

        public function getClient()
        {
            return $this->client;
        }

        public function getIp()
        {
            return $this->ip;
        }

        public function setIp($ip)
        {
            $this->ip = $ip;
            return $this;
        }
    }

    require __DIR__ . '/../library/Server/Manager/Maburvm.php';
}

namespace MaburvmTests {

    /**
     * The panel's billing webhook: it checks each request as the panel does
     * (API key, signature over the exact body, a fresh timestamp, a nonce) and
     * answers from a script.
     */
    class FakePanel
    {
        public const API_KEY = 'test-api-key';
        public const SECRET = 'test-signing-secret';

        /** @var array<int, array{event: string, data: array, idempotency: string, url: string, timeout: int}> */
        public array $requests = [];
        /** @var array<string, array<int, array{0: int, 1: array}>> answers per event, in order; the last one repeats */
        public array $answers = [];

        public function answer(string $event, int $code, array $body): static
        {
            $this->answers[$event][] = [$code, $body];
            return $this;
        }

        public function ok(string $event, array $body = []): static
        {
            return $this->answer($event, 200, ['success' => true] + $body);
        }

        public function events(): array
        {
            return array_column($this->requests, 'event');
        }

        public function sent(string $event, int $n = 0): ?array
        {
            $matches = array_values(array_filter($this->requests, fn (array $r) => $r['event'] === $event));
            return $matches[$n] ?? null;
        }

        public function handle(string $url, string $body, array $headers, int $timeout = 30): array
        {
            $h = [];
            foreach ($headers as $line) {
                [$name, $value] = explode(': ', $line, 2);
                $h[strtolower($name)] = $value;
            }
            if (($h['x-api-key'] ?? '') !== self::API_KEY) {
                return [401, json_encode(['success' => false, 'message' => 'Invalid API key'])];
            }
            if (!hash_equals('sha256=' . hash_hmac('sha256', $body, self::SECRET), $h['x-webhook-signature'] ?? '')) {
                return [401, json_encode(['success' => false, 'message' => 'Invalid webhook signature'])];
            }
            $payload = json_decode($body, true);
            if (!is_array($payload) || abs(time() - strtotime($payload['timestamp'] ?? '')) > 300 || empty($payload['nonce'])) {
                return [400, json_encode(['success' => false, 'message' => 'timestamp and nonce are required'])];
            }
            $event = $payload['event'];
            $this->requests[] = ['event' => $event, 'data' => (array) $payload['data'],
                'idempotency' => $h['x-idempotency-key'] ?? '', 'url' => $url, 'timeout' => $timeout];
            $queue = &$this->answers[$event];
            if (empty($queue)) {
                return [400, json_encode(['success' => false, 'message' => 'Invalid event type: ' . $event])];
            }
            [$code, $answer] = count($queue) > 1 ? array_shift($queue) : $queue[0];
            if ($code === 0) {
                throw new \Server_Exception('the panel could not be reached: timed out');
            }
            return [$code, json_encode($answer)];
        }
    }
}
