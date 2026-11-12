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
  function wire(root) {
    var box = root.querySelector("[data-create-compose]");
    if (box) {
      var ta = box.querySelector("textarea");
      ta.addEventListener("input", function () { sync(box); });
      sync(box);
      root.querySelectorAll("[data-fill]").forEach(function (chip) {
        chip.addEventListener("click", function () {
          ta.value = chip.getAttribute("data-fill") || "";
          sync(box);
          ta.focus();
        });
      });
    }
    document.querySelectorAll("[data-fill]").forEach(function (chip) {
      if (chip.dataset.wired) return;
      chip.dataset.wired = "1";
      chip.addEventListener("click", function () {
        var form = document.querySelector("#composer textarea");
        if (!form || chip.closest("[data-create-compose]")) return;
        form.value = chip.getAttribute("data-fill") || "";
        form.focus();
      });
    });
  }
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    var a = document.querySelector("a.nclose");
    if (!a) return;
    window.location.assign(a.href);
  });
  document.addEventListener("DOMContentLoaded", function () { wire(document); });
})();
