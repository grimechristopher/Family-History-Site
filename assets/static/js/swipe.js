// Card tossing and answer autosave.
//
// Touch first. This is used on an iPad, held, in portrait, so the card follows
// the finger one-to-one and gets thrown rather than clicked. Pointer events
// cover touch, pencil, trackpad and mouse from a single code path.
//
// Everything here is progressive enhancement: the card is a plain form with
// "Save & next" and "Later" buttons and works with this file absent. Two rules
// outrank the fun:
//
//   1. Tossing locks the moment the textarea has focus or content. Nudging the
//      screen must never throw away a half-dictated paragraph.
//   2. Drafts go to the server and to localStorage. Nothing spoken is lost
//      because a connection dropped.
//
// No haptics: iOS Safari exposes no vibration API, so there is nothing to call.

(function () {
  'use strict';

  var TOSS_DISTANCE = 96;    // px of travel that counts as a throw
  var TOSS_VELOCITY = 0.45;  // px/ms — a quick flick counts even if it is short
  var DRAFT_DEBOUNCE = 1500; // ms of quiet before autosaving
  var TOSS_MS = 260;         // how long the card takes to leave

  var reduceMotion = window.matchMedia &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // --- drafts -------------------------------------------------------------

  function draftKey(url) { return 'fhs-draft:' + url; }

  function setState(box, message, state) {
    if (!box) return;
    box.textContent = message;
    if (state) { box.setAttribute('data-state', state); }
    else { box.removeAttribute('data-state'); }
  }

  function saveDraft(textarea) {
    var url = textarea.getAttribute('data-draft-url');
    if (!url) return;
    var body = textarea.value;
    var box = textarea.form && textarea.form.querySelector('[data-save-state]');

    // Local copy first: it survives a failed request, a closed tab, a flat
    // battery.
    try { localStorage.setItem(draftKey(url), body); } catch (e) {}

    if (body.trim() === '') { setState(box, '', null); return; }

    setState(box, 'Saving…', null);
    fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: 'body=' + encodeURIComponent(body),
      credentials: 'same-origin'
    }).then(function (res) {
      if (!res.ok) throw new Error('status ' + res.status);
      setState(box, 'Saved', null);
      try { localStorage.removeItem(draftKey(url)); } catch (e) {}
    }).catch(function () {
      // The words are still on screen and still in localStorage. Say that
      // plainly rather than implying they are gone.
      setState(box, 'Not saved yet — your words are safe here, we’ll keep trying.', 'error');
    });
  }

  function restoreDraft(textarea) {
    var url = textarea.getAttribute('data-draft-url');
    if (!url) return;
    var stored;
    try { stored = localStorage.getItem(draftKey(url)); } catch (e) { return; }
    if (stored && stored.length > textarea.value.length) {
      textarea.value = stored;
      setState(textarea.form && textarea.form.querySelector('[data-save-state]'),
        'Restored what you had said.', null);
    }
  }

  function wireTextarea(textarea) {
    if (textarea.__fhsWired) return;
    textarea.__fhsWired = true;

    restoreDraft(textarea);

    var timer = null;
    textarea.addEventListener('input', function () {
      if (timer) clearTimeout(timer);
      timer = setTimeout(function () { saveDraft(textarea); }, DRAFT_DEBOUNCE);
    });
    textarea.addEventListener('blur', function () {
      if (timer) clearTimeout(timer);
      saveDraft(textarea);
    });
  }

  // --- tossing ------------------------------------------------------------

  function wireCard(card) {
    if (card.__fhsWired) return;
    card.__fhsWired = true;

    var textarea = card.querySelector('textarea');
    var later = card.querySelector('[data-later]');
    var stack = card.closest('.stack');
    if (!later) return;

    var dragging = false;
    var pointerId = null;
    var startX = 0, startY = 0, dx = 0, dy = 0;
    var grabBelowMiddle = false;
    var samples = [];

    // Once dictation is in progress the gesture is more dangerous than useful.
    function locked() {
      if (!textarea) return false;
      if (textarea.value.trim() !== '') return true;
      return document.activeElement === textarea;
    }

    function paint(x, rotate, fade) {
      card.style.transform = 'translate3d(' + x + 'px,0,0) rotate(' + rotate + 'deg)';
      card.style.opacity = String(1 - fade);
    }

    function release(animate) {
      dragging = false;
      pointerId = null;
      samples = [];
      card.classList.remove('dragging');
      card.removeAttribute('data-toss');
      if (stack) stack.removeAttribute('data-armed');

      if (animate && !reduceMotion) {
        card.classList.add('settling');
        setTimeout(function () { card.classList.remove('settling'); }, 240);
      }
      card.style.transform = '';
      card.style.opacity = '';
    }

    card.addEventListener('pointerdown', function (e) {
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      // Never start a throw from something they meant to press or dictate into.
      if (e.target.closest('textarea, button, a, input, select, label, summary')) return;
      if (locked()) return;

      pointerId = e.pointerId;
      startX = e.clientX;
      startY = e.clientY;
      dx = dy = 0;
      samples = [{ x: e.clientX, t: e.timeStamp }];

      // Grabbing low on the card rotates it more, the way a real card would.
      var box = card.getBoundingClientRect();
      grabBelowMiddle = (e.clientY - box.top) > box.height / 2;

      dragging = true;
      card.classList.add('dragging');
      // Keep receiving moves even once the finger leaves the element.
      if (card.setPointerCapture) {
        try { card.setPointerCapture(e.pointerId); } catch (err) {}
      }
    });

    card.addEventListener('pointermove', function (e) {
      if (!dragging || e.pointerId !== pointerId) return;

      dx = e.clientX - startX;
      dy = e.clientY - startY;

      // A mostly-vertical movement is the page being scrolled, not a throw.
      if (Math.abs(dy) > Math.abs(dx) && Math.abs(dx) < 24) return;
      if (locked()) { release(true); return; }

      samples.push({ x: e.clientX, t: e.timeStamp });
      if (samples.length > 6) samples.shift();

      var lean = grabBelowMiddle ? -1 : 1;
      paint(dx, (dx / 22) * lean, Math.min(Math.abs(dx) / 520, 0.35));

      // Say what letting go will do, before they let go.
      if (Math.abs(dx) >= TOSS_DISTANCE) {
        card.setAttribute('data-toss', dx > 0 ? 'right' : 'left');
        if (stack) stack.setAttribute('data-armed', 'true');
      } else {
        card.removeAttribute('data-toss');
        if (stack) stack.removeAttribute('data-armed');
      }
    });

    function velocity() {
      if (samples.length < 2) return 0;
      var first = samples[0], last = samples[samples.length - 1];
      var ms = last.t - first.t;
      return ms > 0 ? (last.x - first.x) / ms : 0;
    }

    function finish(e) {
      if (!dragging || (e && e.pointerId !== pointerId)) return;

      var v = velocity();
      var thrown = !locked() &&
        (Math.abs(dx) >= TOSS_DISTANCE || Math.abs(v) >= TOSS_VELOCITY);

      if (!thrown) { release(true); return; }

      var direction = Math.abs(v) >= TOSS_VELOCITY ? (v > 0 ? 1 : -1) : (dx > 0 ? 1 : -1);
      dragging = false;
      pointerId = null;

      if (reduceMotion) {
        later.click();
        return;
      }

      // Carry the throw off screen, further and faster when it was flung
      // harder, so a firm flick feels different from a gentle nudge.
      var speed = Math.min(Math.abs(v), 3);
      var travel = direction * (window.innerWidth + 240);
      var lean = grabBelowMiddle ? -1 : 1;

      card.classList.remove('dragging');
      card.classList.add('tossed');
      card.style.transition =
        'transform ' + TOSS_MS + 'ms cubic-bezier(0.3,0,0.7,1), opacity ' + TOSS_MS + 'ms linear';
      card.style.transform = 'translate3d(' + travel + 'px,' + (dy * 0.4) + 'px,0) rotate(' +
        (direction * (16 + speed * 6) * lean) + 'deg)';
      card.style.opacity = '0';

      // Let the throw be seen before htmx replaces the region.
      setTimeout(function () { later.click(); }, Math.round(TOSS_MS * 0.55));
    }

    card.addEventListener('pointerup', finish);
    card.addEventListener('pointercancel', function (e) {
      if (!dragging || (e && e.pointerId !== pointerId)) return;
      release(true);
    });
    // Losing capture mid-throw must not leave the card stranded off-centre.
    card.addEventListener('lostpointercapture', function (e) {
      if (dragging && e.pointerId === pointerId) release(true);
    });

    // Keyboard equivalent, so the gesture is never the only way through.
    card.addEventListener('keydown', function (e) {
      if (e.target.closest('textarea, input, select')) return;
      if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
        e.preventDefault();
        later.click();
      }
    });
  }

  function wireAll() {
    document.querySelectorAll('textarea[data-draft-url]').forEach(wireTextarea);
    document.querySelectorAll('.card[data-question]').forEach(wireCard);
  }

  document.addEventListener('DOMContentLoaded', wireAll);
  // htmx swaps in a fresh card after every answer or throw.
  document.body.addEventListener('htmx:afterSwap', wireAll);
})();
