// Card dragging and answer autosave.
//
// Progressive enhancement throughout: the card is a plain form with "Save & next"
// and "Later" buttons, and it works with this file absent. Everything here only
// adds a gesture and a safety net.
//
// Two rules matter more than the animation:
//   1. Dragging locks the moment the textarea has focus or content. Nudging the
//      screen must never throw away a half-written paragraph.
//   2. A draft is saved to the server and to localStorage. Nothing typed is lost
//      because a connection dropped.

(function () {
  'use strict';

  var SWIPE_THRESHOLD = 110;   // px before a drag counts as "Later"
  var DRAFT_DEBOUNCE = 1500;   // ms of quiet before autosaving

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

    // Local copy first: it survives a failed request, a closed tab, and a
    // flat battery.
    try { localStorage.setItem(draftKey(url), body); } catch (e) { /* private mode */ }

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
      // The text is still on screen and still in localStorage. Say so plainly
      // rather than implying it is gone.
      setState(box, 'Not saved yet — your words are safe here, we’ll keep trying.', 'error');
    });
  }

  function restoreDraft(textarea) {
    var url = textarea.getAttribute('data-draft-url');
    if (!url) return;
    var stored;
    try { stored = localStorage.getItem(draftKey(url)); } catch (e) { return; }
    // Only restore when it would not overwrite something newer from the server.
    if (stored && stored.length > textarea.value.length) {
      textarea.value = stored;
      setState(textarea.form && textarea.form.querySelector('[data-save-state]'),
        'Restored what you had typed.', null);
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
    // Leaving the field is a strong signal they are done for now.
    textarea.addEventListener('blur', function () {
      if (timer) clearTimeout(timer);
      saveDraft(textarea);
    });
  }

  // --- dragging -----------------------------------------------------------

  function wireCard(card) {
    if (card.__fhsWired) return;
    card.__fhsWired = true;

    var textarea = card.querySelector('textarea');
    var later = card.querySelector('[data-later]');
    if (!later) return;

    var startX = 0, startY = 0, dx = 0, dragging = false, pointerId = null;

    // Once there is writing in progress, the gesture is more dangerous than it
    // is useful.
    function locked() {
      if (!textarea) return false;
      if (textarea.value.trim() !== '') return true;
      return document.activeElement === textarea;
    }

    function reset(animate) {
      card.classList.remove('dragging');
      if (animate) {
        card.classList.add('settling');
        setTimeout(function () { card.classList.remove('settling'); }, 220);
      }
      card.style.transform = '';
      card.style.opacity = '';
    }

    card.addEventListener('pointerdown', function (e) {
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      // Never start a drag from a control the user meant to press.
      if (e.target.closest('textarea, button, a, input, label')) return;
      if (locked()) return;

      pointerId = e.pointerId;
      startX = e.clientX;
      startY = e.clientY;
      dx = 0;
      dragging = true;
      card.classList.add('dragging');
    });

    card.addEventListener('pointermove', function (e) {
      if (!dragging || e.pointerId !== pointerId) return;
      dx = e.clientX - startX;
      var dy = e.clientY - startY;

      // A mostly-vertical movement is a scroll, not a swipe.
      if (Math.abs(dy) > Math.abs(dx) && Math.abs(dx) < 20) return;
      if (locked()) { dragging = false; reset(true); return; }

      var fade = Math.min(Math.abs(dx) / 400, 0.45);
      card.style.transform = 'translateX(' + dx + 'px) rotate(' + (dx / 28) + 'deg)';
      card.style.opacity = String(1 - fade);
    });

    function finish(e) {
      if (!dragging || (e && e.pointerId !== pointerId)) return;
      dragging = false;

      if (Math.abs(dx) >= SWIPE_THRESHOLD && !locked()) {
        var direction = dx > 0 ? 1 : -1;
        card.classList.add('settling');
        card.style.transform = 'translateX(' + (direction * window.innerWidth) + 'px) rotate(' + (direction * 18) + 'deg)';
        card.style.opacity = '0';
        // htmx replaces the region, so no cleanup of this card is needed.
        later.click();
        return;
      }
      reset(true);
    }

    card.addEventListener('pointerup', finish);
    card.addEventListener('pointercancel', function (e) {
      if (!dragging || (e && e.pointerId !== pointerId)) return;
      dragging = false;
      reset(true);
    });
  }

  function wireAll() {
    document.querySelectorAll('textarea[data-draft-url]').forEach(wireTextarea);
    document.querySelectorAll('.card[data-question]').forEach(wireCard);
  }

  document.addEventListener('DOMContentLoaded', wireAll);
  // htmx swaps in a fresh card after every answer or deferral.
  document.body.addEventListener('htmx:afterSwap', wireAll);
})();
