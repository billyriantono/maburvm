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

    public function testSingleSignOnAsksForALinkForTheServicesAccountAndVm(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('user.sso', ['metadata' => [
            'path' => '/api/v1/auth/billing-sso?token=t0k',
            'url' => 'https://cp.example.test/api/v1/auth/billing-sso?token=t0k',
            'expires_in' => '60',
        ]]);

        $result = maburvm_ServiceSingleSignOn($this->params());

        $this->assertSame(['success' => true, 'redirectTo' => 'https://cp.example.test/api/v1/auth/billing-sso?token=t0k'], $result);
        $request = $this->panel->sent('user.sso');
        $this->assertSame(['user_id' => 'user-uuid', 'external_ref' => 'whmcs-41', 'vm_id' => 'vm-uuid'], $request['data']);
        $this->assertSame(10, $request['timeout'], 'the customer is waiting on the click');
        $this->assertStringStartsWith('whmcs-sso-', $request['idempotency']);
    }

    public function testSingleSignOnUsesTheServerAddressWhenThePanelKnowsNoPublicOne(): void
    {
        Properties::$store[41] = ['panel_user_id' => 'user-uuid'];
        $this->panel->ok('user.sso', ['metadata' => ['path' => '/api/v1/auth/billing-sso?token=t0k', 'expires_in' => '60']]);

        $result = maburvm_ServiceSingleSignOn($this->params());

        $this->assertSame('https://panel.example.test/api/v1/auth/billing-sso?token=t0k', $result['redirectTo']);
        $this->assertArrayNotHasKey('vm_id', $this->panel->sent('user.sso')['data'], 'no VM yet: the link opens the dashboard');
    }

    public function testSingleSignOnNeverMakesAnAccount(): void
    {
        $this->panel->ok('user.ensure', ['metadata' => ['user_id' => 'user-uuid']]);

        $result = maburvm_ServiceSingleSignOn($this->params());

        $this->assertFalse($result['success']);
        $this->assertStringContainsString('no panel account', $result['errorMsg']);
        $this->assertSame([], $this->panel->events());
    }

    public function testSingleSignOnPassesOnThePanelsRefusal(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->answer('user.sso', 403, ['success' => false, 'message' => 'single sign-on refused: the panel account is suspended']);

        $result = maburvm_ServiceSingleSignOn($this->params());

        $this->assertFalse($result['success']);
        $this->assertStringContainsString('the panel account is suspended', $result['errorMsg']);
    }

    public function testSingleSignOnOnAnOlderPanelSaysToUpdateIt(): void
    {
        // No scripted answer: the fake panel says "Invalid event type", as an older panel does.
        Properties::$store[41] = ['panel_user_id' => 'user-uuid'];

        $result = maburvm_ServiceSingleSignOn($this->params());

        $this->assertFalse($result['success']);
        $this->assertStringContainsString('update the panel', $result['errorMsg']);
    }

    public function testSingleSignOnThatCannotReachThePanelIsAnErrorNotACrash(): void
    {
        Properties::$store[41] = ['panel_user_id' => 'user-uuid'];
        $GLOBALS['maburvm_http'] = function (): array {
            throw new \Exception('Failed to reach MaburVM panel: connection refused');
        };

        $result = maburvm_ServiceSingleSignOn($this->params());

        $this->assertFalse($result['success']);
        $this->assertStringContainsString('connection refused', $result['errorMsg']);
    }

    public function testTheClientAreaOpensThePanelThroughSingleSignOn(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.status', ['metadata' => ['state' => 'ready']]);

        $this->assertSame(41, maburvm_ClientArea($this->params())['vars']['serviceId']);
        $template = file_get_contents(__DIR__ . '/../../modules/servers/maburvm/templates/clientarea.tpl');
        $this->assertStringContainsString('action=productdetails&amp;id={$serviceId|escape:\'url\'}&amp;dosinglesignon=1', $template);
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

    public function testUsageSyncWritesDiskAndTheOtherFiguresToTheService(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid'];
        Capsule::table('tblhosting')->where('id', 41)->update(['dedicatedip' => '203.0.113.10']);
        $this->panel->ok('vm.usage', ['metadata' => ['bandwidth_used_mb' => '10', 'bandwidth_limit_mb' => '20', 'disk_limit_mb' => '61440',
            'disk_allocated_mb' => '81920', 'snapshot_count' => '2', 'backup_count' => '3', 'backup_size_mb' => '4096',
            'extra_ips' => '1', 'floating_ips' => '0', 'uptime_hours' => '71', 'uptime_since' => '2026-11-01T00:00:00Z']]);

        maburvm_UsageUpdate($this->params());

        $this->assertSame(81920, $this->service()->diskusage, 'the disk the VM holds, data disks included');
        $this->assertSame(61440, $this->service()->disklimit);
        $props = new Properties(41);
        $this->assertSame('2', $props->get('Snapshots'));
        $this->assertSame('3', $props->get('Backups'));
        $this->assertSame('4096', $props->get('Backup size (MB)'));
        $this->assertSame('1', $props->get('Extra IPs'));
        $this->assertSame('0', $props->get('Floating IPs'));
        $this->assertSame('71', $props->get('Uptime hours'));
    }

    public function testUsageSyncFromAnOlderPanelLeavesDiskUsageAlone(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid'];
        Capsule::table('tblhosting')->where('id', 41)->update(['dedicatedip' => '203.0.113.10', 'diskusage' => 5]);
        $this->panel->ok('vm.usage', ['metadata' => ['bandwidth_used_mb' => '10', 'bandwidth_limit_mb' => '20', 'disk_limit_mb' => '61440']]);

        maburvm_UsageUpdate($this->params());

        $this->assertSame(5, $this->service()->diskusage);
        $this->assertNull((new Properties(41))->get('Snapshots'));
    }

    public function testTheCustomerGetsPowerButtonsThatActOnTheirOwnServiceOnly(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('vm.start')->ok('vm.stop')->ok('vm.restart');

        $this->assertSame(['Start' => 'clientStart', 'Shut down' => 'clientStop', 'Restart' => 'clientRestart'], maburvm_ClientAreaCustomButtonArray());
        foreach (['clientStart', 'clientStop', 'clientRestart', 'clientReinstall', 'clientResetPassword', 'clientConsole'] as $fn) {
            $this->assertContains($fn, maburvm_ClientAreaAllowedFunctions());
            $this->assertTrue(function_exists('maburvm_' . $fn), $fn);
        }
        $this->assertSame('success', maburvm_clientStart($this->params()));
        $this->assertSame('success', maburvm_clientStop($this->params()));
        $this->assertSame('success', maburvm_clientRestart($this->params()));
        $this->assertSame(['vm.start', 'vm.stop', 'vm.restart'], $this->panel->events());
        $this->assertSame(['user_id' => 'user-uuid', 'vm_id' => 'vm-uuid', 'external_ref' => 'whmcs-41'], $this->panel->sent('vm.start')['data'],
            'the panel checks the VM belongs to this account and this service');

        $this->assertStringContainsString('suspended', maburvm_clientStart($this->params(['status' => 'Suspended'])));
        $this->assertCount(3, $this->panel->requests, 'a suspended service sends nothing');
    }

    public function testAPanelRefusalIsReadFromTheErrorEnvelope(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->answer('vm.start', 409, ['success' => false, 'message' => 'The VM is suspended',
            'error' => ['code' => 'conflict', 'message' => 'The VM is suspended']]);
        $this->assertSame('MaburVM vm.start failed: The VM is suspended', maburvm_clientStart($this->params()));
        $this->assertSame('Only in the envelope', maburvm_errorMessage(['error' => ['code' => 'x', 'message' => 'Only in the envelope']], 400));
        $this->assertSame('Before the envelope', maburvm_errorMessage(['message' => 'Before the envelope'], 400));
        $this->assertSame('HTTP 502', maburvm_errorMessage(['raw' => '<html>'], 502));
    }

    public function testReinstallNeedsAConfirmationAndATemplateAndSavesTheNewPassword(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $template = '6f1c2a54-0d4e-4f8e-9a63-2d7b1c9e0a11';
        $this->panel->ok('vm.rebuild', ['metadata' => ['job_id' => '9', 'password' => 'N3wRootPass']]);

        $GLOBALS['maburvm_request'] = ['template_id' => $template];
        $this->assertStringContainsString('confirm', maburvm_clientReinstall($this->params()));
        $GLOBALS['maburvm_request'] = ['confirm' => '1', 'template_id' => 'not-a-template'];
        $this->assertStringContainsString('Choose an operating system', maburvm_clientReinstall($this->params()));
        $this->assertSame([], $this->panel->events(), 'nothing is sent until both are right');

        $GLOBALS['maburvm_request'] = ['confirm' => '1', 'template_id' => $template];
        $this->assertSame('success', maburvm_clientReinstall($this->params()));
        $this->assertSame(['user_id' => 'user-uuid', 'vm_id' => 'vm-uuid', 'external_ref' => 'whmcs-41', 'template_id' => $template,
            'regenerate_password' => true], $this->panel->sent('vm.rebuild')['data']);
        $this->assertSame('enc:N3wRootPass', $this->service()->password, 'the customer reads the new password on the service');
        unset($GLOBALS['maburvm_request']);
    }

    public function testANewRootPasswordIsSavedOnlyWhenThePanelSetIt(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->answer('vm.reset_password', 409, ['success' => false, 'message' => 'The VM is not running']);
        $this->assertStringContainsString('not running', maburvm_clientResetPassword($this->params()));
        $this->assertSame('enc:Sup3rSecret!', $this->service()->password);

        $this->panel->answers['vm.reset_password'] = [[200, ['success' => true]]];
        $this->assertSame('success', maburvm_clientResetPassword($this->params()));
        $sent = $this->panel->sent('vm.reset_password', 1)['data']['password'];
        $this->assertGreaterThanOrEqual(16, strlen($sent));
        $this->assertSame('enc:' . $sent, $this->service()->password);
    }

    public function testTheConsoleOpensThroughAOneTimeLinkToTheConsolePage(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel->ok('user.sso', ['metadata' => ['path' => '/api/v1/auth/billing-sso?token=t', 'expires_in' => '60']]);
        $went = null;
        $GLOBALS['maburvm_redirect'] = function (string $url) use (&$went) {
            $went = $url;
            return 'redirected';
        };

        $this->assertSame('redirected', maburvm_clientConsole($this->params()));
        $this->assertSame('https://panel.example.test/api/v1/auth/billing-sso?token=t', $went);
        $this->assertSame(['user_id' => 'user-uuid', 'vm_id' => 'vm-uuid', 'page' => 'console', 'external_ref' => 'whmcs-41'],
            $this->panel->sent('user.sso')['data']);
        unset($GLOBALS['maburvm_redirect']);
    }

    public function testTheClientAreaDrawsGraphsAndOffersTheTemplatesOfThePlan(): void
    {
        Properties::$store[41] = ['vm_id' => 'vm-uuid', 'panel_user_id' => 'user-uuid'];
        $this->panel
            ->ok('vm.status', ['metadata' => ['state' => 'ready', 'status' => 'stopped', 'ip' => '203.0.113.10']])
            ->ok('vm.metrics', ['data' => ['points' => [
                ['cpu_percent' => 10, 'memory_percent' => 40, 'network_in_bps' => 0, 'network_out_bps' => 2048],
                ['cpu_percent' => 50, 'memory_percent' => 45, 'network_in_bps' => 2000000, 'network_out_bps' => 1024],
            ]]])
            ->ok('catalog.list', ['data' => ['templates' => [
                ['id' => 't-any', 'name' => 'Debian', 'version' => '12', 'plan_ids' => []],
                ['id' => 't-plan', 'name' => 'Windows', 'version' => '2022', 'plan_ids' => ['plan-big']],
                ['id' => 't-other', 'name' => 'Windows', 'version' => '2025', 'plan_ids' => ['plan-other']],
            ]]]);

        $vars = maburvm_ClientArea($this->params(['configoption5' => 'plan-big']))['vars'];

        $this->assertSame('stopped', $vars['vmPower']);
        $this->assertSame(['CPU', 'Memory', 'Network in', 'Network out'], array_column($vars['graphs'], 'label'));
        $this->assertSame('0,53.2 300,30', $vars['graphs'][0]['points']);
        $this->assertSame('50%', $vars['graphs'][0]['now']);
        $this->assertSame('1.9 MB/s', $vars['graphs'][2]['now']);
        $this->assertSame([['id' => 't-any', 'name' => 'Debian 12'], ['id' => 't-plan', 'name' => 'Windows 2022']], $vars['templates']);
        $this->assertSame(['vm_id' => 'vm-uuid', 'user_id' => 'user-uuid', 'external_ref' => 'whmcs-41', 'hours' => 24],
            $this->panel->sent('vm.metrics')['data']);
        $template = file_get_contents(__DIR__ . '/../../modules/servers/maburvm/templates/clientarea.tpl');
        foreach (['clientStart', 'clientStop', 'clientRestart', 'clientConsole', 'clientResetPassword', 'clientReinstall', 'name="token"'] as $needle) {
            $this->assertStringContainsString($needle, $template);
        }
    }
}
