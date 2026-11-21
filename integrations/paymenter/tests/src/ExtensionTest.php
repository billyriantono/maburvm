<?php

namespace MaburvmTests;

use Paymenter\Extensions\Servers\Maburvm\Maburvm;
use PHPUnit\Framework\TestCase;

/** The Paymenter server extension against a scripted panel. */
class ExtensionTest extends TestCase
{
    private FakePanel $panel;
    private Maburvm $ext;
    private Service $service;

    /** The product's settings, as Paymenter passes them. */
    private array $settings = ['template_id' => 'tmpl-debian', 'cpu' => '2', 'memory' => '4096', 'disk' => '60', 'region' => ''];

    protected function setUp(): void
    {
        $this->panel = new FakePanel();
        Maburvm::$transport = [$this->panel, 'handle'];
        $this->ext = new Maburvm(['host' => 'https://panel.example.test/', 'api_key' => FakePanel::API_KEY, 'webhook_secret' => FakePanel::SECRET]);
        $this->service = new Service(41, (object) ['id' => 7, 'email' => 'dewi@example.test', 'first_name' => 'Dewi', 'last_name' => 'Lestari']);
    }

    protected function tearDown(): void
    {
        Maburvm::$transport = null;
    }

    /** A service that was created: it remembers its panel account and VM. */
    private function created(): array
    {
        return [Maburvm::USER_ID => 'user-uuid', Maburvm::VM_ID => 'vm-uuid'];
    }

