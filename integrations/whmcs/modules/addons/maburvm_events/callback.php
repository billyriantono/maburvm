<?php

/**
 * Public endpoint the MaburVM panel posts its events to:
 *   https://<whmcs>/modules/addons/maburvm_events/callback.php
 * Nothing here trusts the caller: an event is applied only when its signature
 * matches (see lib.php).
 */

require_once __DIR__ . '/../../../init.php';
require_once __DIR__ . '/lib.php';

header('Content-Type: application/json');
if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST') {
    http_response_code(405);
    echo json_encode(['success' => false, 'message' => 'POST only']);
    exit;
}
$raw = (string) file_get_contents('php://input', false, null, 0, 65537);
if (strlen($raw) > 65536) {
    http_response_code(413);
    echo json_encode(['success' => false, 'message' => 'Event too large']);
    exit;
}
list($status, $body) = maburvm_events_handle($raw, $_SERVER['HTTP_X_WEBHOOK_SIGNATURE'] ?? '');
http_response_code($status);
echo json_encode($body);
