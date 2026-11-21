<?php

namespace MaburvmTests;

/**
 * Stands in for the panel's billing webhook. It checks each request the way
 * the panel does (API key, signature over the exact body, a fresh timestamp, a
 * nonce) and answers from a script, so the tests see what the module sends.
 */
class FakePanel
{
    public const API_KEY = 'test-api-key';
    public const SECRET = 'test-signing-secret';

    /** @var array<int, array{event: string, data: array, idempotency: string, url: string, timeout: int}> */
    public array $requests = [];
    /** @var array<string, array<int, array{0: int, 1: array}>> scripted answers per event, used in order; the last one repeats */
    public array $answers = [];
    public int $slept = 0;

    public function install(): void
    {
        $GLOBALS['maburvm_http'] = [$this, 'handle'];
        $GLOBALS['maburvm_sleep'] = function (int $seconds): void {
            $this->slept += $seconds;
        };
    }

    public function answer(string $event, int $code, array $body): static
    {
        $this->answers[$event][] = [$code, $body];
        return $this;
    }

    public function ok(string $event, array $body = []): static
    {
        return $this->answer($event, 200, ['success' => true] + $body);
    }

    /** The events sent so far, in order. */
    public function events(): array
    {
        return array_column($this->requests, 'event');
    }

    /** The data of the n-th request for an event (0 = first). */
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
        $this->requests[] = [
            'event' => $event,
            'data' => (array) $payload['data'],
            'idempotency' => $h['x-idempotency-key'] ?? '',
            'url' => $url,
            'timeout' => $timeout,
        ];
        $queue = &$this->answers[$event];
        if (empty($queue)) {
            return [400, json_encode(['success' => false, 'message' => 'Invalid event type: ' . $event])];
        }
        [$code, $answer] = count($queue) > 1 ? array_shift($queue) : $queue[0];
        if ($code === 0) { // scripted as unreachable
            throw new \Exception('Failed to reach MaburVM panel: timed out');
        }
        return [$code, json_encode($answer)];
    }
}
