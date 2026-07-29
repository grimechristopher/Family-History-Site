// The reader's text size.
//
// Everything is sized in rem, so setting the root size scales the whole
// interface proportionally — far easier to find than Safari's own settings.
//
// Loaded without `defer` so the saved choice is applied before first paint and
// nobody watches the page resize underneath them.

(function () {
  'use strict';

  var KEYS = { size: 'fhs-size' };
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
    ['size'].forEach(function (kind) {
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
