(function () {
  var root = document.documentElement;
  var mode = root.getAttribute("data-theme") || "system";

  function systemLight() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches;
  }

  function applyTheme() {
    var light = mode === "light" || (mode === "system" && systemLight());
    root.classList.toggle("light", light);
  }

  applyTheme();
  if (mode === "system" && window.matchMedia) {
    window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", applyTheme);
  }

  document.addEventListener("DOMContentLoaded", function () {
    var search = document.getElementById("search");
    if (search) {
      search.addEventListener("input", function () {
        var q = search.value.trim().toLowerCase();
        var rows = document.querySelectorAll(".ag");
        for (var i = 0; i < rows.length; i++) {
          var name = (rows[i].getAttribute("data-name") || "").toLowerCase();
          rows[i].hidden = q !== "" && name.indexOf(q) === -1;
        }
      });
    }

    document.addEventListener("keydown", function (ev) {
      var tag = document.activeElement && document.activeElement.tagName;
      if (ev.key === "/" && search && tag !== "INPUT" && tag !== "TEXTAREA") {
        ev.preventDefault();
        search.focus();
      }
    });

    var composer = document.getElementById("composer");
    if (composer) {
      var box = composer.querySelector("textarea");
      if (box) {
        box.addEventListener("keydown", function (ev) {
          if (ev.key === "Enter" && !ev.shiftKey) {
            ev.preventDefault();
            if (box.value.trim() !== "") {
              composer.requestSubmit();
            }
          }
        });
      }
    }

    function scrollChat() {
      var box = document.querySelector(".ch-body");
      if (box) box.scrollTop = box.scrollHeight;
    }
    scrollChat();
    window.addEventListener("load", scrollChat);
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(scrollChat);

    document.body.addEventListener("htmx:afterSwap", function (ev) {
      var target = ev.detail && ev.detail.target;
      if (target && target.id === "msgs") scrollChat();
    });
    document.body.addEventListener("htmx:afterRequest", function (ev) {
      var elt = ev.detail && ev.detail.elt;
      if (!elt || elt.id !== "composer" || !ev.detail.successful) return;
      elt.reset();
      scrollChat();
    });

    var shows = document.querySelectorAll("[data-show-password]");
    for (var s = 0; s < shows.length; s++) {
      shows[s].addEventListener("click", function () {
        var input = document.getElementById(this.getAttribute("data-show-password"));
        if (!input) {
          return;
        }
        var show = input.type === "password";
        input.type = show ? "text" : "password";
        this.setAttribute("aria-label", show ? "Hide password" : "Show password");
      });
    }

    var otp = document.getElementById("otp");
    if (otp) {
      var input = otp.querySelector("input");
      var cells = otp.querySelectorAll(".cell");
      var verify = document.getElementById("verify");
      var sent = false;
      function paint() {
        var v = (input.value || "").replace(/\D/g, "").slice(0, 6);
        if (input.value !== v) {
          input.value = v;
        }
        for (var i = 0; i < cells.length; i++) {
          cells[i].textContent = v.charAt(i) || "";
          cells[i].classList.toggle("filled", i < v.length);
          cells[i].classList.toggle("now", i === v.length && v.length < 6);
        }
        if (verify) {
          var ready = v.length === 6;
          verify.disabled = !ready;
          verify.classList.toggle("dis", !ready);
          if (ready) {
            verify.removeAttribute("aria-disabled");
          } else {
            verify.setAttribute("aria-disabled", "true");
          }
        }
      }
      input.addEventListener("keydown", function (ev) {
        if (otp.classList.contains("bad") && ev.key && ev.key.length === 1) {
          input.value = "";
          otp.classList.remove("bad");
        }
      });
      input.addEventListener("input", function () {
        if (otp.classList.contains("bad")) {
          otp.classList.remove("bad");
        }
        paint();
        if (!sent && input.value.length === 6 && verify && verify.form) {
          sent = true;
          verify.form.requestSubmit();
        }
      });
      paint();
      input.focus();
    }

    var recovery = document.getElementById("use-recovery");
    if (recovery) {
      recovery.addEventListener("click", function (ev) {
        ev.preventDefault();
        var panel = document.getElementById("recovery-panel");
        var otpWrap = document.getElementById("otp");
        var verify = document.getElementById("verify");
        if (!panel || !otpWrap) {
          return;
        }
        otpWrap.hidden = true;
        panel.hidden = false;
        if (verify) {
          verify.disabled = false;
          verify.classList.remove("dis");
          verify.removeAttribute("aria-disabled");
        }
        var field = panel.querySelector("input");
        if (field) {
          field.focus();
        }
      });
    }

    var clocks = document.querySelectorAll("[data-lock-until]");
    if (clocks.length) {
      var until = Date.parse(clocks[0].getAttribute("data-lock-until"));
      function tick() {
        var ms = until - Date.now();
        if (ms < 0) {
          ms = 0;
        }
        var total = Math.ceil(ms / 1000);
        var m = Math.floor(total / 60);
        var r = total % 60;
        var text = m + ":" + (r < 10 ? "0" : "") + r;
        for (var i = 0; i < clocks.length; i++) {
          if (clocks[i].hasAttribute("data-lock-button")) {
            clocks[i].textContent = "Sign in again in " + text;
          } else {
            clocks[i].textContent = text;
          }
        }
        if (ms === 0) {
          window.location.reload();
        }
      }
      tick();
      window.setInterval(tick, 1000);
    }

    var themeForm = document.getElementById("theme-form");
    if (themeForm) {
      themeForm.addEventListener("change", function () {
        var picked = themeForm.querySelector("input:checked");
        if (picked) {
          mode = picked.value;
          applyTheme();
        }
        themeForm.requestSubmit();
      });
    }
  });
})();
