// Reader display preferences: colour polarity, contrast, and text size.
//
// Whether light-on-dark or dark-on-light is easier to read with macular
// degeneration is individual and the research is inconsistent, so this is a
// choice rather than a decision made for the reader. "Auto" follows whatever
// the iPad is already set to.
//
// Loaded without `defer` so the saved choice is applied before first paint and
// nobody watches the page change colour underneath them.

(function () {
  'use strict';

  var KEYS = { theme: 'fhs-theme', contrast: 'fhs-contrast', size: 'fhs-size' };
  var root = document.documentElement;

  function read(key) {
    try { return localStorage.getItem(key); } catch (e) { return null; }
  }
  function write(key, value) {
    try {
      if (value === null) { localStorage.removeItem(key); }
      else { localStorage.setItem(key, value); }
    } catch (e) { /* private browsing: the choice just won't persist */ }
  }

  function apply() {
    var theme = read(KEYS.theme);
    if (theme) { root.setAttribute('data-theme', theme); }
    else { root.removeAttribute('data-theme'); }

    var contrast = read(KEYS.contrast);
    if (contrast) { root.setAttribute('data-contrast', contrast); }
    else { root.removeAttribute('data-contrast'); }

    var size = read(KEYS.size);
    if (size) { root.setAttribute('data-size', size); }
    else { root.removeAttribute('data-size'); }
  }

  // Before paint.
  apply();

  function markPressed(group, current) {
    group.forEach(function (btn) {
      btn.setAttribute('aria-pressed', String(btn.dataset.value === (current || 'auto')));
    });
  }

  function wire() {
    ['theme', 'contrast', 'size'].forEach(function (kind) {
      var group = Array.prototype.slice.call(
        document.querySelectorAll('[data-setting="' + kind + '"]'));
      if (!group.length) return;

      markPressed(group, read(KEYS[kind]));

      group.forEach(function (btn) {
        btn.addEventListener('click', function () {
          var value = btn.dataset.value;
          write(KEYS[kind], value === 'auto' ? null : value);
          apply();
          markPressed(group, read(KEYS[kind]));
        });
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wire);
  } else {
    wire();
  }
})();
