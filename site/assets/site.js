// btpair site: GitHub links from one <meta>, screen tabs, copy buttons.
(function () {
  var url = (document.querySelector('meta[name="github-url"]') || {}).content || "#";
  document.querySelectorAll(".js-github").forEach(function (a) { a.href = url; });
  document.querySelectorAll(".js-github-clone").forEach(function (el) {
    el.textContent = url.replace(/\/$/, "") + ".git";
  });

  // Tabs (roving focus, arrow keys).
  var tabs = Array.prototype.slice.call(document.querySelectorAll('[role="tab"]'));
  function select(tab, focus) {
    tabs.forEach(function (t) {
      var on = t === tab;
      t.setAttribute("aria-selected", on);
      t.tabIndex = on ? 0 : -1;
      document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
    });
    if (focus) tab.focus();
  }
  tabs.forEach(function (t, i) {
    t.tabIndex = t.getAttribute("aria-selected") === "true" ? 0 : -1;
    t.addEventListener("click", function () { select(t); });
    t.addEventListener("keydown", function (e) {
      var d = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
      if (d) { e.preventDefault(); select(tabs[(i + d + tabs.length) % tabs.length], true); }
    });
  });

  // Copy buttons: copy the code without comment lines.
  document.querySelectorAll(".copy").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var el = document.getElementById(btn.dataset.copyTarget);
      var text = el.innerText.split("\n").filter(function (l) { return !/^\s*#/.test(l); }).join("\n").trim();
      var done = function () {
        btn.textContent = "Copied";
        setTimeout(function () { btn.textContent = "Copy"; }, 1400);
      };
      if (navigator.clipboard) {
        navigator.clipboard.writeText(text).then(done, function () {});
      }
    });
  });
})();
