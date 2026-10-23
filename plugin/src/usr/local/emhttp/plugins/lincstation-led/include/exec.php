<?php
/* Backend for the LincStation LED page. POST only (Unraid checks the csrf_token), JSON out,
 * every value validated before it reaches the binary. No network service is involved. */
$plugin = 'lincstation-led';
$bin    = "/usr/local/sbin/$plugin";
$state  = "/boot/config/plugins/$plugin/state";

header('Content-Type: application/json');
header('Cache-Control: no-store');

function out($d, $code = 200) { http_response_code($code); echo json_encode($d); exit; }
function fail($m, $code = 400) { out(['ok' => false, 'error' => $m], $code); }

if ($_SERVER['REQUEST_METHOD'] !== 'POST') fail('POST required', 405);
if (!is_executable($bin)) fail('binary missing, reinstall the plugin', 500);

$base = escapeshellarg($bin);
$sd   = '-state-dir ' . escapeshellarg($state);

switch ($_POST['action'] ?? '') {
    case 'get':
        $json = trim((string)shell_exec("$base get $sd 2>&1"));
        $st = json_decode($json, true);
        if (!is_array($st)) fail($json ?: 'cannot read state', 500);
        out(['ok' => true, 'state' => $st]);

    case 'set':
        $mode = $_POST['mode'] ?? '';
        if (!in_array($mode, ['off', 'normal', 'breath', 'loop'], true)) fail('bad effect');
        $args = '-mode ' . escapeshellarg($mode);
        $g = $_POST['gamma'] ?? '2.2';
        if (!in_array($g, ['1', '2.2'], true)) fail('bad colour correction');
        $args .= ' -gamma ' . $g;
        if ($mode !== 'off') {
            $c0 = $_POST['color0'] ?? '';
            if (!preg_match('/^#[0-9A-Fa-f]{6}$/', $c0)) fail('colour must be #RRGGBB');
            $args .= ' -color0 ' . escapeshellarg($c0);
            if ($mode === 'loop') {
                $c1 = $_POST['color1'] ?? '';
                if (!preg_match('/^#[0-9A-Fa-f]{6}$/', $c1)) fail('second colour must be #RRGGBB');
                $args .= ' -color1 ' . escapeshellarg($c1);
            }
            if ($mode !== 'breath') {
                $l = $_POST['light'] ?? '';
                if (!preg_match('/^\d{1,3}$/', $l) || (int)$l < 1 || (int)$l > 255) fail('brightness must be 1 to 255');
                $args .= ' -light ' . (int)$l;
            }
        }
        exec("$base set $sd $args 2>&1", $lines, $code);
        out(['ok' => $code === 0, 'output' => implode("\n", $lines)], $code === 0 ? 200 : 500);

    default:
        fail('unknown action');
}
