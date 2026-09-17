// Applies the chosen colour theme before the page is drawn, so a forced theme
// does not flash the system one first. app.js owns the selector.
(function () {
  const m = document.cookie.match(/(?:^|;\s*)gwiki-theme=(light|dark)(?:;|$)/);
  if (m) document.documentElement.dataset.theme = m[1];
})();
