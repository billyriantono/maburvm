<?php

namespace MaburvmTests;

use PHPUnit\Framework\TestCase;
use WHMCS\Database\Capsule;

/** The events addon: what the panel sends, checked and applied. */
class EventsTest extends TestCase
{
    private const SECRET = 'test-signing-secret';

    protected function setUp(): void
    {
        Recorder::reset();
        Capsule::table('tblservers')->insert(['id' => 3, 'type' => 'maburvm', 'accesshash' => self::SECRET]);
        Capsule::table('tblservers')->insert(['id' => 4, 'type' => 'cpanel', 'accesshash' => 'someone-elses']);
        Capsule::table('tblclients')->insert(['id' => 7]);
        Capsule::table('tblproducts')->insert(['id' => 1, 'servertype' => 'maburvm']);
        Capsule::table('tblproducts')->insert(['id' => 2, 'servertype' => 'cpanel']);
        Capsule::table('tblhosting')->insert(['id' => 41, 'userid' => 7, 'packageid' => 1, 'dedicatedip' => '', 'assignedips' => '', 'username' => '']);
        Capsule::table('tblhosting')->insert(['id' => 42, 'userid' => 8, 'packageid' => 2, 'dedicatedip' => '', 'assignedips' => '', 'username' => '']);
        Capsule::table('tbladdonmodules')->insert(['module' => 'maburvm_events', 'setting' => 'floating_ip_price', 'value' => '2.50']);
        Recorder::$apiResults['AddBillableItem'] = function (array $values): array {
            Capsule::table('tblbillableitems')->insert(['id' => 900, 'userid' => $values['clientid'],
                'description' => $values['description'], 'amount' => $values['amount']]);
            return ['result' => 'success', 'billableid' => 900];
        };
    }

    /** Sends an event signed the way the panel signs it. */
    private function send(array $event, ?string $signature = null): array
    {
        $raw = json_encode($event, JSON_UNESCAPED_SLASHES);
        return maburvm_events_handle($raw, $signature ?? hash_hmac('sha256', $raw, self::SECRET));
    }

    private function usage(string $id, int $billable, string $sentAt, array $over = []): array
    {
        return $over + ['id' => $id, 'event' => 'floating_ip.usage', 'sent_at' => $sentAt, 'reason' => 'ordered',
            'user_id' => 'user-uuid', 'user_ref' => 'whmcs-client-7', 'total' => $billable + 1, 'free' => 1, 'billable' => $billable];
    }

    public function testTheSignatureIsTheOneThePanelComputes(): void
    {
        // The same vector is asserted in the panel's tests (TestBillingEventSignatureVector).
        $this->assertSame(
            'eaca18e228437956e41580c76c5485a88bb20d9c4e0fdf2da23d55af5ab17799',
            hash_hmac('sha256', '{"event":"floating_ip.usage","id":"e1"}', self::SECRET)
        );
        $this->assertTrue(maburvm_events_verify('{"event":"floating_ip.usage","id":"e1"}', 'eaca18e228437956e41580c76c5485a88bb20d9c4e0fdf2da23d55af5ab17799'));
        $this->assertTrue(maburvm_events_verify('{"event":"floating_ip.usage","id":"e1"}', 'sha256=EACA18E228437956E41580C76C5485A88BB20D9C4E0FDF2DA23D55AF5AB17799'));
    }

    public function testAnEventThatIsNotSignedByThePanelChangesNothing(): void
    {
        foreach (['', 'deadbeef', hash_hmac('sha256', '{}', 'someone-elses')] as $signature) {
            [$status] = $this->send($this->usage('e1', 2, '2026-01-01T00:00:00Z'), $signature);
            $this->assertSame(401, $status);
        }
        [$status] = maburvm_events_handle('{"event":"floating_ip.usage","billable":9}', hash_hmac('sha256', '{"event":"floating_ip.usage","billable":2}', self::SECRET));
        $this->assertSame(401, $status, 'a body changed after signing');
        $this->assertSame([], Recorder::$apiCalls);
        $this->assertSame([], Capsule::$tables['tblbillableitems'] ?? []);
    }

    public function testASecretSetOnTheAddonReplacesTheServers(): void
    {
        Capsule::table('tbladdonmodules')->insert(['module' => 'maburvm_events', 'setting' => 'webhook_secret', 'value' => 'panel-own-secret']);
        $raw = json_encode($this->usage('e1', 1, '2026-01-01T00:00:00Z'));

        $this->assertSame(401, maburvm_events_handle($raw, hash_hmac('sha256', $raw, self::SECRET))[0]);
        $this->assertSame(200, maburvm_events_handle($raw, hash_hmac('sha256', $raw, 'panel-own-secret'))[0]);
    }

