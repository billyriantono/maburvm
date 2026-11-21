<?php

namespace MaburvmTests;

use PHPUnit\Framework\TestCase;

/** The FOSSBilling server manager against a scripted panel. */
class ManagerTest extends TestCase
{
    private FakePanel $panel;
    private \Server_Manager_Maburvm $manager;

    protected function setUp(): void
    {
        $this->panel = new FakePanel();
        \Server_Manager_Maburvm::$transport = [$this->panel, 'handle'];
        $this->manager = $this->manager();
        // Every action first finds the client's panel account.
        $this->panel->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid', 'created' => 'false']]);
    }

    protected function tearDown(): void
    {
        \Server_Manager_Maburvm::$transport = null;
    }

    private function manager(array $over = []): \Server_Manager_Maburvm
    {
        return new \Server_Manager_Maburvm($over + ['host' => 'panel.example.test', 'secure' => true,
            'username' => 'unused', 'password' => FakePanel::API_KEY, 'accesshash' => FakePanel::SECRET]);
    }

    private function account(array $custom = ['template_id' => 'tmpl-debian', 'cpu' => '2', 'memory' => '4096', 'disk' => '60'], string $password = 'Sup3rSecret!'): \Server_Account
    {
        return new \Server_Account('vps41', $password, 'web-1.example.test', new \Server_Package($custom),
            new \Server_Client(7, 'dewi@example.test', 'Dewi Lestari'));
    }

    public function testCreateFindsTheAccountAndOrdersTheVmForThisAccountReference(): void
    {
        $this->panel->ok('vm.create', ['vm_id' => 'vm-uuid']);

        $this->assertTrue($this->manager->createAccount($this->account()));

        $this->assertSame(['user.ensure', 'vm.create'], $this->panel->events());
        $this->assertSame(['email' => 'dewi@example.test', 'name' => 'Dewi Lestari', 'external_ref' => 'fossbilling-client-7'],
            $this->panel->sent('user.ensure')['data']);
        $create = $this->panel->sent('vm.create');
        $this->assertSame('user-uuid', $create['data']['user_id']);
        $this->assertSame('web-1.example.test', $create['data']['hostname']);
        $this->assertSame('tmpl-debian', $create['data']['template_id']);
        $this->assertSame('fossbilling-vps41', $create['data']['external_ref']);
        $this->assertSame('Sup3rSecret!', $create['data']['password'], 'the account password becomes the root password');
        $this->assertSame(['cpu' => 2, 'memory' => 4096, 'disk' => 60], $create['data']['vm_specs']);
        $this->assertSame('fossbilling-create-vps41', $create['idempotency']);
        $this->assertSame('https://panel.example.test/webhooks/billing', $create['url']);
    }

    public function testAPlanAndRegionComeFromTheHostingPlanAndAShortPasswordIsNotSent(): void
    {
        $this->panel->ok('vm.create', ['vm_id' => 'vm-uuid']);

        $this->manager->createAccount($this->account(['template_id' => 'tmpl-alma', 'plan_id' => 'plan-small', 'region' => 'jkt'], 'short'));

        $data = $this->panel->sent('vm.create')['data'];
        $this->assertSame('plan-small', $data['plan_id']);
        $this->assertSame('jkt', $data['region']);
        $this->assertArrayNotHasKey('vm_specs', $data);
        $this->assertArrayNotHasKey('password', $data, 'the panel makes one instead');
    }

    public function testCreateNeedsATemplateAndReportsARefusal(): void
    {
        try {
            $this->manager->createAccount($this->account(['cpu' => '1']));
            $this->fail('created without a template');
        } catch (\Server_Exception $e) {
            $this->assertStringContainsString('template_id', $e->getMessage());
        }
        $this->assertSame([], $this->panel->events(), 'a plan without a template makes no account and orders nothing');

        $this->panel->answer('vm.create', 409, ['success' => false, 'error' => ['code' => 'conflict', 'message' => 'no free address on the node'],
            'message' => 'no free address on the node']);
        $this->expectException(\Server_Exception::class);
        $this->expectExceptionMessage('MaburVM create failed: no free address on the node');
        $this->manager->createAccount($this->account());
    }

    public function testLifecycleEventsNameTheVmByItsOwnerAndAccountReference(): void
    {
        foreach (['vm.suspend', 'vm.unsuspend', 'vm.destroy', 'vm.resize', 'vm.reset_password'] as $event) {
            $this->panel->ok($event);
        }
        $account = $this->account();
        $this->assertTrue($this->manager->suspendAccount($account));
        $this->assertTrue($this->manager->unsuspendAccount($account));
        $this->assertTrue($this->manager->cancelAccount($account));
        $this->assertTrue($this->manager->changeAccountPackage($account, new \Server_Package(['plan_id' => 'plan-large'])));
        $this->assertTrue($this->manager->changeAccountPassword($account, 'N3wPassword!'));

        $vmEvents = array_values(array_filter($this->panel->requests, fn (array $r) => $r['event'] !== 'user.ensure'));
        $this->assertSame(['vm.suspend', 'vm.unsuspend', 'vm.destroy', 'vm.resize', 'vm.reset_password'], array_column($vmEvents, 'event'));
        foreach ($vmEvents as $r) {
            $this->assertSame('user-uuid', $r['data']['user_id']);
            $this->assertSame('fossbilling-vps41', $r['data']['external_ref']);
            $this->assertArrayNotHasKey('vm_id', $r['data'], 'FOSSBilling keeps no VM id; the panel finds it by reference and owner');
        }
        $this->assertSame('plan-large', $this->panel->sent('vm.resize')['data']['plan_id']);
        $this->assertSame('N3wPassword!', $this->panel->sent('vm.reset_password')['data']['password']);
    }

