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

  // The filter rail is a hamburger below 62rem and a column above it. The
  // markup ships open so it is reachable without JavaScript; this closes it on
  // narrow screens and follows the breakpoint if the window is resized or the
  // iPad is turned.
  function followBreakpoint() {
    var panel = document.querySelector('.side-toggle');
    if (!panel) return;
    var wide = window.matchMedia('(min-width: 62rem)');

    function sync(isWide) { panel.open = isWide; }
    sync(wide.matches);

    if (wide.addEventListener) {
      wide.addEventListener('change', function (e) { sync(e.matches); });
    } else if (wide.addListener) {
      wide.addListener(function (e) { sync(e.matches); });
    }
  }

  function start() { wire(); followBreakpoint(); }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();

// Fill in the name from the tree, so it is visible and correctable before the
// form is sent rather than appearing afterwards. The server derives it too: this
// is the courtesy, not the rule.
(function () {
  'use strict';
  var picker = document.querySelector('select[data-names-field]');
  if (!picker) return;
  var field = document.getElementById(picker.dataset.namesField);
  if (!field) return;

  // Asking for a name is only worth doing when there is nobody on the tree to
  // take it from. Hidden rather than removed, so the form still carries the field
  // and still works for somebody whose browser never ran this.
  var wrapper = document.querySelector('[data-hide-when-picked]');
  var syncWrapper = function () {
    if (wrapper) wrapper.hidden = picker.value !== '';
  };
  syncWrapper();

  picker.addEventListener('change', function () {
    syncWrapper();
    var chosen = picker.options[picker.selectedIndex];
    var name = chosen && chosen.dataset ? chosen.dataset.name : '';
    // Never over-write something typed by hand: somebody who has written "Aunt
    // Jane" meant it, and the tree's version of her name is not an improvement.
    if (name && (!field.value || field.dataset.fromTree === 'yes')) {
      field.value = name;
      field.dataset.fromTree = 'yes';
    } else if (!name && field.dataset.fromTree === 'yes') {
      field.value = '';
      delete field.dataset.fromTree;
    }
  });

  field.addEventListener('input', function () { delete field.dataset.fromTree; });
})();
