<?php

namespace MaburvmTests;

use PHPUnit\Framework\TestCase;
use WHMCS\Database\Capsule;

/** The provisioning module against a scripted panel. */
class ModuleTest extends TestCase
{
    private FakePanel $panel;

    protected function setUp(): void
    {
        Recorder::reset();
        $this->panel = new FakePanel();
        $this->panel->install();
        Capsule::table('tblhosting')->insert(['id' => 41, 'userid' => 7, 'server' => 3, 'domainstatus' => 'Active',
            'dedicatedip' => '', 'assignedips' => '', 'username' => '', 'password' => 'enc:Sup3rSecret!']);
    }

    /** What WHMCS passes a module function for service 41 of client 7. */
    private function params(array $over = []): array
    {
        return $over + [
            'serviceid' => 41,
            'userid' => 7,
            'serverid' => 3,
            'serversecure' => 'on',
            'serverhostname' => 'panel.example.test',
            'serverip' => '',
            'serverpassword' => FakePanel::API_KEY,
            'serveraccesshash' => FakePanel::SECRET,
            'domain' => 'shop.example.test',
            'password' => 'Sup3rSecret!',
            'configoption1' => 'tmpl-debian',
            'configoption2' => '2',
            'configoption3' => '4096',
            'configoption4' => '60',
            'configoption5' => '',
            'configoption6' => '',
            'configoption7' => '20',
            'configoptions' => [],
            'customfields' => [],
            'clientsdetails' => ['userid' => 7, 'email' => 'dewi@example.test', 'firstname' => 'Dewi', 'lastname' => 'Lestari'],
            'model' => new class {
                public Properties $serviceProperties;

                public function __construct()
                {
                    $this->serviceProperties = new Properties(41);
                }
            },
        ];
    }

    private function service(): object
    {
        return Capsule::table('tblhosting')->where('id', 41)->first();
    }