    public function testAVmAlreadyGoneIsCancelledAndOtherRefusalsAreErrors(): void
    {
        $this->panel->answer('vm.destroy', 404, ['success' => false, 'error' => ['code' => 'not_found', 'message' => 'no VM of this user carries external_ref'],
            'message' => 'no VM of this user carries external_ref']);
        $this->assertTrue($this->manager->cancelAccount($this->account()));

        $this->panel->answer('vm.suspend', 404, ['success' => false, 'error' => ['code' => 'not_found', 'message' => 'no VM of this user carries external_ref'],
            'message' => 'no VM of this user carries external_ref']);
        $this->expectExceptionMessage('MaburVM vm.suspend failed: no VM of this user carries external_ref');
        $this->manager->suspendAccount($this->account());
    }

    public function testRenamesAreRefusedWithAReason(): void
    {
        foreach (['changeAccountUsername', 'changeAccountDomain', 'changeAccountIp'] as $method) {
            try {
                $this->manager->$method($this->account(), 'new');
                $this->fail($method . ' did not refuse');
            } catch (\Server_Exception $e) {
                $this->assertStringContainsString('MaburVM', $e->getMessage());
            }
        }
        $this->assertSame([], $this->panel->events());
    }

    public function testSynchronizeReadsTheVmsAddress(): void
    {
        $this->panel->ok('vm.status', ['metadata' => ['state' => 'ready', 'ip' => '203.0.113.10']]);
        $account = $this->manager->synchronizeAccount($this->account());
        $this->assertSame('203.0.113.10', $account->getIp());
        $this->assertSame(['user_id' => 'user-uuid', 'external_ref' => 'fossbilling-vps41'], $this->panel->sent('vm.status')['data']);
    }

    public function testTheLoginLinkSignsTheClientInOnceAndFallsBackToTheSignInPage(): void
    {
        $this->panel->ok('user.sso', ['metadata' => ['path' => '/sso?ticket=abc']])
            ->answer('user.sso', 403, ['success' => false, 'error' => ['code' => 'forbidden', 'message' => 'account suspended'], 'message' => 'account suspended']);

        $this->assertSame('https://panel.example.test/sso?ticket=abc', $this->manager->getLoginUrl($this->account()));
        $this->assertSame(['user_id' => 'user-uuid', 'external_ref' => 'fossbilling-vps41'], $this->panel->sent('user.sso')['data']);
        $this->assertSame('https://panel.example.test/login', $this->manager->getLoginUrl($this->account()));
        $this->assertSame('https://panel.example.test/login', $this->manager->getLoginUrl(null));
        $this->assertSame('https://panel.example.test/login', $this->manager->getResellerLoginUrl(null));
    }

    public function testAClientWithoutAnIdIsFoundByEmail(): void
    {
        $client = new class {
            public function getEmail()
            {
                return 'ayu@example.test';
            }

            public function getFullName()
            {
                return 'Ayu';
            }
        };
        $this->panel->ok('vm.suspend');
        $this->manager->suspendAccount(new \Server_Account('vps9', 'x', '', new \Server_Package([]), $client));
        $this->assertSame(['email' => 'ayu@example.test', 'name' => 'Ayu'], $this->panel->sent('user.ensure')['data']);
    }

    public function testTheServerSettingsAndConnection(): void
    {
        $this->assertSame('http://panel.example.test:8080/login', $this->manager(['secure' => false, 'port' => 8080])->getLoginUrl(null));
        $this->assertSame('https://panel.example.test/login', $this->manager(['host' => 'https://panel.example.test/'])->getLoginUrl(null));

        $this->panel->ok('catalog.list', ['data' => ['templates' => []]]);
        $this->assertTrue($this->manager->testConnection());

        $this->expectException(\Server_Exception::class);
        $this->expectExceptionMessage('Invalid webhook signature');
        $this->manager(['accesshash' => 'not-the-secret'])->testConnection();
    }

    public function testAServerWithoutCredentialsIsRefusedAtOnce(): void
    {
        $this->expectException(\Server_Exception::class);
        $this->expectExceptionMessage('billing API key');
        new \Server_Manager_Maburvm(['host' => 'panel.example.test']);
    }
}
