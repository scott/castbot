(function () {
  function selected() {
    var out = {};
    var inputs = document.querySelectorAll(".hosted-control input[type=radio]");
    for (var i = 0; i < inputs.length; i++) {
      if (inputs[i].checked) out[inputs[i].name] = inputs[i].value;
    }
    return out;
  }

  function match(raw, state) {
    if (!raw) return true;
    var want;
    try {
      want = JSON.parse(raw);
    } catch (e) {
      return true;
    }
    for (var key in want) {
      if (!Object.prototype.hasOwnProperty.call(want, key)) continue;
      if (state[key] !== want[key]) return false;
    }
    return true;
  }

  function apply() {
    var state = selected();
    var nodes = document.querySelectorAll("[data-when]");
    for (var i = 0; i < nodes.length; i++) {
      if (match(nodes[i].getAttribute("data-when"), state)) nodes[i].removeAttribute("hidden");
      else nodes[i].setAttribute("hidden", "");
    }
  }

  document.addEventListener("change", function (ev) {
    var t = ev.target;
    if (!t || !t.closest || !t.closest(".hosted-control")) return;
    apply();
  });
})();
