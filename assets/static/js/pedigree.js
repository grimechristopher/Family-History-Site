// The drawn pedigree.
//
// Uses d3-hierarchy's tree layout — the one genuinely hard part — and draws the
// SVG by hand. d3-shape is not vendored for the sake of one bezier, and
// Three.js would mean a WebGL renderer and camera to draw boxes and text that
// SVG carries natively.
//
// This is the view for someone browsing on a laptop. The nested list on the same
// page stays the accessible path: a screen reader gets a usable outline from a
// list of links and nothing usable from a diagram, and text reflows under
// magnification where a drawing does not. Neither is a fallback for the other.

(function () {
  'use strict';

  var mount = document.getElementById('pedigree');
  if (!mount || !window.d3 || !window.d3.hierarchy) return;

  var NODE_W = 210;
  var NODE_H = 62;
  var GAP_X = 78;    // between generations
  var GAP_Y = 14;    // between siblings

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

    roots.forEach(function (rootData) {
      var root = window.d3.hierarchy(rootData, function (d) { return d.parents; });

      // Laid out vertically then transposed, so generations run left to right
      // and names have room to be read.
      var layout = window.d3.tree().nodeSize([NODE_H + GAP_Y, NODE_W + GAP_X]);
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
      var height = (maxY - minY) + NODE_H + padding * 2;
      var width = maxX + NODE_W + padding * 2;
      var shiftY = padding - minY + NODE_H / 2;

      var figure = document.createElement('figure');
      figure.className = 'pedigree-figure';

      var caption = document.createElement('figcaption');
      caption.className = 'pedigree-caption';
      caption.textContent = rootData.name + '’s line';
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

      root.links().forEach(function (link) {
        // Drawn from the child's right edge to the parent's left edge.
        linkLayer.appendChild(el('path', {
          d: elbow(
            { x: link.source.px + padding + NODE_W, y: link.source.py + shiftY },
            { x: link.target.px + padding, y: link.target.py + shiftY }
          )
        }));
      });

      nodes.forEach(function (n) {
        var d = n.data;
        var x = n.px + padding;
        var y = n.py + shiftY - NODE_H / 2;

        var group = el('g', { class: 'pnode' + (d.gen === 0 ? ' pnode-root' : '') });

        // Anchor is a link only where there is something to read, matching the
        // list exactly.
        var shell = d.slug
          ? el('a', { href: '/subjects/' + d.slug, class: 'pnode-hit' })
          : el('g', { class: 'pnode-hit pnode-flat' });

        shell.appendChild(el('rect', {
          x: x, y: y, width: NODE_W, height: NODE_H, rx: 10,
          class: 'pnode-box'
        }));

        var name = el('text', {
          x: x + 14, y: y + 25, class: 'pnode-name'
        });
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

        // A left edge in the accent marks people who have been talked about,
        // the same signal the list uses.
        if (d.answered > 0) {
          shell.appendChild(el('rect', {
            x: x, y: y, width: 4, height: NODE_H, rx: 2, class: 'pnode-told'
          }));
        }

        group.appendChild(shell);
        svg.appendChild(group);
      });

      scroller.appendChild(svg);
      figure.appendChild(scroller);
      mount.appendChild(figure);
    });
  }

  fetch('/tree.json', { credentials: 'same-origin' })
    .then(function (res) {
      if (!res.ok) throw new Error('status ' + res.status);
      return res.json();
    })
    .then(function (roots) {
      render(roots);
      // Only reveal the switch once a drawing actually exists. Which view is
      // showing is the toggle's business alone: setting it here as well had the
      // two scripts overwriting each other.
      var toggle = document.querySelector('[data-pedigree-toggle]');
      if (toggle) {
        toggle.hidden = false;
        toggle.dispatchEvent(new CustomEvent('pedigree:ready'));
      }
    })
    .catch(function (err) {
      // The list is already on the page, so say what happened and leave it.
      mount.innerHTML = '<p class="banner banner-quiet">The chart could not be ' +
        'drawn just now. The family is listed below.</p>';
      if (window.console) console.warn('pedigree:', err);
    });
})();