    public function testFloatingIpsBecomeOneRecurringChargeThatFollowsTheCount(): void
    {
        [$status, $body] = $this->send($this->usage('e1', 2, '2026-01-01T00:00:00Z'));
        $this->assertSame(200, $status, $body['message']);
        $this->assertCount(1, Recorder::$apiCalls);
        [$command, $values] = Recorder::$apiCalls[0];
        $this->assertSame('AddBillableItem', $command);
        $this->assertSame(7, $values['clientid']);
        $this->assertSame('Floating IP addresses x 2', $values['description']);
        $this->assertSame(5.0, $values['amount']);
        $this->assertSame('recur', $values['invoiceaction']);
        $this->assertSame('Months', $values['recurcycle']);

        // One more address: the same item, a new amount. No second item.
        $this->assertSame(200, $this->send($this->usage('e2', 3, '2026-01-02T00:00:00Z'))[0]);
        $this->assertCount(1, Recorder::$apiCalls);
        $item = Capsule::table('tblbillableitems')->where('id', 900)->first();
        $this->assertSame('Floating IP addresses x 3', $item->description);
        $this->assertSame(7.5, $item->amount);

        // All released: the charge goes.
        $this->assertSame(200, $this->send($this->usage('e3', 0, '2026-01-03T00:00:00Z'))[0]);
        $this->assertFalse(Capsule::table('tblbillableitems')->where('id', 900)->exists());
        $this->assertSame(0, Capsule::table('mod_maburvm_floating')->where('client_id', 7)->first()->quantity);
    }

    public function testAnEventIsAppliedOnceAndAnOlderCountNeverOverwritesANewerOne(): void
    {
        $this->send($this->usage('e2', 3, '2026-01-02T00:00:00Z'));

        // The panel retries a delivery it thinks failed.
        [$status, $body] = $this->send($this->usage('e2', 3, '2026-01-02T00:00:00Z'));
        $this->assertSame(200, $status);
        $this->assertTrue($body['duplicate']);
        $this->assertCount(1, Recorder::$apiCalls);

        // An earlier event arrives late.
        [$status, $body] = $this->send($this->usage('e1', 1, '2026-01-01T00:00:00Z'));
        $this->assertSame(200, $status);
        $this->assertStringContainsString('newer count', $body['message']);
        $this->assertSame(7.5, Capsule::table('tblbillableitems')->where('id', 900)->first()->amount);
    }

    public function testWithNoPriceTheCountIsRecordedAndNothingIsCharged(): void
    {
        Capsule::table('tbladdonmodules')->where('setting', 'floating_ip_price')->update(['value' => '0']);

        [$status, $body] = $this->send($this->usage('e1', 2, '2026-01-01T00:00:00Z'));

        $this->assertSame(200, $status);
        $this->assertStringContainsString('no price is set', $body['message']);
        $this->assertSame([], Recorder::$apiCalls);
        $this->assertSame(2, Capsule::table('mod_maburvm_floating')->where('client_id', 7)->first()->quantity);
    }

    public function testSettingThePriceBackToZeroRemovesTheChargeAtTheNextCount(): void
    {
        $this->send($this->usage('e1', 2, '2026-01-01T00:00:00Z'));
        $this->assertTrue(Capsule::table('tblbillableitems')->where('id', 900)->exists());
        Capsule::table('tbladdonmodules')->where('setting', 'floating_ip_price')->update(['value' => '0']);

        [$status, $body] = $this->send($this->usage('e2', 2, '2026-01-02T00:00:00Z'));

        $this->assertSame(200, $status);
        $this->assertStringContainsString('nothing is charged', $body['message']);
        $this->assertFalse(Capsule::table('tblbillableitems')->where('id', 900)->exists(), 'an item left behind would go on charging');
    }

    public function testAClientIsFoundThroughAServiceWhenThePanelHasNoReferenceForThem(): void
    {
        Capsule::table('tblcustomfields')->insert(['id' => 5, 'type' => 'product', 'relid' => 1, 'fieldname' => 'Panel User ID']);
        Capsule::table('tblcustomfieldsvalues')->insert(['fieldid' => 5, 'relid' => 41, 'value' => 'legacy-user']);

        [$status] = $this->send($this->usage('e1', 1, '2026-01-01T00:00:00Z', ['user_id' => 'legacy-user', 'user_ref' => null]));

        $this->assertSame(200, $status);
        $this->assertSame(7, Recorder::$apiCalls[0][1]['clientid']);
    }

    public function testAPanelAccountWithNoClientIsAcknowledgedAndNotCharged(): void
    {
        [$status, $body] = $this->send($this->usage('e1', 2, '2026-01-01T00:00:00Z', ['user_id' => 'staff-made', 'user_ref' => 'whmcs-client-999']));

        $this->assertSame(200, $status, 'a retry would find the same: the panel must not keep trying');
        $this->assertStringContainsString('no client matches', $body['message']);
        $this->assertSame([], Recorder::$apiCalls);
        $this->assertStringContainsString('nothing was charged', Recorder::$activity[0][0]);
    }

    public function testAChargeThatCannotBeMadeAsksThePanelToTryAgain(): void
    {
        Recorder::$apiResults['AddBillableItem'] = ['result' => 'error', 'message' => 'Client ID Not Found'];

        [$status, $body] = $this->send($this->usage('e1', 2, '2026-01-01T00:00:00Z'));
        $this->assertSame(500, $status);
        $this->assertStringContainsString('Client ID Not Found', $body['message']);
        $this->assertFalse(Capsule::table('mod_maburvm_events')->where('id', 'e1')->exists(), 'not remembered, so the retry is applied');

        Recorder::$apiResults['AddBillableItem'] = ['result' => 'success', 'billableid' => 901];
        $this->assertSame(200, $this->send($this->usage('e1', 2, '2026-01-01T00:00:00Z'))[0]);
    }