    private function readyPanel(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid', 'created' => 'true']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid'])
            ->ok('vm.status', ['metadata' => ['state' => 'provisioning', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']])
            ->ok('vm.status', ['metadata' => ['state' => 'ready', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10,203.0.113.11', 'username' => 'root']]);
    }

    public function testCreateMakesTheAccountOrdersTheVmAndFillsInTheService(): void
    {
        $this->readyPanel();

        $this->assertSame('success', maburvm_CreateAccount($this->params()));

        $this->assertSame(['user.ensure', 'vm.create', 'vm.status', 'vm.status'], $this->panel->events());
        $this->assertSame(
            ['email' => 'dewi@example.test', 'name' => 'Dewi Lestari', 'external_ref' => 'whmcs-client-7'],
            $this->panel->sent('user.ensure')['data']
        );
        $create = $this->panel->sent('vm.create');
        $this->assertSame('https://panel.example.test/webhooks/billing', $create['url']);
        $this->assertSame('whmcs-create-41', $create['idempotency']);
        $this->assertSame([
            'user_id' => 'user-uuid',
            'hostname' => 'shop.example.test',
            'template_id' => 'tmpl-debian',
            'external_ref' => 'whmcs-41',
            'password' => 'Sup3rSecret!',
            'vm_specs' => ['cpu' => 2, 'memory' => 4096, 'disk' => 60],
        ], $create['data']);
        $this->assertSame(MABURVM_POLL_INTERVAL, $this->panel->slept);

        $this->assertSame('user-uuid', Properties::$store[41]['panel_user_id']);
        $this->assertSame('vm-uuid', Properties::$store[41]['vm_id']);
        $service = $this->service();
        $this->assertSame('203.0.113.10', $service->dedicatedip);
        $this->assertSame('203.0.113.11', $service->assignedips);
        $this->assertSame('root', $service->username);
        $this->assertSame('enc:Sup3rSecret!', $service->password, 'the password WHMCS already holds is the one that was set');
    }

    public function testAPanelUserIdTypedByStaffIsUsedAsItIs(): void
    {
        $this->readyPanel();

        $this->assertSame('success', maburvm_CreateAccount($this->params(['customfields' => ['Panel User ID' => ' legacy-user ']])));

        $this->assertNotContains('user.ensure', $this->panel->events());
        $this->assertSame('legacy-user', $this->panel->sent('vm.create')['data']['user_id']);
    }

    public function testWhatTheCustomerChoseAtCheckoutWinsOverTheProduct(): void
    {
        $this->readyPanel();
        $params = $this->params([
            'configoption5' => 'plan-small',
            'configoption6' => 'jakarta',
            'configoptions' => ['Operating System' => 'tmpl-windows', 'Plan' => 'plan-large', 'Region' => 'singapore', 'Bandwidth' => '250', 'Traffic' => '2000'],
        ]);

        $this->assertSame('success', maburvm_CreateAccount($params));

        $data = $this->panel->sent('vm.create')['data'];
        $this->assertSame('tmpl-windows', $data['template_id']);
        $this->assertSame('plan-large', $data['plan_id']);
        $this->assertSame('singapore', $data['region']);
        $this->assertSame(250, $data['bandwidth_mbps']);
        $this->assertSame(2000, $data['data_quota_gb']);
        $this->assertArrayNotHasKey('vm_specs', $data, 'a plan decides the size');
    }

    public function testTheProductsPlanAndRegionAreSentWhenTheCustomerChoseNone(): void
    {
        $this->readyPanel();

        maburvm_CreateAccount($this->params(['configoption5' => 'plan-small', 'configoption6' => 'jakarta']));

        $data = $this->panel->sent('vm.create')['data'];
        $this->assertSame('plan-small', $data['plan_id']);
        $this->assertSame('jakarta', $data['region']);
    }

    public function testAnInstallationThatFailsIsAnErrorNotASuccess(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid'])
            ->ok('vm.status', ['metadata' => ['state' => 'failed', 'error' => 'the node ran out of disk', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        $result = maburvm_CreateAccount($this->params());

        $this->assertNotSame('success', $result);
        $this->assertStringContainsString('the node ran out of disk', $result);
        $this->assertStringContainsString('Terminate', $result);
        $this->assertSame('vm-uuid', Properties::$store[41]['vm_id'], 'the failed VM stays known, so Terminate can remove it');
    }

    public function testAFailureThePanelIsStillRetryingSaysSoAndDoesNotAdviseTerminate(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid'])
            ->ok('vm.status', ['metadata' => ['state' => 'failed', 'retrying' => 'true', 'error' => 'the node ran out of disk', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        $result = maburvm_CreateAccount($this->params());

        $this->assertStringContainsString('not installed yet: the node ran out of disk', $result);
        $this->assertStringContainsString('still trying', $result);
        $this->assertStringNotContainsString('remove the failed VM', $result);
    }

    public function testCreateOnAServiceWhoseInstalledVmIsBrokenNeitherSucceedsNorOrdersAnother(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.status', ['metadata' => ['state' => 'error', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        $result = maburvm_CreateAccount($this->params());

        $this->assertStringContainsString('error state on the panel', $result);
        $this->assertStringNotContainsString('Terminate', $result, 'a customer\'s installed VM is never called a failed order');
        $this->assertNotContains('vm.create', $this->panel->events());
    }

    public function testAnOrderThePanelRefusesIsAnError(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])
            ->answer('vm.create', 400, ['success' => false, 'message' => 'failed to create VM: this operating system is not offered on this plan']);

        $result = maburvm_CreateAccount($this->params());

        $this->assertStringContainsString('not offered on this plan', $result);
        $this->assertArrayNotHasKey('vm_id', Properties::$store[41] ?? []);
        $this->assertSame('', $this->service()->dedicatedip);
    }

    public function testNoAccountMeansNoOrder(): void
    {
        $this->panel->answer('user.ensure', 409, ['success' => false, 'message' => 'that email belongs to a staff account']);

        $result = maburvm_CreateAccount($this->params());

        $this->assertStringContainsString('staff account', $result);
        $this->assertSame(['user.ensure'], $this->panel->events());
    }

    public function testAVmStillInstallingAfterTheWaitKeepsTheOrderAndItsAddress(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid'])
            ->ok('vm.status', ['metadata' => ['state' => 'provisioning', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'Administrator']]);

        $this->assertSame('success', maburvm_CreateAccount($this->params(['configoption7' => '10'])));

        $this->assertSame(10, $this->panel->slept, 'it waited as long as the product says, and no longer');
        $this->assertSame('203.0.113.10', $this->service()->dedicatedip);
        $this->assertSame('Administrator', $this->service()->username);
        $this->assertStringContainsString('still being installed', Recorder::$moduleCalls[0][1]);
    }

    public function testAWaitOfZeroAsksOnceAndDoesNotSleep(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid'])
            ->ok('vm.status', ['metadata' => ['state' => 'provisioning', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        $this->assertSame('success', maburvm_CreateAccount($this->params(['configoption7' => '0'])));
        $this->assertSame(0, $this->panel->slept);
        $this->assertCount(1, array_keys($this->panel->events(), 'vm.status'));
    }

    public function testCreateAgainFinishesTheFirstOrderInsteadOfPlacingASecond(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.status', ['metadata' => ['state' => 'ready', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        $this->assertSame('success', maburvm_CreateAccount($this->params()));

        $this->assertNotContains('vm.create', $this->panel->events());
        $this->assertSame('203.0.113.10', $this->service()->dedicatedip);
    }

    public function testCreateAfterTheVmWasRemovedOrdersANewOne(): void
    {
        Properties::$store[41] = ['vm_id' => 'old-vm', 'panel_user_id' => 'user-uuid'];
        $this->panel
            ->answer('vm.status', 404, ['success' => false, 'message' => 'VM not found'])
            ->ok('vm.status', ['metadata' => ['state' => 'ready', 'ip' => '203.0.113.12', 'ips' => '203.0.113.12', 'username' => 'root']])
            ->ok('vm.create', ['vm_id' => 'new-vm']);

        $this->assertSame('success', maburvm_CreateAccount($this->params()));

        $this->assertSame('new-vm', Properties::$store[41]['vm_id']);
        $this->assertSame('203.0.113.12', $this->service()->dedicatedip);
    }

    public function testAPasswordTooShortToSetIsReplacedAndSaved(): void
    {
        $this->readyPanel();

        $this->assertSame('success', maburvm_CreateAccount($this->params(['password' => 'short'])));

        $sent = $this->panel->sent('vm.create')['data']['password'];
        $this->assertGreaterThanOrEqual(8, strlen($sent));
        $this->assertSame('enc:' . $sent, $this->service()->password, 'WHMCS shows the customer the password that was really set');
    }

    public function testAPanelThatDropsOutDuringTheWaitNeitherFailsTheOrderNorLosesThePassword(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid'])
            ->answer('vm.status', 0, [])
            ->ok('vm.status', ['metadata' => ['state' => 'ready', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        $this->assertSame('success', maburvm_CreateAccount($this->params(['password' => 'short'])));

        $this->assertSame('enc:' . $this->panel->sent('vm.create')['data']['password'], $this->service()->password);
        $this->assertSame('203.0.113.10', $this->service()->dedicatedip);
    }

    public function testAPanelThatStaysAwayAfterTheOrderStillLeavesThePasswordOnTheService(): void
    {
        $this->panel
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']])
            ->ok('vm.create', ['vm_id' => 'vm-uuid'])
            ->answer('vm.status', 0, []);

        $this->assertSame('success', maburvm_CreateAccount($this->params(['password' => 'short', 'configoption7' => '10'])), 'the VM was ordered');

        $this->assertSame('vm-uuid', Properties::$store[41]['vm_id']);
        $this->assertSame('enc:' . $this->panel->sent('vm.create')['data']['password'], $this->service()->password);
    }

    public function testCreateNeedsATemplate(): void
    {
        $this->panel->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']]);

        $this->assertStringContainsString('Template ID', maburvm_CreateAccount($this->params(['configoption1' => ''])));
        $this->assertNotContains('vm.create', $this->panel->events());
    }

    public function testSuspendUnsuspendAndTerminateNameTheVmItsOwnerAndItsService(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.suspend')->ok('vm.unsuspend')->ok('vm.destroy');

        $this->assertSame('success', maburvm_SuspendAccount($this->params()));
        $this->assertSame('success', maburvm_UnsuspendAccount($this->params()));
        $this->assertSame('success', maburvm_TerminateAccount($this->params()));

        $this->assertSame(['vm.suspend', 'vm.unsuspend', 'vm.destroy'], $this->panel->events());
        foreach (['vm.suspend' => 'suspend', 'vm.unsuspend' => 'unsuspend', 'vm.destroy' => 'terminate'] as $event => $action) {
            $request = $this->panel->sent($event);
            $this->assertSame(['user_id' => 'user-uuid', 'vm_id' => 'vm-uuid', 'external_ref' => 'whmcs-41'], $request['data']);
            $this->assertSame('whmcs-' . $action . '-41-vm-uuid', $request['idempotency']);
        }
    }

    public function testAServiceProvisionedByTheOldModuleStillWorks(): void
    {
        // The old module kept the VM in a service property and the owner in a
        // custom field staff filled in. Nothing else is on the service.
        Properties::$store[41] = ['vm_id' => 'vm-uuid'];
        $this->panel->ok('vm.suspend');

        $this->assertSame('success', maburvm_SuspendAccount($this->params(['customfields' => ['Panel User ID' => 'legacy-user']])));

        $this->assertSame(['vm.suspend'], $this->panel->events());
        $this->assertSame('legacy-user', $this->panel->sent('vm.suspend')['data']['user_id']);
    }

    public function testAnActionOnAVmNeverMakesAnAccount(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid'];
        $this->panel
            ->answer('vm.suspend', 400, ['success' => false, 'message' => 'user_id is required for vm.suspend: set the panel user ID on this service'])
            ->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']]);

        $result = maburvm_SuspendAccount($this->params());

        $this->assertStringContainsString('set the panel user ID', $result);
        $this->assertSame(['vm.suspend'], $this->panel->events());
    }

    public function testAnActionWithoutAVmSaysSo(): void
    {
        $this->assertStringContainsString('no VM ID', maburvm_SuspendAccount($this->params()));
        $this->assertStringContainsString('no VM ID', maburvm_ChangePackage($this->params()));
        $this->assertSame([], $this->panel->events());
    }

    public function testARefusedActionReturnsThePanelsReason(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->answer('vm.destroy', 403, ['success' => false, 'message' => 'the VM belongs to a different billing service than the one this event names']);

        $this->assertStringContainsString('different billing service', maburvm_TerminateAccount($this->params()));
    }

    public function testChangePackageSendsThePlanOrTheSize(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.resize');

        $this->assertSame('success', maburvm_ChangePackage($this->params(['configoption5' => 'plan-large'])));
        $this->assertSame('success', maburvm_ChangePackage($this->params(['configoption2' => '4', 'configoption3' => '8192', 'configoption4' => '120'])));

        $this->assertSame('plan-large', $this->panel->sent('vm.resize', 0)['data']['plan_id']);
        $this->assertSame(['cpu' => 4, 'memory' => 8192, 'disk' => 120], $this->panel->sent('vm.resize', 1)['data']['vm_specs']);
    }

    public function testChangePasswordAndPowerButtons(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.reset_password')->ok('vm.start')->ok('vm.stop')->ok('vm.restart');

        $this->assertSame('success', maburvm_ChangePassword($this->params(['password' => 'N3wPassword!'])));
        $this->assertSame('success', maburvm_start($this->params()));
        $this->assertSame('success', maburvm_stop($this->params()));
        $this->assertSame('success', maburvm_restart($this->params()));

        $this->assertSame('N3wPassword!', $this->panel->sent('vm.reset_password')['data']['password']);
        $this->assertSame(['Start' => 'start', 'Stop' => 'stop', 'Reboot' => 'restart'], maburvm_AdminCustomButtonArray());
    }

    public function testTestConnectionProvesTheCredentials(): void
    {
        $this->panel->ok('catalog.list', ['data' => ['templates' => [], 'plans' => [], 'regions' => []]]);
        $this->assertSame(['success' => true, 'error' => ''], maburvm_TestConnection($this->params()));

        $wrong = maburvm_TestConnection($this->params(['serveraccesshash' => 'wrong-secret']));
        $this->assertFalse($wrong['success']);
        $this->assertStringContainsString('signature', $wrong['error']);

        $missing = maburvm_TestConnection($this->params(['serverpassword' => '']));
        $this->assertFalse($missing['success']);
    }

    public function testTestConnectionAcceptsAPanelFromBeforeTheCatalog(): void
    {
        // No scripted answer: the fake panel says "Invalid event type", as an older panel does.
        $this->assertTrue(maburvm_TestConnection($this->params())['success']);
    }

    public function testTheProductSettingsOfferThePanelsCatalog(): void
    {
        $this->panel->ok('catalog.list', ['data' => [
            'templates' => [
                ['id' => 't1', 'name' => 'Debian', 'version' => '12', 'licensed' => false],
                ['id' => 't2', 'name' => 'Windows Server', 'version' => '2025', 'licensed' => true],
            ],
            'plans' => [['id' => 'p1', 'name' => 'Small', 'cpu' => 2, 'memory' => 2048, 'disk' => 40]],
            'regions' => [['slug' => 'jakarta', 'name' => 'Jakarta']],
        ]]);

        $this->assertSame(['t1' => 'Debian 12', 't2' => 'Windows Server 2025 (licensed)'], maburvm_loadTemplates($this->params()));
        $this->assertSame('Small (2 vCPU, 2048 MB, 40 GB)', maburvm_loadPlans($this->params())['p1']);
        $this->assertSame('Jakarta', maburvm_loadRegions($this->params())['jakarta']);
        $this->assertSame([], $this->panel->sent('catalog.list')['data'], 'an event with no data is sent as an object, not a list');
    }

    public function testALoaderThatCannotAskThePanelThrowsSoWhmcsFallsBackToText(): void
    {
        $this->panel->answer('catalog.list', 503, ['success' => false, 'message' => 'billing webhook is not configured']);

        $this->expectExceptionMessage('billing webhook is not configured');
        maburvm_loadPlans($this->params());
    }

    public function testTheProductSettingsKeepTheirPositions(): void
    {
        // WHMCS stores these by position: products saved by the old module
        // must read the same values in the same slots.
        $this->assertSame(
            ['Template ID', 'vCPU', 'Memory (MB)', 'Disk (GB)', 'Plan ID', 'Region', 'Provisioning wait (seconds)'],
            array_keys(maburvm_ConfigOptions())
        );
    }

    public function testUsageSyncWritesTrafficAndAMissingAddress(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid'];
        $this->panel
            ->ok('vm.usage', ['metadata' => ['bandwidth_used_mb' => '1500', 'bandwidth_limit_mb' => '512000', 'disk_limit_mb' => '61440']])
            ->ok('vm.status', ['metadata' => ['state' => 'ready', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        maburvm_UsageUpdate($this->params());

        $service = $this->service();
        $this->assertSame(1500, $service->bwusage);
        $this->assertSame(512000, $service->bwlimit);
        $this->assertSame(61440, $service->disklimit);
        $this->assertSame('203.0.113.10', $service->dedicatedip);
        $this->assertArrayNotHasKey('user_id', $this->panel->sent('vm.status')['data']);

        maburvm_UsageUpdate($this->params());
        $this->assertCount(1, array_keys($this->panel->events(), 'vm.status'), 'a service that has its address is not asked about again');
    }

    public function testClientAreaShowsTheLiveStateAndSurvivesAPanelThatIsDown(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.status', ['metadata' => ['state' => 'ready', 'ip' => '203.0.113.10', 'ips' => '203.0.113.10', 'username' => 'root']]);

        $vars = maburvm_ClientArea($this->params())['vars'];
        $this->assertSame('vm-uuid', $vars['vmId']);
        $this->assertSame('Ready', $vars['vmStateLabel']);
        $this->assertSame('203.0.113.10', $vars['vmIp']);
        $this->assertSame('https://panel.example.test', $vars['panelUrl']);
        $this->assertSame(5, $this->panel->sent('vm.status')['timeout'], 'a customer\'s page view never waits long for the panel');

        $GLOBALS['maburvm_http'] = function (): array {
            throw new \Exception('Failed to reach MaburVM panel: connection refused');
        };
        $vars = maburvm_ClientArea($this->params())['vars'];
        $this->assertSame('vm-uuid', $vars['vmId']);
        $this->assertSame('', $vars['vmStateLabel']);
    }

    public function testAPanelThatCannotBeReachedIsAnErrorNotACrash(): void
    {
        $GLOBALS['maburvm_http'] = function (): array {
            throw new \Exception('Failed to reach MaburVM panel: connection refused');
        };

        $this->assertStringContainsString('connection refused', maburvm_CreateAccount($this->params()));
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->assertStringContainsString('connection refused', maburvm_TerminateAccount($this->params()));
    }
}
