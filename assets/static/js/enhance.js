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

// Pointing out a face.
//
// Click or tap the photograph and the hidden fields fill in with where, as a
// percentage of the width and height so the pin stays on the right face at any
// size. Then choose who it is.
//
// Without this the form still works: choose a name, submit, and they are recorded as
// being in the picture with no point. That is worth having on its own -- knowing
// somebody is in a photograph is most of the value; knowing which one they are is
// the rest.
(function () {
  'use strict';
  var frame = document.getElementById('photo-frame');
  var image = document.getElementById('photo-image');
  if (!frame || !image) return;

  // Put the saved pins where they belong. This is done here rather than with a
  // style attribute in the markup because the Content-Security-Policy has
  // style-src 'self' and no unsafe-inline, so the browser ignores a style
  // attribute entirely -- silently, which is how the pins ended up stacked in the
  // corner. Setting it from script is not affected by that rule.
  var photoID = frame.dataset.photo;
  frame.querySelectorAll('.photo-pin[data-x]').forEach(function (pin) {
    pin.style.left = pin.dataset.x + '%';
    pin.style.top = pin.dataset.y + '%';
    pin.hidden = false;
    pin.dataset.draggable = 'yes';
  });

  // On a touchscreen there is no hovering, so a tap on the picture reveals the pins
  // and a tap elsewhere puts them away again.
  frame.addEventListener('pointerdown', function (event) {
    if (event.pointerType === 'touch') frame.dataset.showPins = 'yes';
  });
  document.addEventListener('pointerdown', function (event) {
    if (!frame.contains(event.target)) delete frame.dataset.showPins;
  });

  // Dragging a pin moves who it points at.
  //
  // Saved as a percentage on release rather than continuously: one write when you
  // let go, not one per pixel. The same endpoint the form uses, which already
  // treats a repeat as moving the point.
  var dragging = null;

  function percentFrom(event) {
    var box = frame.getBoundingClientRect();
    return {
      x: Math.min(100, Math.max(0, ((event.clientX - box.left) / box.width) * 100)),
      y: Math.min(100, Math.max(0, ((event.clientY - box.top) / box.height) * 100))
    };
  }

  frame.addEventListener('pointerdown', function (event) {
    var pin = event.target.closest && event.target.closest('.photo-pin[data-draggable]');
    if (!pin) return;
    dragging = { pin: pin, moved: false };
    pin.dataset.dragging = 'yes';
    pin.setPointerCapture(event.pointerId);
    event.preventDefault();
  });

  frame.addEventListener('pointermove', function (event) {
    if (!dragging) return;
    var at = percentFrom(event);
    dragging.pin.style.left = at.x.toFixed(2) + '%';
    dragging.pin.style.top = at.y.toFixed(2) + '%';
    dragging.at = at;
    dragging.moved = true;
  });

  function endDrag(event) {
    if (!dragging) return;
    var pin = dragging.pin;
    delete pin.dataset.dragging;
    var moved = dragging.moved, at = dragging.at;
    dragging = null;
    if (!moved || !at) return;

    // The link underneath must not fire on the click that ends a drag.
    var swallow = function (e) { e.preventDefault(); pin.removeEventListener('click', swallow, true); };
    pin.addEventListener('click', swallow, true);

    pin.dataset.saving = 'yes';
    var body = new URLSearchParams();
    body.set('subject_id', pin.dataset.subject);
    body.set('x', at.x.toFixed(2));
    body.set('y', at.y.toFixed(2));
    fetch('/photos/' + photoID + '/people', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString()
    }).then(function (res) {
      delete pin.dataset.saving;
      if (!res.ok) throw new Error('status ' + res.status);
      pin.dataset.x = at.x.toFixed(2);
      pin.dataset.y = at.y.toFixed(2);
    }).catch(function (err) {
      // Put it back where it was, so the picture never shows a position that was
      // not saved.
      delete pin.dataset.saving;
      pin.style.left = pin.dataset.x + '%';
      pin.style.top = pin.dataset.y + '%';
      if (window.console) console.warn('could not move the pin:', err);
    });
  }
  frame.addEventListener('pointerup', endDrag);
  frame.addEventListener('pointercancel', endDrag);

  var sheet = document.getElementById('tag-sheet');
  if (!sheet) return;

  var fx = document.getElementById('tag-x');
  var fy = document.getElementById('tag-y');
  var where = document.getElementById('tag-where');
  var marker = null;

  image.style.cursor = 'crosshair';

  image.addEventListener('click', function (event) {
    var box = image.getBoundingClientRect();
    var x = ((event.clientX - box.left) / box.width) * 100;
    var y = ((event.clientY - box.top) / box.height) * 100;
    if (x < 0 || x > 100 || y < 0 || y > 100) return;

    fx.value = x.toFixed(2);
    fy.value = y.toFixed(2);

    // A provisional pin, so it is obvious what was picked before saving.
    if (!marker) {
      marker = document.createElement('span');
      marker.className = 'photo-pin photo-pin-new';
      marker.innerHTML = '<span class="photo-pin-dot" aria-hidden="true"></span>' +
        '<span class="photo-pin-name">who is this?</span>';
      frame.appendChild(marker);
    }
    marker.style.left = x.toFixed(2) + '%';
    marker.style.top = y.toFixed(2) + '%';

    where.hidden = false;
    where.textContent = 'Face picked. Now say who it is.';
    // Straight into the question, since that is what clicking a face was asking.
    if (typeof sheet.showModal === 'function' && !sheet.open) sheet.showModal();
    var pick = document.getElementById('tag-subject');
    if (pick) pick.focus();
  });
})();

// Anything that opens a dialog.
//
// A button carrying data-opens names the dialog it belongs to. Kept generic because
// this is the second thing on the site that wants a modal and will not be the last.
(function () {
  'use strict';
  document.querySelectorAll('[data-opens]').forEach(function (trigger) {
    var sheet = document.getElementById(trigger.dataset.opens);
    if (!sheet || typeof sheet.showModal !== 'function') return;

    trigger.addEventListener('click', function () {
      sheet.showModal();
      // Straight to the first thing they came to do.
      var first = sheet.querySelector('input, select, textarea, button');
      if (first) first.focus();
    });

    // Clicking the darkened area outside closes it, which is what everybody
    // expects and what a keyboard user gets from Escape for free.
    sheet.addEventListener('click', function (event) {
      if (event.target === sheet) sheet.close();
    });
  });
})();

// A field that only matters once a particular choice is made.
//
// The name box for somebody outside the family stood open beside a list of the
// family, which invited the question of which one you were meant to fill in. It
// appears when the choice that needs it is made.
(function () {
  'use strict';
  document.querySelectorAll('select[data-reveals]').forEach(function (pick) {
    var target = document.getElementById(pick.dataset.reveals);
    if (!target) return;
    var when = pick.dataset.revealsOn;

    var sync = function () {
      var wanted = pick.value === when;
      target.hidden = !wanted;
      var field = target.querySelector('input, textarea');
      if (field) {
        // Required only while it is the thing being asked for, or the form cannot
        // be submitted at all once it is hidden.
        if (wanted) {
          field.required = true;
          field.focus();
        } else {
          field.required = false;
          field.value = '';
        }
      }
    };
    pick.addEventListener('change', sync);
    sync();
  });
})();
