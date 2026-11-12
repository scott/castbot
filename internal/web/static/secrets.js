(function () {
  function joinAnd(names) {
    if (names.length === 0) return "";
    if (names.length === 1) return names[0];
    if (names.length === 2) return names[0] + " and " + names[1];
    return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
  }
  function note() {
    var box = document.querySelector("[data-grant-note]");
    if (!box) return;
    var running = [];
    var paused = [];
    document.querySelectorAll(".gpick input").forEach(function (el) {
      if (!el.checked || !el.getAttribute("data-name")) return;
      if (el.getAttribute("data-paused") === "1") paused.push(el.getAttribute("data-name"));
      else running.push(el.getAttribute("data-name"));
    });
    var parts = [];
    if (running.length) parts.push(joinAnd(running) + " reload to get it. No new machine.");
    if (paused.length === 1) parts.push(paused[0] + " gets it when it wakes.");
    else if (paused.length > 1) parts.push(joinAnd(paused) + " get it when they wake.");
    box.textContent = parts.length ? parts.join(" ") : "No agent gets it yet.";
  }
  document.addEventListener("change", function (e) {
    if (e.target && e.target.closest && e.target.closest(".gpick")) note();
  });
  document.addEventListener("click", function (e) {
    var tab = e.target.closest && e.target.closest("[data-panel-tab]");
    if (tab) {
      e.preventDefault();
      window.location.replace(tab.href);
      return;
    }
    var close = e.target.closest && e.target.closest("[data-close]");
    if (close && window.history.length > 1) {
      e.preventDefault();
      window.history.back();
    }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    var dialog = document.querySelector(".modal[role=dialog], .drawer");
    if (dialog) {
      var cancel = dialog.querySelector("a.btn, a.ibtn");
      if (cancel) window.location.href = cancel.href;
      return;
    }
    var close = document.querySelector("[data-close]");
    if (!close) return;
    if (window.history.length > 1) window.history.back();
    else window.location.href = close.getAttribute("href") || "/";
  });
})();