    public function testCreateFindsTheAccountOrdersTheVmAndRemembersBoth(): void
    {
        $this->panel->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid', 'created' => 'true']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid']);

        $this->assertSame(['vm_id' => 'vm-uuid'], $this->ext->createServer($this->service, $this->settings, []));

        $this->assertSame(['user.ensure', 'vm.create'], $this->panel->events());
        $this->assertSame(['email' => 'dewi@example.test', 'name' => 'Dewi Lestari', 'external_ref' => 'paymenter-user-7'],
            $this->panel->sent('user.ensure')['data']);
        $create = $this->panel->sent('vm.create');
        $this->assertSame('user-uuid', $create['data']['user_id']);
        $this->assertSame('tmpl-debian', $create['data']['template_id']);
        $this->assertSame('paymenter-41', $create['data']['external_ref']);
        $this->assertSame('svc-41', $create['data']['hostname']);
        $this->assertSame(['cpu' => 2, 'memory' => 4096, 'disk' => 60], $create['data']['vm_specs']);
        $this->assertArrayNotHasKey('region', $create['data']);
        $this->assertSame('paymenter-create-41', $create['idempotency'], 'a retried create never orders a second VM');
        $this->assertSame('https://panel.example.test/webhooks/billing', $create['url']);
        $this->assertSame('user-uuid', $this->service->props->rows[Maburvm::USER_ID]['value']);
        $this->assertSame('vm-uuid', $this->service->props->rows[Maburvm::VM_ID]['value']);
    }

    public function testWhatTheCustomerChoseWinsAndAPlanReplacesTheSize(): void
    {
        $this->panel->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])->ok('vm.create', ['vm_id' => 'vm-uuid']);

        $this->ext->createServer($this->service, $this->settings + ['plan_id' => 'plan-small'],
            ['hostname' => 'web-1.example.test', 'region' => 'jkt', 'template_id' => 'tmpl-alma']);

        $data = $this->panel->sent('vm.create')['data'];
        $this->assertSame('tmpl-alma', $data['template_id']);
        $this->assertSame('web-1.example.test', $data['hostname']);
        $this->assertSame('jkt', $data['region']);
        $this->assertSame('plan-small', $data['plan_id']);
        $this->assertArrayNotHasKey('vm_specs', $data);
    }

    public function testCreateOnAServiceThatHasAVmOrdersNothing(): void
    {
        $this->assertSame(['vm_id' => 'vm-uuid'], $this->ext->createServer($this->service, $this->settings, $this->created()));
        $this->assertSame([], $this->panel->events());
    }

    public function testCreateNeedsATemplateAndReportsARefusal(): void
    {
        try {
            $this->ext->createServer($this->service, ['template_id' => ''], []);
            $this->fail('created without a template');
        } catch (\Exception $e) {
            $this->assertStringContainsString('Template ID', $e->getMessage());
        }
        $this->assertSame([], $this->panel->events(), 'a product without a template makes no account and orders nothing');

        $this->panel->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']]);
        $this->panel->answer('vm.create', 409, ['success' => false, 'error' => ['code' => 'conflict', 'message' => 'no free address on the node'],
            'message' => 'no free address on the node']);
        $this->expectExceptionMessage('MaburVM create failed: no free address on the node');
        $this->ext->createServer($this->service, $this->settings, []);
    }

    public function testLifecycleEventsNameTheVmItsOwnerAndItsService(): void
    {
        foreach (['vm.suspend', 'vm.unsuspend', 'vm.destroy', 'vm.resize'] as $event) {
            $this->panel->ok($event);
        }
        $this->assertTrue($this->ext->suspendServer($this->service, $this->settings, $this->created()));
        $this->assertTrue($this->ext->unsuspendServer($this->service, $this->settings, $this->created()));
        $this->assertTrue($this->ext->terminateServer($this->service, $this->settings, $this->created()));
        $this->assertTrue($this->ext->upgradeServer($this->service, ['plan_id' => 'plan-large'], $this->created()));

        $this->assertSame(['vm.suspend', 'vm.unsuspend', 'vm.destroy', 'vm.resize'], $this->panel->events());
        foreach ($this->panel->requests as $r) {
            $this->assertSame('user-uuid', $r['data']['user_id']);
            $this->assertSame('vm-uuid', $r['data']['vm_id']);
            $this->assertSame('paymenter-41', $r['data']['external_ref']);
        }
        $this->assertSame('plan-large', $this->panel->sent('vm.resize')['data']['plan_id']);
    }

    public function testAVmAlreadyGoneIsTerminatedAndOtherRefusalsAreErrors(): void
    {
        $this->panel->answer('vm.destroy', 404, ['success' => false, 'error' => ['code' => 'not_found', 'message' => 'VM not found'], 'message' => 'VM not found']);
        $this->assertTrue($this->ext->terminateServer($this->service, $this->settings, $this->created()));

        $this->panel->answer('vm.suspend', 403, ['success' => false, 'error' => ['code' => 'forbidden', 'message' => 'the VM does not belong to the user this event names'],
            'message' => 'the VM does not belong to the user this event names']);
        $this->expectExceptionMessage('the VM does not belong to the user');
        $this->ext->suspendServer($this->service, $this->settings, $this->created());
    }

    public function testAnActionOnAServiceWithoutAnAccountMakesNone(): void
    {
        try {
            $this->ext->suspendServer($this->service, $this->settings, []);
            $this->fail('suspended a service with no panel account');
        } catch (\Exception $e) {
            $this->assertStringContainsString('no panel account', $e->getMessage());
        }
        $this->assertSame([], $this->panel->events(), 'only a create may make an account');
    }

    public function testTheServicePageOffersAOneTimeSignInLink(): void
    {
        $this->panel->ok('user.sso', ['metadata' => ['path' => '/sso?ticket=abc']])
            ->ok('user.sso', ['metadata' => ['path' => '/sso?ticket=def', 'url' => 'https://portal.example.test/sso?ticket=def']]);

        $actions = $this->ext->getActions($this->service, $this->settings, $this->created());
        $this->assertSame([['type' => 'button', 'label' => 'Open control panel', 'url' => 'https://panel.example.test/sso?ticket=abc']], $actions);
        $sso = $this->panel->sent('user.sso')['data'];
        $this->assertSame(['user_id' => 'user-uuid', 'external_ref' => 'paymenter-41', 'vm_id' => 'vm-uuid'], $sso);

        $this->assertSame('https://portal.example.test/sso?ticket=def', $this->ext->ssoLink($this->service, $this->created(), 'console'));
        $this->assertSame('console', $this->panel->sent('user.sso', 1)['data']['page']);

        $this->assertSame([], $this->ext->getActions($this->service, $this->settings, []), 'no account, no button');
    }

    public function testAPanelThatCannotBeReachedLeavesTheServicePageWithoutAButton(): void
    {
        $this->panel->answer('user.sso', 0, []);
        $this->assertSame([], $this->ext->getActions($this->service, $this->settings, $this->created()));
    }

    public function testTestConfigProvesTheCredentials(): void
    {
        $this->panel->ok('catalog.list', ['data' => ['templates' => [], 'plans' => [], 'regions' => []]]);
        $this->assertTrue($this->ext->testConfig());

        $wrong = new Maburvm(['host' => 'https://panel.example.test', 'api_key' => FakePanel::API_KEY, 'webhook_secret' => 'not-the-secret']);
        $this->assertSame('Invalid webhook signature', $wrong->testConfig());

        $empty = new Maburvm(['host' => 'https://panel.example.test']);
        $this->assertStringContainsString('no billing API key', $empty->testConfig());
    }

    public function testTheSettingsAskForWhatThePanelNeeds(): void
    {
        $this->assertSame(['host', 'api_key', 'webhook_secret'], array_column($this->ext->getConfig(), 'name'));
        $this->assertSame(['template_id', 'plan_id', 'cpu', 'memory', 'disk', 'region'], array_column($this->ext->getProductConfig(), 'name'));
    }
}
