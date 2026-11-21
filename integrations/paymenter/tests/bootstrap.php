<?php

/**
 * Stand-ins for the parts of Paymenter the extension uses, written from its
 * documented extension interface, plus a scripted panel. Passing here does not
 * prove the extension runs inside a real Paymenter.
 */

namespace App\Classes\Extension {

    abstract class Extension
    {
        public function __construct(public array $config = [])
        {
        }

        public function config($key)
        {
            return $this->config[$key] ?? null;
        }
    }

    abstract class Server extends Extension
    {
    }
}

namespace MaburvmTests {

    /** A service's property rows, as $service->properties() hands them out. */
    class Properties
    {
        /** @var array<string, array{name: string, value: string}> */
        public array $rows = [];

        public function updateOrCreate(array $attributes, array $values): object
        {
            $this->rows[$attributes['key']] = $values;
            return (object) ($attributes + $values);
        }
    }

    /** A Paymenter service of user $user. */
    class Service
    {
        public Properties $props;

        public function __construct(public int $id, public object $user)
        {
            $this->props = new Properties();
        }

        public function properties(): Properties
        {
            return $this->props;
        }
    }

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
                throw new \Exception('the panel could not be reached: timed out');
            }
            return [$code, json_encode($answer)];
        }
    }
}

namespace {
    require __DIR__ . '/../extensions/Servers/Maburvm/Maburvm.php';
}
