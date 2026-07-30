// The drawn pedigree.
//
// Uses d3-hierarchy's tree layout — the one genuinely hard part — and draws the
// SVG by hand. d3-shape is not vendored for the sake of one bezier, and
// Three.js would mean a WebGL renderer and camera to draw boxes and text that
// SVG carries natively.
//
// Since this is now the only view of the tree, every box is a real SVG <a> with a
// spoken label naming the person, their years and their progress. That makes the
// chart keyboard-navigable and readable aloud — weaker than a list of links, but
// the diagram is no longer a dead end for anyone using one.

(function () {
  'use strict';

  var mount = document.getElementById('pedigree');
  if (!mount || !window.d3 || !window.d3.hierarchy) return;

  // Sized so all four generations fit the page without scrolling on a laptop.
  // Wider boxes looked better in isolation and pushed the great-grandparents off
  // the right edge, which is worse.
  var NODE_W = 200;
  var NODE_H = 60;
  var COUPLE_W = 240;   // a pair needs room for two names
  var COUPLE_H = 92;
  var GAP_X = 56;       // between generations
  var GAP_Y = 12;       // between siblings

  var css = getComputedStyle(document.documentElement);
  function token(name, fallback) {
    return (css.getPropertyValue(name) || '').trim() || fallback;
  }

  var svgNS = 'http://www.w3.org/2000/svg';
  function el(name, attrs) {
    var node = document.createElementNS(svgNS, name);
    for (var k in attrs) {
      if (attrs[k] !== undefined && attrs[k] !== null) node.setAttribute(k, attrs[k]);
    }
    return node;
  }

  // An elbow rather than a curve: a pedigree is a record of fact, and right
  // angles read as a diagram instead of a flourish.
  function elbow(source, target) {
    var midX = (source.x + target.x) / 2;
    return 'M' + source.x + ',' + source.y +
           'H' + midX + 'V' + target.y + 'H' + target.x;
  }

  function render(roots) {
    mount.textContent = '';
    var figures = [];

    roots.forEach(function (rootData, rootIndex) {
      var root = window.d3.hierarchy(rootData, function (d) { return d.parents; });

      // Laid out vertically then transposed, so generations run left to right
      // and names have room to be read. Spaced for the tallest box, so a couple
      // never overlaps its neighbour.
      var layout = window.d3.tree().nodeSize([COUPLE_H + GAP_Y, COUPLE_W + GAP_X]);
      layout(root);

      var nodes = root.descendants();
      var minY = Infinity, maxY = -Infinity, maxX = 0;
      nodes.forEach(function (n) {
        // d3 lays out x down the page; transpose so depth is horizontal.
        var x = n.y, y = n.x;
        n.px = x; n.py = y;
        if (y < minY) minY = y;
        if (y > maxY) maxY = y;
        if (x > maxX) maxX = x;
      });

      var padding = 24;
      var height = (maxY - minY) + COUPLE_H + padding * 2;
      var width = maxX + COUPLE_W + padding * 2;
      var shiftY = padding - minY + COUPLE_H / 2;

      var figure = document.createElement('figure');
      figure.className = 'pedigree-figure';
      figure.dataset.line = String(rootIndex);

      var caption = document.createElement('figcaption');
      caption.className = 'pedigree-caption';
      caption.textContent = rootData.name;
      figure.appendChild(caption);

      var scroller = document.createElement('div');
      scroller.className = 'pedigree-scroll';

      var svg = el('svg', {
        viewBox: '0 0 ' + width + ' ' + height,
        width: width,
        height: height,
        role: 'img',
        'aria-label': 'Pedigree chart for ' + rootData.name +
          '. The same family is listed as links below.'
      });

      var linkLayer = el('g', { fill: 'none', 'stroke-width': 1.5 });
      linkLayer.setAttribute('stroke', token('--rule-firm', '#c9b697'));
      svg.appendChild(linkLayer);

      function boxW(d) { return d.members ? COUPLE_W : NODE_W; }
      function boxH(d) { return d.members ? COUPLE_H : NODE_H; }

      root.links().forEach(function (link) {
        // Drawn from the child's right edge to the parent's left edge.
        linkLayer.appendChild(el('path', {
          d: elbow(
            { x: link.source.px + padding + boxW(link.source.data), y: link.source.py + shiftY },
            { x: link.target.px + padding, y: link.target.py + shiftY }
          )
        }));
      });

      nodes.forEach(function (n) {
        var d = n.data;
        var w = boxW(d), h = boxH(d);
        var x = n.px + padding;
        var y = n.py + shiftY - h / 2;

        var cls = 'pnode';
        if (d.gen === 0) cls += ' pnode-root';
        if (d.members) cls += ' pnode-couple';
        var group = el('g', { class: cls });

        // Spoken label, so tabbing through the chart reads as sentences rather
        // than as a pile of shapes.
        var spoken = d.members
          ? d.members.map(function (m) {
              return m.name + (m.years ? ', ' + m.years : '');
            }).join(' and ')
          : d.name + (d.years ? ', ' + d.years : '');
        if (d.total) spoken += '. ' + (d.answered || 0) + ' of ' + d.total + ' questions answered';

        var shell = d.slug
          ? el('a', { href: '/subjects/' + d.slug, class: 'pnode-hit', 'aria-label': spoken })
          : el('g', { class: 'pnode-hit pnode-flat', role: 'text', 'aria-label': spoken });

        shell.appendChild(el('rect', {
          x: x, y: y, width: w, height: h, rx: 10, class: 'pnode-box'
        }));

        if (d.members) {
          // One box, both names, each with their own years underneath.
          d.members.forEach(function (m, i) {
            var top = y + 23 + i * 35;
            var nm = el('text', { x: x + 14, y: top, class: 'pnode-name' });
            nm.textContent = m.name;
            shell.appendChild(nm);
            if (m.years) {
              var yr = el('text', { x: x + 14, y: top + 16, class: 'pnode-meta' });
              yr.textContent = m.years;
              shell.appendChild(yr);
            }
          });
          if (d.total) {
            var count = el('text', { x: x + w - 14, y: y + h - 10, class: 'pnode-meta pnode-count' });
            count.setAttribute('text-anchor', 'end');
            count.textContent = (d.answered || 0) + '/' + d.total + ' answered';
            shell.appendChild(count);
          }
        } else {
          var name = el('text', { x: x + 14, y: y + 25, class: 'pnode-name' });
          name.textContent = d.name;
          shell.appendChild(name);

          var meta = [];
          if (d.years) meta.push(d.years);
          if (d.total) meta.push((d.answered || 0) + '/' + d.total + ' answered');
          if (meta.length) {
            var sub = el('text', { x: x + 14, y: y + 46, class: 'pnode-meta' });
            sub.textContent = meta.join('  ·  ');
            shell.appendChild(sub);
          }
        }

        // A left edge in the accent marks people already talked about, the same
        // signal the list uses.
        if (d.answered > 0) {
          shell.appendChild(el('rect', {
            x: x, y: y, width: 4, height: h, rx: 2, class: 'pnode-told'
          }));
        }

        group.appendChild(shell);
        svg.appendChild(group);
      });

      scroller.appendChild(svg);
      figure.appendChild(scroller);
      mount.appendChild(figure);
      figures.push({
        figure: figure,
        scroller: scroller,
        // Where the person the chart is about sits, so the view can open on them.
        rootY: root.py + shiftY,
        label: rootData.label || rootData.name
      });
    });

    buildPicker(figures);
    if (figures.length) focusRoot(figures.find(function (f) { return !f.figure.hidden; }) || figures[0]);
  }

  // Opens the chart on the person it is about. The pedigree fans out to the right
  // with the root halfway down, so a phone-sized window onto it starts on empty
  // canvas: 366px of a 1176px chart, with the root 364px below the top edge.
  // Computed from the layout rather than measured, because a hidden figure has no
  // box to measure.
  function focusRoot(f) {
    if (!f || !f.scroller) return;
    f.scroller.scrollLeft = 0;
    f.scroller.scrollTop = Math.max(0, f.rootY - f.scroller.clientHeight / 2);
  }

  // One button per line. Pointless with a single line, so it is only built when
  // there is more than one.
  function buildPicker(figures) {
    var picker = document.getElementById('line-picker');
    if (!picker) return;
    picker.textContent = '';
    if (figures.length < 2) return;

    function show(index) {
      figures.forEach(function (f, i) {
        f.figure.hidden = i !== index;
      });
      // After unhiding, so clientHeight is real.
      focusRoot(figures[index]);
      Array.prototype.forEach.call(picker.children, function (b, i) {
        b.setAttribute('aria-pressed', String(i === index));
      });
      // Stored by name rather than position, so it survives a line being added.
      try { localStorage.setItem('fhs-tree-line', figures[index].label); } catch (e) {}
    }

    figures.forEach(function (f, i) {
      var button = document.createElement('button');
      button.type = 'button';
      button.className = 'line-button';
      button.textContent = f.label + '\u2019s family';
      button.addEventListener('click', function () { show(i); });
      picker.appendChild(button);
    });

    function indexOfLabel(label) {
      if (!label) return -1;
      for (var i = 0; i < figures.length; i++) {
        if (figures[i].label === label) return i;
      }
      return -1;
    }

    var saved = null;
    try { saved = localStorage.getItem('fhs-tree-line'); } catch (e) {}

    // Your own line first: signed in as Mom, her family is the one you came to
    // look at. A previous explicit choice still wins over that.
    var viewer = mount.dataset.viewer;
    var start = indexOfLabel(saved);
    if (start < 0) start = indexOfLabel(viewer);
    if (start < 0) start = 0;
    show(start);
  }

  // The address comes from the page rather than being written here: every page now
  // lives under /f/{family}/, so a hardcoded path fetches nothing at all.
  fetch(mount.dataset.src || '/tree.json', { credentials: 'same-origin' })
    .then(function (res) {
      if (!res.ok) throw new Error('status ' + res.status);
      return res.json();
    })
    .then(function (roots) {
      render(roots);
    })
    .catch(function (err) {
      // The chart is the only view now, so the failure has to offer a way on.
      mount.innerHTML = '<p class="banner">The family chart could not be drawn ' +
        'just now. You can still reach everyone from ' +
        '<a class="link" href="/questions">Questions</a>.</p>';
      if (window.console) console.warn('pedigree:', err);
    });
})();
