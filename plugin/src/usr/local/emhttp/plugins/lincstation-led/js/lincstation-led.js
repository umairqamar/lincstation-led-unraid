/* LincStation LED page: effect and hexagon colour picker. Applies changes at once through include/exec.php. */
(function ($) {
  var URL = '/plugins/lincstation-led/include/exec.php';
  var PRESETS = [
    ['#87ceeb', 'Sky blue'], ['#ffffff', 'White'], ['#ffd9a0', 'Warm white'], ['#ff0000', 'Red'],
    ['#ff8c00', 'Orange'], ['#ffd700', 'Yellow'], ['#00ff00', 'Green'], ['#00ffd5', 'Teal'],
    ['#0000ff', 'Blue'], ['#8a2be2', 'Violet'], ['#ff2fa0', 'Pink']
  ];
  var RINGS = 6, SIZE = 15;           // hexagon board: 6 rings around the centre cell
  var mode = 'normal', timer = null, ready = false;
  var colour = ['#87ceeb', '#0000ff'];

  function call(action, data) {
    return $.ajax({ url: URL, type: 'POST', dataType: 'json',
      data: $.extend({ action: action, csrf_token: window.csrf_token }, data || {}) });
  }
  function say(ok, text) {
    $('#ls-msg').removeClass('ls-ok ls-bad').addClass(ok ? 'ls-ok' : 'ls-bad').text(text);
    clearTimeout(say.t); say.t = setTimeout(function () { $('#ls-msg').text(''); }, 5000);
  }
  function failText(xhr) {
    try { var j = JSON.parse(xhr.responseText); return j.error || j.output || 'request failed'; } catch (e) { return 'request failed'; }
  }

  // ---- colour maths
  function hslToHex(h, s, l) {
    var a = s * Math.min(l, 1 - l);
    function f(n) {
      var k = (n + h / 30) % 12, c = l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
      return ('0' + Math.round(255 * c).toString(16)).slice(-2);
    }
    return '#' + f(0) + f(8) + f(4);
  }

  // ---- hexagon board: hue by angle, white in the middle, saturated near the edge, darker at the rim
  function buildHex(host) {
    var w = Math.sqrt(3) * SIZE, rows = [], ns = 'http://www.w3.org/2000/svg';
    var half = (RINGS + 0.5) * w, vh = (RINGS * 1.5 + 1) * SIZE * 2 + SIZE * 3.4;
    var svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('class', 'ls-hex');
    svg.setAttribute('viewBox', [-half, -(RINGS * 1.5 + 1) * SIZE, half * 2, vh].join(' '));
    function cell(cx, cy, fill) {
      var p = document.createElementNS(ns, 'polygon'), pts = [];
      for (var i = 0; i < 6; i++) {
        var ang = Math.PI / 180 * (60 * i - 30);
        pts.push((cx + SIZE * Math.cos(ang)).toFixed(2) + ',' + (cy + SIZE * Math.sin(ang)).toFixed(2));
      }
      p.setAttribute('points', pts.join(' '));
      p.setAttribute('fill', fill);
      p.setAttribute('data-c', fill);
      svg.appendChild(p);
    }
    for (var q = -RINGS; q <= RINGS; q++) {
      for (var r = Math.max(-RINGS, -q - RINGS); r <= Math.min(RINGS, -q + RINGS); r++) {
        var x = w * (q + r / 2), y = SIZE * 1.5 * r;
        var d = Math.max(Math.abs(q), Math.abs(r), Math.abs(q + r)), t = d / RINGS, hex;
        if (d === 0) { hex = '#ffffff'; }
        else {
          var hue = (Math.atan2(y, x) * 180 / Math.PI + 360) % 360;
          var cut = 0.72;
          if (t <= cut) { var s = t / cut; hex = hslToHex(hue, 1, 1 - 0.5 * s); }
          else { hex = hslToHex(hue, 1, 0.5 - 0.3 * ((t - cut) / (1 - cut))); }
        }
        cell(x, y, hex);
      }
    }
    // grey strip underneath: white to black
    var gy = (RINGS * 1.5 + 2.2) * SIZE, n = 9;
    for (var i = 0; i < n; i++) {
      var v = Math.round(255 * (1 - i / (n - 1))), g = ('0' + v.toString(16)).slice(-2);
      cell((i - (n - 1) / 2) * w, gy, '#' + g + g + g);
    }
    $(host).append(svg);
  }

  function setColour(slot, hex, fromUser) {
    colour[slot] = hex.toLowerCase();
    paint();
    if (fromUser) queue();
  }

  function paint() {
    [0, 1].forEach(function (slot) {
      var $p = $('.ls-pick[data-slot=' + slot + ']'), v = colour[slot];
      $p.find('.ls-chip').css('background', v);
      if (!$p.find('.ls-hexin').is(':focus')) { $p.find('.ls-hexin').val(v.toUpperCase()); }
      $p.find('.ls-native').val(v);
      $p.find('polygon').each(function () { $(this)[0].classList.toggle('sel', $(this).attr('data-c') === v); });
      $p.find('.ls-preset').each(function () { $(this).toggleClass('sel', $(this).data('c') === v); });
    });
    $('.ls-fx').each(function () { $(this).toggleClass('sel', $(this).data('mode') === mode); });
    $('#ls-colour-block').toggle(mode !== 'off');
    $('#ls-c1-block').toggle(mode === 'loop');
    $('#ls-bright-block').toggle(mode === 'normal' || mode === 'loop');
  }

  function queue() { if (!ready) return; clearTimeout(timer); timer = setTimeout(apply, 250); }

  function apply() {
    call('set', { mode: mode, color0: colour[0], color1: colour[1], light: $('#ls-light').val(),
                  gamma: $('#ls-gamma').is(':checked') ? '2.2' : '1' })
      .done(function () { say(true, 'Applied'); })
      .fail(function (xhr) { say(false, failText(xhr)); });
  }

  $(function () {
    $('.ls-pick').each(function () {
      var slot = parseInt($(this).attr('data-slot'), 10), $p = $(this);
      buildHex($p.find('.ls-hexhost'));
      PRESETS.forEach(function (p) {
        $('<button type="button" class="ls-preset">').attr('title', p[1]).data('c', p[0]).css('background', p[0]).appendTo($p.find('.ls-presets'));
      });
      $p.on('click', 'polygon', function () { setColour(slot, $(this).attr('data-c'), true); });
      $p.on('click', '.ls-preset', function () { setColour(slot, $(this).data('c'), true); });
      $p.find('.ls-native').on('input', function () { setColour(slot, this.value, true); });
      $p.find('.ls-hexin').on('input', function () {
        var v = this.value.trim(); if (v.charAt(0) !== '#') v = '#' + v;
        if (/^#[0-9A-Fa-f]{6}$/.test(v)) { setColour(slot, v, true); }
      }).on('blur', paint);
    });
    $('.ls-fx').on('click', function () { mode = $(this).data('mode'); paint(); queue(); });
    $('#ls-light').on('input', function () { $('#ls-light-val').text(this.value); }).on('change', queue);
    $('#ls-gamma').on('change', queue);

    call('get').done(function (r) {
      var s = r.state || {};
      if (['normal', 'breath', 'loop', 'off'].indexOf(s.Mode) >= 0) mode = s.Mode;
      if (/^#[0-9A-Fa-f]{6}$/.test(s.Color0)) colour[0] = s.Color0.toLowerCase();
      if (/^#[0-9A-Fa-f]{6}$/.test(s.Color1)) colour[1] = s.Color1.toLowerCase();
      var l = parseInt(s.Light, 10);
      if (l >= 1 && l <= 255) { $('#ls-light').val(l); $('#ls-light-val').text(l); }
      $('#ls-gamma').prop('checked', parseFloat(s.Gamma) > 1);
    }).fail(function (xhr) { say(false, failText(xhr)); })
      .always(function () { paint(); ready = true; });
    paint();
  });
})(jQuery);
