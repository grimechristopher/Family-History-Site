// Switches between the drawn chart and the list.
//
// Kept apart from pedigree.js so the switch works even if the drawing fails: the
// list is the default and the button only appears once a chart exists.

(function () {
  'use strict';

  var views = document.getElementById('pedigree-views');
  var button = document.querySelector('[data-pedigree-toggle]');
  if (!views || !button) return;

  var chart = document.getElementById('pedigree');
  var list = views.querySelector('.pedigree-list');

  function show(which) {
    views.dataset.view = which;
    var isChart = which === 'chart';
    button.setAttribute('aria-pressed', String(isChart));
    button.textContent = isChart ? 'Show as a list' : 'Show as a chart';
    // Hidden from assistive tech as well as from sight, so a screen reader is
    // never reading out a diagram it cannot navigate.
    chart.setAttribute('aria-hidden', String(!isChart));
    if (list) list.setAttribute('aria-hidden', String(isChart));
    try { localStorage.setItem('fhs-tree-view', which); } catch (e) {}
  }

  button.addEventListener('click', function () {
    show(views.dataset.view === 'chart' ? 'list' : 'chart');
  });

  // The list shows until a chart exists. Once one does, restore whichever view
  // was last chosen.
  show('list');

  button.addEventListener('pedigree:ready', function () {
    var saved;
    try { saved = localStorage.getItem('fhs-tree-view'); } catch (e) {}
    if (saved === 'chart') show('chart');
  });
})();
