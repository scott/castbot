(function () {
  function sync(box) {
    var ta = box.querySelector("textarea");
    var btn = box.querySelector("[data-send]");
    if (!ta || !btn) return;
    var empty = !ta.value.trim();
    btn.disabled = empty;
    if (empty) btn.setAttribute("aria-disabled", "true");
    else btn.removeAttribute("aria-disabled");
  }
  function fit(ta) {
    if (!ta) return;
    ta.style.height = "auto";
    var next = ta.scrollHeight;
    if (next > 140) next = 140;
    ta.style.height = next + "px";
  }
  function clearBox(box) {
    var ta = box.querySelector("textarea");
    if (!ta) return;
    ta.value = "";
    ta.style.height = "";
    sync(box);
    try { sessionStorage.removeItem("draft:" + (location.pathname || "")); } catch (e) {}
  }
  function applyChrome() {
    var panel = document.getElementById("nmain");
    var box = document.querySelector("[data-create-compose]");
    if (!panel || !box) return;
    var ta = box.querySelector("textarea");
    var ph = panel.getAttribute("data-placeholder");
    if (ta && ph) ta.setAttribute("placeholder", ph);
  }
  function bindChips() {
    document.querySelectorAll("[data-fill]").forEach(function (chip) {
      if (chip.dataset.wired) return;
      chip.dataset.wired = "1";
      chip.addEventListener("click", function () {
        if (chip.closest("[data-create-compose]")) return;
        var box = document.querySelector("[data-create-compose]");
        var area = box ? box.querySelector("textarea") : document.querySelector("#composer textarea");
        if (!area) return;
        area.value = chip.getAttribute("data-fill") || "";
        if (box) {
          fit(area);
          sync(box);
        }
        area.focus();
      });
    });
  }
  function wire(root) {
    var box = root.querySelector("[data-create-compose]");
    if (box && !box.dataset.wired) {
      box.dataset.wired = "1";
      var ta = box.querySelector("textarea");
      ta.addEventListener("input", function () {
        fit(ta);
        sync(box);
      });
      sync(box);
      if (ta.value) fit(ta);
    }
    bindChips();
  }
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    var a = document.querySelector("a.nclose");
    if (!a) return;
    window.location.assign(a.href);
  });
  document.addEventListener("DOMContentLoaded", function () {
    wire(document);
    document.body.addEventListener("htmx:afterRequest", function (ev) {
      var elt = ev.detail && ev.detail.elt;
      if (!elt || !elt.hasAttribute("data-create-compose") || !ev.detail.successful) return;
      clearBox(elt);
      applyChrome();
    });
    document.body.addEventListener("htmx:afterSwap", function (ev) {
      var target = ev.detail && ev.detail.target;
      if (!target || target.id !== "nmain") return;
      applyChrome();
      bindChips();
    });
  });
})();
