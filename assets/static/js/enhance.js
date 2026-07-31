// Everything that needs the page to exist.
//
// Kept out of display.js on purpose. That file runs in the head, without defer, so
// the reader's saved text size is applied before first paint -- which means it runs
// before there is a body to look at. Four enhancements were written in there and
// none of them ever ran: the filter panel measured nothing, the name never came off
// the tree, and choosing somebody in the rail still reloaded the whole page. They
// failed silently, because each one begins by looking for an element and giving up
// politely when it is not there.
//
// This file is deferred and loaded after htmx, so both the document and htmx are
// ready by the time any of it runs.

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

// How tall the filter panel may be.
//
// On a narrow screen the panel is positioned against the controls row, and that
// row is sticky -- so the panel travels with it and stays near the top of the
// screen. Scrolling the page cannot bring the bottom of it into view, and with
// four lines in it the panel is taller than the phone it is on. The CSS gives it a
// conservative 60dvh so it is always scrollable; this measures the room actually
// there and gives it the rest.
(function () {
  'use strict';
  var toggle = document.querySelector('.side-toggle');
  if (!toggle) return;
  var panel = toggle.querySelector('.side');
  if (!panel) return;

  function fit() {
    // Only while it is behaving as a drawer. On a wide screen the rail is an
    // ordinary column and sizing it against the viewport would cut it off.
    if (!toggle.open || getComputedStyle(panel).position !== 'absolute') {
      panel.style.maxHeight = '';
      return;
    }
    panel.style.maxHeight = '';
    var top = panel.getBoundingClientRect().top;
    var room = window.innerHeight - top - 16;
    // Never smaller than a couple of rows: if the measurement comes out absurd,
    // the stylesheet's own limit is the better answer.
    if (room > 120) panel.style.maxHeight = Math.round(room) + 'px';
  }

  toggle.addEventListener('toggle', fit);
  window.addEventListener('resize', fit);
  window.addEventListener('orientationchange', fit);
  // The row it hangs from is sticky, so its position can change as the page moves.
  window.addEventListener('scroll', fit, { passive: true });
  fit();
})();

// Choosing somebody in the filter rail replaces the questions and leaves the rail
// alone.
//
// It used to be an ordinary link, so picking a grandparent reloaded the page: back
// to the top, the rail scrolled to the start, every group you had opened closed
// again. The links still work as links -- this only intercepts them when htmx is
// there to do the swap, so with no JavaScript the page loads as it always did.
(function () {
  'use strict';
  if (!window.htmx) return;

  document.querySelectorAll('a.side-link[data-swaps-list]').forEach(function (link) {
    link.setAttribute('hx-get', link.getAttribute('href'));
    link.setAttribute('hx-target', '#question-list');
    link.setAttribute('hx-swap', 'outerHTML');
    // So the address stays right and the back button still works.
    link.setAttribute('hx-push-url', 'true');
  });
  window.htmx.process(document.body);

  // The highlight moves here rather than by redrawing the rail, which is the one
  // thing that must not happen: redrawing it would lose the scroll position and the
  // open groups all over again.
  document.addEventListener('click', function (event) {
    var link = event.target.closest && event.target.closest('a.side-link[data-swaps-list]');
    if (!link) return;
    document.querySelectorAll('a.side-link[data-swaps-list][aria-current]').forEach(function (other) {
      other.removeAttribute('aria-current');
    });
    link.setAttribute('aria-current', 'true');
  });

  // The rail must not move. Pushing a new address makes the browser take an
  // interest in scroll position, and the rail drifted a few hundred pixels on every
  // choice -- which is most of what "the sidebar shouldn't change" means. Its
  // position is taken before the request and put back after, for whichever element
  // is doing the scrolling: the details wrapper on a wide screen, the panel itself
  // when it is a drawer.
  var scrollers = function () {
    return [document.querySelector('.side-toggle'), document.querySelector('.side')]
      .filter(Boolean);
  };
  var held = null;
  document.body.addEventListener('htmx:beforeRequest', function (event) {
    if (!event.detail || !event.detail.target) return;
    if (event.detail.target.id !== 'question-list') return;
    held = scrollers().map(function (el) { return el.scrollTop; });
  });
  var restore = function (event) {
    if (!event.detail || !event.detail.target) return;
    if (event.detail.target.id !== 'question-list') return;
    if (held) {
      scrollers().forEach(function (el, i) {
        if (held[i] !== undefined) el.scrollTop = held[i];
      });
    }
    // The questions are what changed, so that is what should be at the top -- not
    // the whole window, which would scroll the rail away on a phone.
    var column = document.querySelector('.with-side-main');
    if (column && column.scrollTop > 0) column.scrollTop = 0;
  };
  document.body.addEventListener('htmx:afterSwap', restore);
  // Again after settling, because that is when the pushed address is applied and
  // anything the browser wants to do about scrolling has happened.
  document.body.addEventListener('htmx:afterSettle', function (event) {
    restore(event);
    held = null;
  });
})();
