// The only JavaScript needed to sign in.
//
// Supabase magic links return their tokens in the URL fragment, and browsers
// never send a fragment to the server, so Go cannot see it. This reads the
// fragment, hands the access token to Go once, and Go issues its own long-lived
// session cookie. After this, no page on the site needs JavaScript to work.

(function () {
  'use strict';

  var status = document.getElementById('status');

  function fail(message) {
    if (status) {
      status.textContent = message;
      status.className = 'banner';
    }
  }

  var hash = window.location.hash.replace(/^#/, '');
  var params = new URLSearchParams(hash);

  // Supabase reports its own failures in the fragment too.
  var providerError = params.get('error_description') || params.get('error');
  if (providerError) {
    fail(providerError + ' — the link may have expired. Ask for a fresh one.');
    return;
  }

  var token = params.get('access_token');
  if (!token) {
    fail('That link is missing its sign-in details. It may have already been used, or expired.');
    return;
  }

  // Clear the fragment before anything else, so the token is not left sitting in
  // history or in a bookmark.
  history.replaceState(null, '', window.location.pathname);

  fetch('/auth/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: 'access_token=' + encodeURIComponent(token),
    credentials: 'same-origin'
  }).then(function (res) {
    if (res.redirected) { window.location.replace(res.url); return; }
    if (res.ok) { window.location.replace('/'); return; }
    return res.text().then(function (body) {
      fail(body || ('Sign-in failed (' + res.status + ').'));
    });
  }).catch(function () {
    fail('Could not reach the site to finish signing in. Check your connection and tap the link again.');
  });
})();
