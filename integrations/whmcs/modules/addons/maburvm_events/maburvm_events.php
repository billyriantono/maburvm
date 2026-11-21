<?php

/**
 * MaburVM events: an addon that receives the events a MaburVM panel sends
 * (floating IP usage, VM created or failed, transfer overage) and applies them
 * to WHMCS. See lib.php for what each event does.
 *
 * Copy this folder to  <whmcs>/modules/addons/maburvm_events/  and activate it
 * under Addon Modules. Then set the panel's webhook URL (System settings, API)
 * to the address this addon's page shows.
 */

if (!defined('WHMCS')) {
    die('This file cannot be accessed directly');
}

use WHMCS\Database\Capsule;

require_once __DIR__ . '/lib.php';

function maburvm_events_config()
{
    return [
        'name' => 'MaburVM events',
        'description' => 'Receives events from a MaburVM panel: bills floating IP addresses and fills in a new VM\'s address.',
        'version' => '1.0',
        'author' => 'MaburVM',
        'fields' => [
            'floating_ip_price' => [
                'FriendlyName' => 'Floating IP price per month',
                'Type' => 'text',
                'Size' => '10',
                'Default' => '0',
                'Description' => 'Charged per billable address, in the client\'s currency as it stands. 0 records the count and charges nothing',
            ],
            'floating_ip_description' => [
                'FriendlyName' => 'Floating IP invoice line',
                'Type' => 'text',
                'Size' => '40',
                'Default' => 'Floating IP addresses',
                'Description' => 'The count is added, for example "Floating IP addresses x 2"',
            ],
            'webhook_secret' => [
                'FriendlyName' => 'Signing secret',
                'Type' => 'text',
                'Size' => '60',
                'Description' => 'Only when the panel signs its events with a secret of its own (System settings, API). Empty uses the Access Hash of the MaburVM servers',
            ],
        ],
    ];
}

function maburvm_events_activate()
{
    try {
        maburvm_events_ensureTables();
        return ['status' => 'success', 'description' => 'Activated. Open the addon\'s page for the address to give the panel.'];
    } catch (\Throwable $e) {
        return ['status' => 'error', 'description' => 'Could not create the addon\'s tables: ' . $e->getMessage()];
    }
}

/** The tables stay: they are what stops an event being applied twice. */
function maburvm_events_deactivate()
{
    return ['status' => 'success', 'description' => 'Deactivated. Received events and floating IP charges are kept.'];
}

/** Admin page: where the panel should send events, and what has arrived. */
function maburvm_events_output($vars)
{
    maburvm_events_ensureTables();
    $esc = function ($s) {
        return htmlspecialchars((string) $s, ENT_QUOTES, 'UTF-8');
    };
    $base = rtrim((string) Capsule::table('tblconfiguration')->where('setting', 'SystemURL')->value('value'), '/');
    echo '<p>Set the panel\'s webhook URL (System settings, API) to:</p>';
    echo '<pre>' . $esc($base . '/modules/addons/maburvm_events/callback.php') . '</pre>';
    if (!maburvm_events_secrets()) {
        echo '<div class="errorbox">No signing secret is known: add a MaburVM server with its Access Hash, or set a signing secret in this addon\'s settings. Until then every event is refused.</div>';
    }
    $rows = Capsule::table('mod_maburvm_events')->orderBy('received_at', 'desc')->limit(50)->get();
    echo '<h2>Last events received</h2>';
    if (!count($rows)) {
        echo '<p>None yet.</p>';
        return;
    }
    echo '<table class="datatable" width="100%"><tr><th>Received</th><th>Event</th><th>What was done</th></tr>';
    foreach ($rows as $row) {
        echo '<tr><td>' . $esc($row->received_at) . '</td><td>' . $esc($row->event) . '</td><td>' . $esc($row->summary) . '</td></tr>';
    }
    echo '</table>';
}
