<?php

/**
 * Stand-ins for the parts of WHMCS the modules call, so they can be exercised
 * without a WHMCS install. These mimic the documented behaviour only: passing
 * here does not prove the modules run inside a real WHMCS.
 */

namespace WHMCS\Database {

    /** In-memory tables with the few query-builder calls the modules use. */
    class Capsule
    {
        /** @var array<string, array<int, array<string, mixed>>> */
        public static array $tables = [];

        public static function reset(): void
        {
            self::$tables = [];
        }

        public static function table(string $name): Query
        {
            return new Query($name);
        }

        public static function raw(string $sql): string
        {
            return $sql;
        }

        public static function connection(): object
        {
            return new class {
                public function transaction(callable $work): mixed
                {
                    return $work($this);
                }
            };
        }

        public static function schema(): object
        {
            return new class {
                public function hasTable(string $name): bool
                {
                    return true;
                }
            };
        }
    }

    class Query
    {
        private array $filters = [];
        private ?int $limit = null;

        public function __construct(private string $table)
        {
            Capsule::$tables[$table] ??= [];
        }

        public function where(string $column, mixed $value): static
        {
            $this->filters[] = fn (array $row) => ($row[$column] ?? null) == $value;
            return $this;
        }

        public function whereIn(string $column, array $values): static
        {
            $this->filters[] = fn (array $row) => in_array($row[$column] ?? null, $values);
            return $this;
        }

        public function orderBy(string $column, string $direction = 'asc'): static
        {
            return $this;
        }

        public function lockForUpdate(): static
        {
            return $this;
        }

        public function limit(int $n): static
        {
            $this->limit = $n;
            return $this;
        }

        private function keys(): array
        {
            $keys = [];
            foreach (Capsule::$tables[$this->table] as $i => $row) {
                foreach ($this->filters as $filter) {
                    if (!$filter($row)) {
                        continue 2;
                    }
                }
                $keys[] = $i;
            }
            return $this->limit === null ? $keys : array_slice($keys, 0, $this->limit);
        }

        public function get(): array
        {
            return array_map(fn (int $i) => (object) Capsule::$tables[$this->table][$i], $this->keys());
        }

        public function first(): ?object
        {
            return $this->get()[0] ?? null;
        }

        public function exists(): bool
        {
            return (bool) $this->keys();
        }

        public function value(string $column): mixed
        {
            $row = $this->first();
            return $row === null ? null : ($row->$column ?? null);
        }

        public function pluck(string $column): array
        {
            return array_map(fn (object $row) => $row->$column ?? null, $this->get());
        }

        public function update(array $fields): int
        {
            foreach ($this->keys() as $i) {
                Capsule::$tables[$this->table][$i] = $fields + Capsule::$tables[$this->table][$i];
            }
            return count($this->keys());
        }

        public function insert(array $row): bool
        {
            Capsule::$tables[$this->table][] = $row;
            return true;
        }

        public function delete(): int
        {
            $keys = $this->keys();
            foreach ($keys as $i) {
                unset(Capsule::$tables[$this->table][$i]);
            }
            Capsule::$tables[$this->table] = array_values(Capsule::$tables[$this->table]);
            return count($keys);
        }
    }
}

namespace WHMCS\Service {

    use WHMCS\Database\Capsule;

    /** A service row with its properties, as the usage sync reads it. */
    class Service
    {
        public static function where(string $column, mixed $value): object
        {
            return new class ($column, $value) {
                private array $filters = [];

                public function __construct(string $column, mixed $value)
                {
                    $this->filters[$column] = $value;
                }

                public function where(string $column, mixed $value): static
                {
                    $this->filters[$column] = $value;
                    return $this;
                }

                public function get(): array
                {
                    $query = Capsule::table('tblhosting');
                    foreach ($this->filters as $column => $value) {
                        $query->where($column, $value);
                    }
                    return array_map(function (object $row) {
                        $row->serviceProperties = new \MaburvmTests\Properties($row->id);
                        return $row;
                    }, $query->get());
                }
            };
        }
    }
}

namespace MaburvmTests {

    /** Service properties: values kept per service, as WHMCS keeps them in custom fields. */
    class Properties
    {
        /** @var array<int|string, array<string, string>> */
        public static array $store = [];

        public function __construct(private int|string $serviceId)
        {
        }

        public function get(string $name): ?string
        {
            return self::$store[$this->serviceId][$name] ?? null;
        }

        public function save(array $values): void
        {
            foreach ($values as $name => $value) {
                self::$store[$this->serviceId][$name] = (string) $value;
            }
        }
    }

    /** What the stubs below recorded. */
    class Recorder
    {
        public static array $activity = [];
        public static array $moduleCalls = [];
        public static array $apiCalls = [];
        public static array $apiResults = [];

        public static function reset(): void
        {
            self::$activity = self::$moduleCalls = self::$apiCalls = self::$apiResults = [];
            Properties::$store = [];
            \WHMCS\Database\Capsule::reset();
            unset($GLOBALS['maburvm_http'], $GLOBALS['maburvm_sleep']);
        }
    }
}

namespace {

    define('WHMCS', true);

    function logModuleCall($module, $action, $request, $response, $data = '', $replace = [])
    {
        \MaburvmTests\Recorder::$moduleCalls[] = [$action, is_string($response) ? $response : json_encode($response)];
    }

    function logActivity($message, $userId = 0)
    {
        \MaburvmTests\Recorder::$activity[] = [$message, (int) $userId];
    }

    function localAPI($command, $values = [], $adminUser = null)
    {
        \MaburvmTests\Recorder::$apiCalls[] = [$command, $values];
        $result = \MaburvmTests\Recorder::$apiResults[$command] ?? ['result' => 'error', 'message' => 'no stub for ' . $command];
        return is_callable($result) ? $result($values) : $result;
    }

    function encrypt($value)
    {
        return 'enc:' . $value;
    }

    require __DIR__ . '/../modules/servers/maburvm/maburvm.php';
    require __DIR__ . '/../modules/addons/maburvm_events/maburvm_events.php';
    require __DIR__ . '/src/FakePanel.php';
}