    public function testANewVmsAddressIsWrittenIntoItsService(): void
    {
        [$status] = $this->send(['id' => 'v1', 'event' => 'vm.created', 'sent_at' => '2026-01-01T00:00:00Z', 'vm_id' => 'vm-uuid',
            'external_ref' => 'whmcs-41', 'ip' => '203.0.113.10', 'ips' => ['203.0.113.10', '203.0.113.11'], 'username' => 'Administrator']);

        $this->assertSame(200, $status);
        $service = Capsule::table('tblhosting')->where('id', 41)->first();
        $this->assertSame('203.0.113.10', $service->dedicatedip);
        $this->assertSame('203.0.113.11', $service->assignedips);
        $this->assertSame('Administrator', $service->username);
    }

    public function testAnEventCannotWriteIntoAServiceOfAnotherModule(): void
    {
        [$status, $body] = $this->send(['id' => 'v1', 'event' => 'vm.created', 'external_ref' => 'whmcs-42', 'ip' => '203.0.113.10', 'username' => 'root']);

        $this->assertSame(200, $status);
        $this->assertStringContainsString('ignored', $body['message']);
        $this->assertSame('', Capsule::table('tblhosting')->where('id', 42)->first()->dedicatedip);
    }

    public function testAFailedInstallationAndAnOverageAreOnTheClientsRecord(): void
    {
        $this->send(['id' => 'f1', 'event' => 'vm.failed', 'vm_id' => 'vm-uuid', 'external_ref' => 'whmcs-41', 'reason' => 'the node ran out of disk']);
        $this->send(['id' => 'o1', 'event' => 'bandwidth.overage', 'vm_id' => 'vm-uuid', 'external_ref' => 'whmcs-41', 'used_gb' => 512.4, 'quota_gb' => 500]);

        $this->assertSame(['MaburVM: the VM of service #41 could not be installed: the node ran out of disk', 7], Recorder::$activity[0]);
        $this->assertSame(['MaburVM: service #41 is over its transfer quota (512.4 GB used of 500 GB)', 7], Recorder::$activity[1]);

        $this->send(['id' => 'f0', 'event' => 'vm.failed', 'vm_id' => 'vm-uuid', 'external_ref' => 'whmcs-41', 'reason' => 'the node ran out of disk', 'retrying' => true]);
        $this->assertStringEndsWith('the node ran out of disk (the panel is still trying)', Recorder::$activity[2][0]);
    }

    public function testTheAddonsPageShowsWhereToSendEventsAndWhatArrived(): void
    {
        Capsule::table('tblconfiguration')->insert(['setting' => 'SystemURL', 'value' => 'https://billing.example.test/']);
        $this->send(['id' => 'f1', 'event' => 'vm.failed', 'vm_id' => '<b>vm</b>', 'reason' => 'x']);

        ob_start();
        maburvm_events_output([]);
        $html = ob_get_clean();

        $this->assertStringContainsString('https://billing.example.test/modules/addons/maburvm_events/callback.php', $html);
        $this->assertStringContainsString('vm.failed', $html);
        $this->assertStringContainsString('&lt;b&gt;vm&lt;/b&gt;', $html, 'what an event carried is shown as text, never as markup');
        $this->assertStringNotContainsString('No signing secret', $html);

        Capsule::table('tblservers')->where('type', 'maburvm')->delete();
        ob_start();
        maburvm_events_output([]);
        $this->assertStringContainsString('No signing secret', ob_get_clean());

        $this->assertSame('success', maburvm_events_activate()['status']);
        $this->assertSame(['floating_ip_price', 'floating_ip_description', 'webhook_secret'], array_keys(maburvm_events_config()['fields']));
    }

    public function testOtherEventsAreAcknowledgedAndAnOldPanelsEventsStillCountOnce(): void
    {
        foreach (['vm.suspended', 'vm.unsuspended', 'vm.destroyed', 'something.new'] as $i => $event) {
            $this->assertSame(200, $this->send(['id' => 'x' . $i, 'event' => $event, 'external_ref' => 'whmcs-41'])[0]);
        }
        $this->assertCount(4, Capsule::$tables['mod_maburvm_events']);

        // A panel from before delivery ids: the same body twice is one event.
        $old = ['event' => 'floating_ip.usage', 'user_id' => 'user-uuid', 'user_ref' => 'whmcs-client-7', 'billable' => 1, 'sent_at' => '2026-01-01T00:00:00Z'];
        $this->assertArrayNotHasKey('duplicate', $this->send($old)[1]);
        $this->assertTrue($this->send($old)[1]['duplicate']);

        $this->assertSame(400, maburvm_events_handle('[1,2]', hash_hmac('sha256', '[1,2]', self::SECRET))[0]);
    }
}
