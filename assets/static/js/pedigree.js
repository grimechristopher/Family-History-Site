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
//
// A pedigree can only show ancestors: every box has exactly two parents above it
// and nothing beside it, so the brothers, sisters and cousins the family most
// wants to write about have nowhere to go. Boxes with children carry a badge that
// opens a second, downward chart underneath — the same person at the left, their
// children beside them, their grandchildren after that. Two charts rather than one
// crowded one, because a descendant fanning out of a pedigree collides with the
// generation already drawn in that column.

(function () {
  'use strict';

  var mount = document.getElementById('pedigree');
  if (!mount || !window.d3 || !window.d3.hierarchy) return;

  // Sized so all four generations fit the page without scrolling on a laptop.
  // Wider boxes looked better in isolation and pushed the great-grandparents off
  // the right edge, which is worse.
  // Every box reserves its bottom-right corner for the badge that opens a person
  // downward, so the sizes below are the text plus that corner.
  var NODE_W = 220;
  var NODE_H = 68;
  var COUPLE_W = 240;   // a pair needs room for two names
  var COUPLE_H = 104;
  var GAP_X = 56;       // between generations
  var GAP_Y = 16;       // between siblings
  var PAD = 24;

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

  function boxW(d) { return d.members ? COUPLE_W : NODE_W; }
  function boxH(d) { return d.members ? COUPLE_H : NODE_H; }

  function spokenLabel(d) {
    var spoken = d.members
      ? d.members.map(function (m) {
          return m.name + (m.years ? ', ' + m.years : '');
        }).join(' and ')
      : d.name + (d.years ? ', ' + d.years : '');
    if (d.total) spoken += '. ' + (d.answered || 0) + ' of ' + d.total + ' questions answered';
    return spoken;
  }

  // One box. Shared by both charts, so a person looks the same whichever one they
  // are read in.
  //
  // isRoot is passed rather than read off the data: a person's generation is
  // counted from the pedigree's root, so everybody in an opened chart carries
  // generation zero and every one of them was drawn as the person the page is
  // about -- eight apricot boxes and no way to tell which was which.
  function drawBox(d, x, y, extraClass, isRoot) {
    var w = boxW(d), h = boxH(d);
    var cls = 'pnode';
    if (isRoot) cls += ' pnode-root';
    if (d.members) cls += ' pnode-couple';
    if (extraClass) cls += ' ' + extraClass;
    var group = el('g', { class: cls });

    // The line goes on the link because a slug is only unique inside one: every
    // line has a "further-back", and without this the link landed on whichever
    // the database returned first.
    var shell = d.slug
      ? el('a', {
          href: '/subjects/' + d.slug + (d.family ? '?family=' + encodeURIComponent(d.family) : ''),
          class: 'pnode-hit', 'aria-label': spokenLabel(d)
        })
      : el('g', { class: 'pnode-hit pnode-flat', role: 'text', 'aria-label': spokenLabel(d) });

    shell.appendChild(el('rect', {
      x: x, y: y, width: w, height: h, rx: 10, class: 'pnode-box'
    }));

    if (d.members) {
      // One box, both names, each with their own years underneath.
      d.members.forEach(function (m, i) {
        var top = y + 24 + i * 36;
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
        // Left-aligned under the second name. It used to sit in the bottom-right
        // corner, which is where the badge now lives.
        var count = el('text', { x: x + 14, y: y + h - 12, class: 'pnode-meta' });
        count.textContent = (d.answered || 0) + '/' + d.total + ' answered';
        shell.appendChild(count);
      }
    } else {
      var name = el('text', { x: x + 14, y: y + 25, class: 'pnode-name' });
      name.textContent = d.name;
      shell.appendChild(name);

      var meta = [];
      if (d.years) meta.push(d.years);
      if (d.total) meta.push((d.answered || 0) + '/' + d.total);
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
    return group;
  }

  // The badge that opens a person downward. Sits in the bottom-right corner, which
  // no text is allowed into, and outside the <a> around the box -- a button nested
  // in a link is neither one thing nor the other.
  function drawOpener(d, x, y, onOpen) {
    var cx = x + boxW(d) - 22;
    var cy = y + boxH(d) - 18;
    var count = d.kin.length;
    var who = d.members ? d.members[0].name.split(' ')[0] + '’s' : d.name.split(' ')[0] + '’s';

    var g = el('g', {
      class: 'pnode-open',
      role: 'button',
      tabindex: '0',
      'aria-label': 'Show ' + who + ' ' + (count === 1 ? 'child' : count + ' children') +
        ' and grandchildren'
    });
    g.appendChild(el('circle', { cx: cx, cy: cy, r: 12, class: 'pnode-open-disc' }));
    var mark = el('text', { x: cx, y: cy + 5, class: 'pnode-open-mark' });
    mark.setAttribute('text-anchor', 'middle');
    mark.textContent = count;
    g.appendChild(mark);

    function go(event) {
      event.preventDefault();
      event.stopPropagation();
      onOpen();
    }
    g.addEventListener('click', go);
    g.addEventListener('keydown', function (event) {
      if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') go(event);
    });
    return g;
  }

  // The downward chart, drawn with the same boxes so the two read as one family
  // rather than two diagrams.
  //
  // Mirrored, so the youngest are on the left in both: the pedigree runs backwards
  // to the right, and a descendant chart that ran forwards to the right would
  // reverse the direction of time halfway down the page. The grandchildren are at
  // the left edge, the person you opened is at the right, and the eye travels the
  // same way it just did above.
  function drawDescendants(d) {
    var root = window.d3.hierarchy(d, function (n) { return n.kin; });
    window.d3.tree().nodeSize([NODE_H + GAP_Y, NODE_W + GAP_X])(root);

    var nodes = root.descendants();
    var minY = Infinity, maxY = -Infinity, contentW = 0;
    nodes.forEach(function (n) {
      n.py = n.x;
      // Right edges rather than left, because the columns are right-aligned once
      // mirrored -- a couple's box is wider than an individual's and would
      // otherwise stick out.
      n.right = n.y + boxW(n.data);
      if (n.py < minY) minY = n.py;
      if (n.py > maxY) maxY = n.py;
      if (n.right > contentW) contentW = n.right;
    });
    nodes.forEach(function (n) {
      n.px = contentW - n.right;
    });

    var height = (maxY - minY) + COUPLE_H + PAD * 2;
    var width = contentW + PAD * 2;
    var shiftY = PAD - minY + COUPLE_H / 2;

    var svg = el('svg', {
      viewBox: '0 0 ' + width + ' ' + height,
      width: width,
      height: height,
      role: 'img',
      'aria-label': 'The children and grandchildren of ' + d.name
    });

    var linkLayer = el('g', { fill: 'none', 'stroke-width': 1.5 });
    linkLayer.setAttribute('stroke', token('--rule-firm', '#c9b697'));
    svg.appendChild(linkLayer);

    root.links().forEach(function (link) {
      // Mirrored, the parent is on the right and the child on the left, so the
      // line leaves the parent's left edge and arrives at the child's right one.
      linkLayer.appendChild(el('path', {
        d: elbow(
          { x: link.source.px + PAD, y: link.source.py + shiftY },
          { x: link.target.px + PAD + boxW(link.target.data), y: link.target.py + shiftY }
        )
      }));
    });

    nodes.forEach(function (n) {
      var data = n.data;
      var x = n.px + PAD;
      var y = n.py + shiftY - boxH(data) / 2;
      // The person the chart is about, and the one you came in through, are both
      // worth marking: without them a row of names gives no clue where you are.
      var extra = n.depth === 0 ? 'pnode-subject' : (data.onLine ? 'pnode-online' : '');
      svg.appendChild(drawBox(data, x, y, extra, false));
    });

    return svg;
  }

  // At most one open at a time. Two charts is a comparison; five is a mess, and on
  // a phone it is a page you cannot get back to the top of.
  function openKin(figure, d) {
    var panel = figure.querySelector('.kin-panel');
    if (panel) panel.remove();

    panel = document.createElement('section');
    panel.className = 'kin-panel';
    panel.tabIndex = -1;

    var head = document.createElement('div');
    head.className = 'kin-head';

    var title = document.createElement('p');
    title.className = 'kin-title';
    title.textContent = d.name + '’s children and grandchildren';
    head.appendChild(title);

    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'kin-close';
    close.textContent = 'Close';
    close.addEventListener('click', function () {
      panel.remove();
    });
    head.appendChild(close);
    panel.appendChild(head);

    var scroller = document.createElement('div');
    scroller.className = 'pedigree-scroll';
    scroller.appendChild(drawDescendants(d));
    panel.appendChild(scroller);

    figure.appendChild(panel);
    panel.focus();
    panel.scrollIntoView({
      block: 'nearest',
      behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'
    });
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

      var height = (maxY - minY) + COUPLE_H + PAD * 2;
      var width = maxX + COUPLE_W + PAD * 2;
      var shiftY = PAD - minY + COUPLE_H / 2;

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

      root.links().forEach(function (link) {
        // Drawn from the child's right edge to the parent's left edge.
        linkLayer.appendChild(el('path', {
          d: elbow(
            { x: link.source.px + PAD + boxW(link.source.data), y: link.source.py + shiftY },
            { x: link.target.px + PAD, y: link.target.py + shiftY }
          )
        }));
      });

      nodes.forEach(function (n) {
        var d = n.data;
        var x = n.px + PAD;
        var y = n.py + shiftY - boxH(d) / 2;
        svg.appendChild(drawBox(d, x, y, '', d.gen === 0));
        if (d.kin && d.kin.length) {
          svg.appendChild(drawOpener(d, x, y, function () { openKin(figure, d); }));
        }
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
        // A chart opened in one line has no business staying open behind another.
        var panel = f.figure.querySelector('.kin-panel');
        if (panel && i !== index) panel.remove();
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
      // The label already names the line -- "The Grime line" -- so the possessive
      // that read well against a person's name now reads as a stutter.
      button.textContent = f.label;
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
