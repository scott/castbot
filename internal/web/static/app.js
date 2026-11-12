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

    document.body.addEventListener("htmx:afterRequest", function (ev) {
      var elt = ev.detail && ev.detail.elt;
      if (!elt || elt.id !== "composer" || !ev.detail.successful) return;
      elt.reset();
    });

    var shows = document.querySelectorAll("[data-show-password]");
    for (var s = 0; s < shows.length; s++) {
      shows[s].addEventListener("click", function () {
        var input = document.getElementById(this.getAttribute("data-show-password"));
        if (!input) return;
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
          if (ready) verify.removeAttribute("aria-disabled");
          else verify.setAttribute("aria-disabled", "true");
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
        if (!panel || !otpWrap) return;
        otpWrap.hidden = true;
        panel.hidden = false;
        if (verify) {
          verify.disabled = false;
          verify.classList.remove("dis");
          verify.removeAttribute("aria-disabled");
        }
        var field = panel.querySelector("input");
        if (field) field.focus();
      });
    }

    var clocks = document.querySelectorAll("[data-lock-until]");
    if (clocks.length) {
      var until = Date.parse(clocks[0].getAttribute("data-lock-until"));
      function tick() {
        var ms = until - Date.now();
        if (ms < 0) ms = 0;
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

    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        onEscape(e);
      }
    });

    document.addEventListener("change", function (ev) {
      var t = ev.target;
      if (t && t.name === "on" && t.form && t.form.querySelector(".tog")) {
        t.form.submit();
        return;
      }
      if (!t || t.name !== "preset" || !t.form) {
        return;
      }
      var cron = t.form.querySelector("[name=cron]");
      if (cron && t.getAttribute("data-cron")) {
        cron.value = t.getAttribute("data-cron");
        cron.dispatchEvent(new Event("change", { bubbles: true }));
      }
    });
    document.addEventListener("input", function (ev) {
      var t = ev.target;
      if (!t || t.name !== "cron" || !t.form) {
        return;
      }
      var custom = t.form.querySelector("[name=preset][value=custom]");
      if (custom) {
        custom.checked = true;
      }
    });
    document.body.addEventListener("htmx:afterSwap", function () {
      var prev = document.getElementById("routine-preview");
      var save = document.getElementById("routine-save");
      if (!prev || !save) {
        return;
      }
      var bad = prev.classList.contains("dim");
      save.disabled = bad;
      save.setAttribute("aria-disabled", bad ? "true" : "false");
    });

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

var lastPanelTrigger = null;

function storeGet(key) {
  try {
    return localStorage.getItem(key);
  } catch (err) {
    return null;
  }
}

function storeSet(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch (err) {}
}

function sessionGet(key) {
  try {
    return sessionStorage.getItem(key);
  } catch (err) {
    return null;
  }
}

function sessionSet(key, value) {
  try {
    if (value) {
      sessionStorage.setItem(key, value);
    } else {
      sessionStorage.removeItem(key);
    }
  } catch (err) {}
}

function draftKey() {
  var form = document.getElementById("composer");
  if (!form) {
    return "";
  }
  return "draft:" + (form.getAttribute("data-agent-id") || "");
}

function saveDraft() {
  var form = document.getElementById("composer");
  var key = draftKey();
  if (!form || !key) {
    return;
  }
  var box = form.querySelector("textarea");
  if (!box) {
    return;
  }
  sessionSet(key, box.value);
}

function restoreDraft() {
  var form = document.getElementById("composer");
  var key = draftKey();
  if (!form || !key) {
    return;
  }
  var box = form.querySelector("textarea");
  if (!box || box.value) {
    return;
  }
  var saved = sessionGet(key);
  if (saved) {
    box.value = saved;
  }
}

function threadKey() {
  var form = document.getElementById("composer");
  if (!form) {
    return "";
  }
  var thread = form.querySelector('input[name="thread"]');
  return "scroll:" + (form.getAttribute("data-agent-id") || "") + ":" + (thread ? thread.value : "");
}

function saveScroll() {
  var box = document.getElementById("msgs");
  var key = threadKey();
  if (!box || !key) {
    return;
  }
  sessionSet(key, String(box.scrollTop));
}

function restoreScroll() {
  var box = document.getElementById("msgs");
  var key = threadKey();
  if (!box || !key) {
    return;
  }
  var saved = sessionGet(key);
  if (saved) {
    box.scrollTop = Number(saved);
  }
}

function panelOpen() {
  var panel = document.getElementById("agent-panel");
  return !!(panel && !panel.hidden && panel.classList.contains("s19pane"));
}

function chatURL() {
  var q = new URLSearchParams(location.search);
  q.delete("panel");
  q.delete("routine");
  q.delete("file");
  q.delete("edit");
  q.delete("view");
  q.delete("give");
  q.delete("revoke");
  var next = location.pathname;
  if (q.toString()) {
    next += "?" + q.toString();
  }
  return next;
}

function armPanelHistory() {
  var panel = new URLSearchParams(location.search).get("panel");
  if (!panel || panel === "closed") {
    return;
  }
  if (history.state && history.state.castbotPanel) {
    return;
  }
  var here = location.pathname + location.search + location.hash;
  history.replaceState(null, "", chatURL());
  history.pushState({ castbotPanel: true }, "", here);
}

function bootPanel() {
  var q = new URLSearchParams(location.search);
  if (q.get("panel") === "closed") {
    storeSet("castbot.panel", "closed");
    q.delete("panel");
    var closed = location.pathname + (q.toString() ? "?" + q.toString() : "") + location.hash;
    location.replace(closed);
    return;
  }
  var onAgent = /^\/agents\/[^/]+$/.test(location.pathname);
  var panel = q.get("panel");
  if (onAgent && !panel) {
    var wide = window.matchMedia && window.matchMedia("(min-width: 1280px)").matches;
    var pref = storeGet("castbot.panel");
    if (wide && pref !== "closed") {
      var tab = storeGet("castbot.panelTab") || "agent";
      if (!tab || tab === "closed") {
        tab = "agent";
      }
      window.__castbotOpen = tab;
    }
  }
  if (panel) {
    storeSet("castbot.panel", "open");
    storeSet("castbot.panelTab", panel);
    armPanelHistory();
  }
}

function hidePanel() {
  var panel = document.getElementById("agent-panel");
  if (panel && !panel.hidden) {
    var ph = document.createElement("aside");
    ph.id = "agent-panel";
    ph.hidden = true;
    panel.replaceWith(ph);
  }
  var app = document.querySelector(".app");
  if (app) {
    app.classList.remove("with-pane");
    app.classList.remove("wide-pane");
  }
  document.body.classList.remove("p-slide");
  var main = document.querySelector(".main");
  var sb = document.querySelector(".sb");
  if (main) {
    main.removeAttribute("inert");
  }
  if (sb) {
    sb.removeAttribute("inert");
  }
  var tog = document.querySelector("[data-panel-toggle]");
  if (tog) {
    tog.setAttribute("aria-expanded", "false");
    tog.classList.remove("on");
  }
}

function restoreFocus() {
  var el = lastPanelTrigger;
  if (el && el.isConnected) {
    var box = el.getBoundingClientRect();
    if (box.width > 0 && box.height > 0) {
      el.focus();
      return;
    }
  }
  var area = document.querySelector("#composer textarea");
  if (area) {
    area.focus();
  }
}

function closeAgentPanel() {
  storeSet("castbot.panel", "closed");
  if (history.state && history.state.castbotPanel) {
    history.back();
    return;
  }
  history.replaceState(null, "", chatURL());
  hidePanel();
  restoreFocus();
}

function openDefaultPanel() {
  var tab = window.__castbotOpen;
  if (!tab || !window.htmx || panelOpen()) {
    return;
  }
  var q = new URLSearchParams(location.search);
  if (q.get("panel")) {
    return;
  }
  q.set("panel", tab);
  var url = location.pathname + "?" + q.toString() + location.hash;
  window.htmx.ajax("GET", url, { target: "#agent-panel", swap: "outerHTML" });
  history.pushState({ castbotPanel: true }, "", url);
  storeSet("castbot.panel", "open");
  storeSet("castbot.panelTab", tab);
}

function rememberView() {
  if (location.pathname === "/settings" || location.pathname.indexOf("/settings/") === 0) {
    return;
  }
  sessionSet("castbot.back", location.href);
}

function closeSettings() {
  var back = sessionGet("castbot.back");
  if (back) {
    try {
      if (new URL(back).origin === location.origin && history.length > 1) {
        history.back();
        return;
      }
    } catch (err) {}
  }
  var last = sessionGet("castbot.lastAgent");
  if (last) {
    location.assign("/agents/" + encodeURIComponent(last));
    return;
  }
  location.assign("/");
}

function onEscape(ev) {
  var dialog = document.querySelector(".rmodal:not([hidden]), .modal:not([hidden]), .smodal:not([hidden])");
  if (dialog) {
    var close = dialog.querySelector("[data-close]");
    if (close) {
      close.click();
    }
    ev.preventDefault();
    return;
  }
  if (document.body.classList.contains("p-set") || document.querySelector("[data-sclose]")) {
    if (location.pathname === "/settings" || location.pathname.indexOf("/settings/") === 0) {
      ev.preventDefault();
      closeSettings();
      return;
    }
  }
  if (panelOpen()) {
    ev.preventDefault();
    closeAgentPanel();
  }
}

function syncPanelChrome(panel) {
  var app = document.querySelector(".app");
  var open = panel && !panel.hidden && panel.classList.contains("s19pane");
  if (app) {
    app.classList.toggle("with-pane", !!open);
    app.classList.toggle("wide-pane", !!(open && panel.classList.contains("wide")));
  }
  if (open) {
    document.body.classList.add("p-slide");
  }
  var tog = document.querySelector("[data-panel-toggle]");
  if (tog) {
    tog.setAttribute("aria-expanded", open ? "true" : "false");
    tog.classList.toggle("on", !!open);
  }
  var phone = window.matchMedia && window.matchMedia("(max-width: 760px)").matches;
  var main = document.querySelector(".main");
  var sb = document.querySelector(".sb");
  var dialog = document.querySelector(".rmodal");
  if (phone && open && !dialog) {
    panel.setAttribute("aria-modal", "true");
    if (main) {
      main.setAttribute("inert", "");
    }
    if (sb) {
      sb.setAttribute("inert", "");
    }
  } else if (panel) {
    panel.removeAttribute("aria-modal");
    if (main) {
      main.removeAttribute("inert");
    }
    if (sb) {
      sb.removeAttribute("inert");
    }
  }
}

function focusPanelTab(panel) {
  if (!panel) {
    return;
  }
  var hash = (location.hash || "").replace("#", "");
  if (hash === "replace-open" || hash === "replace-hint") {
    var opener = document.getElementById(hash);
    if (opener) {
      opener.focus();
      return;
    }
  }
  var tab = panel.querySelector(".ptabs a.on") || panel.querySelector(".ptabs a");
  if (tab) {
    tab.focus();
  }
}

function armPanelLinks(root) {
  var panel = (root && root.id === "agent-panel") ? root : document.getElementById("agent-panel");
  if (!panel || !window.htmx) {
    return;
  }
  var links = panel.querySelectorAll("a[href]");
  for (var i = 0; i < links.length; i++) {
    var a = links[i];
    if (a.hasAttribute("data-close-panel") || a.hasAttribute("hx-get")) {
      continue;
    }
    var href = a.getAttribute("href") || "";
    if (href.charAt(0) === "#" || href.indexOf("/agents/") !== 0 || href.indexOf("panel=") < 0) {
      continue;
    }
    a.setAttribute("hx-get", href);
    a.setAttribute("hx-target", "#agent-panel");
    a.setAttribute("hx-swap", "outerHTML");
    a.setAttribute("hx-push-url", "false");
    if (!a.hasAttribute("data-panel-tab")) {
      a.setAttribute("data-panel-nested", "");
    }
    window.htmx.process(a);
  }
}

function armToasts(root) {
  var nodes = (root || document).querySelectorAll(".s19toast");
  for (var i = 0; i < nodes.length; i++) {
    (function (node) {
      if (node.getAttribute("data-armed")) {
        return;
      }
      node.setAttribute("data-armed", "1");
      window.setTimeout(function () {
        node.hidden = true;
      }, 8000);
    })(nodes[i]);
  }
}

function watchSettings() {
  if (location.pathname !== "/settings" || !("IntersectionObserver" in window)) {
    return;
  }
  var chips = document.querySelectorAll(".stoc a");
  if (!chips.length) {
    return;
  }
  var sections = document.querySelectorAll(".set-wrap > section");
  var obs = new IntersectionObserver(function (entries) {
    var best = null;
    for (var i = 0; i < entries.length; i++) {
      if (entries[i].isIntersecting && (!best || entries[i].intersectionRatio >= best.intersectionRatio)) {
        best = entries[i];
      }
    }
    if (!best) {
      return;
    }
    var id = best.target.id || "";
    var heading = best.target.querySelector("h3");
    if (heading && heading.id && id !== "usage") {
      id = heading.id;
    }
    for (var j = 0; j < chips.length; j++) {
      var href = chips[j].getAttribute("href") || "";
      var on = href === "/settings#" + id || href.indexOf("#" + id) === href.length - ("#" + id).length;
      chips[j].classList.toggle("on", on);
      if (on) {
        chips[j].setAttribute("aria-current", "true");
      } else {
        chips[j].removeAttribute("aria-current");
      }
    }
  }, { threshold: [0.3, 0.6] });
  for (var n = 0; n < sections.length; n++) {
    obs.observe(sections[n]);
  }
}

function trapPanelTab(ev) {
  if (ev.key !== "Tab" || !panelOpen()) {
    return;
  }
  if (!(window.matchMedia && window.matchMedia("(max-width: 760px)").matches)) {
    return;
  }
  if (document.querySelector(".rmodal")) {
    return;
  }
  var panel = document.getElementById("agent-panel");
  var items = panel.querySelectorAll("a, button, input, textarea, select");
  var list = [];
  for (var i = 0; i < items.length; i++) {
    if (items[i].disabled) {
      continue;
    }
    var box = items[i].getBoundingClientRect();
    if (box.width < 1 && box.height < 1) {
      continue;
    }
    list.push(items[i]);
  }
  if (!list.length) {
    return;
  }
  var first = list[0];
  var last = list[list.length - 1];
  if (ev.shiftKey && document.activeElement === first) {
    ev.preventDefault();
    last.focus();
  } else if (!ev.shiftKey && document.activeElement === last) {
    ev.preventDefault();
    first.focus();
  }
}

document.addEventListener("input", function (ev) {
  if (ev.target && ev.target.closest && ev.target.closest("#composer")) {
    saveDraft();
  }
});

document.addEventListener("scroll", function (ev) {
  if (ev.target && ev.target.id === "msgs") {
    saveScroll();
  }
}, true);

document.addEventListener("click", function (ev) {
  var opener = ev.target.closest && ev.target.closest("[data-open-panel], [data-panel-toggle], [data-panel-tab]");
  if (opener) {
    lastPanelTrigger = opener;
  }
  var tog = ev.target.closest && ev.target.closest("[data-panel-toggle]");
  if (tog && panelOpen()) {
    ev.preventDefault();
    ev.stopPropagation();
    closeAgentPanel();
    return;
  }
  var shut = ev.target.closest && ev.target.closest("[data-close-panel]");
  if (shut) {
    ev.preventDefault();
    closeAgentPanel();
    return;
  }
  var dismiss = ev.target.closest && ev.target.closest("[data-dismiss-toast]");
  if (dismiss) {
    var toast = dismiss.closest(".s19toast");
    if (toast) {
      toast.hidden = true;
    }
    return;
  }
  var settings = ev.target.closest && ev.target.closest("[data-sclose]");
  if (settings) {
    ev.preventDefault();
    closeSettings();
  }
}, true);

document.addEventListener("keydown", trapPanelTab);

document.addEventListener("DOMContentLoaded", function () {
  openDefaultPanel();
  rememberView();
  restoreDraft();
  restoreScroll();
  var form = document.getElementById("composer");
  if (form && form.getAttribute("data-agent-id")) {
    sessionSet("castbot.lastAgent", form.getAttribute("data-agent-id"));
  }
  var tog = document.querySelector("[data-panel-toggle]");
  if (tog && !panelOpen()) {
    var tab = storeGet("castbot.panelTab") || "agent";
    if (!tab || tab === "closed") {
      tab = "agent";
    }
    var url = new URL(tog.getAttribute("href"), location.origin);
    url.searchParams.set("panel", tab);
    var next = url.pathname + url.search;
    tog.setAttribute("href", next);
    tog.setAttribute("hx-get", next);
  }
  armPanelLinks();
  armToasts();
  syncPanelChrome(document.getElementById("agent-panel"));
  watchSettings();
  if (location.hash === "#replace-open" || location.hash === "#replace-hint") {
    var opener = document.getElementById(location.hash.slice(1));
    if (opener) {
      opener.focus();
    }
  }
  var agent = document.body.getAttribute("data-agent");
  if (agent) {
    window.setInterval(function () {
      fetch("/approvals/live?agent=" + encodeURIComponent(agent), { credentials: "same-origin", headers: { Accept: "text/html" } })
        .then(function (res) { return res.text(); })
        .then(function (html) {
          if (!html || html.indexOf("s19toast") < 0) {
            return;
          }
          var box = document.getElementById("toasts");
          if (!box) {
            return;
          }
          box.insertAdjacentHTML("beforeend", html);
          armToasts(box);
        })
        .catch(function () {});
    }, 8000);
  }
});

document.body && document.body.addEventListener("htmx:afterRequest", function (ev) {
  var elt = ev.detail && ev.detail.elt;
  if (elt && elt.id === "composer" && ev.detail.successful) {
    var key = draftKey();
    if (key) {
      sessionSet(key, "");
    }
  }
});

document.addEventListener("DOMContentLoaded", function () {
  document.body.addEventListener("htmx:configRequest", function (ev) {
    var elt = ev.detail && ev.detail.elt;
    if (!elt) {
      return;
    }
    if (elt.hasAttribute("data-panel-nested")) {
      elt.setAttribute("data-hist-now", "push");
    } else if (elt.hasAttribute("data-panel-tab") || elt.hasAttribute("data-open-panel")) {
      elt.setAttribute("data-hist-now", panelOpen() ? "replace" : "push");
    }
  });
  document.body.addEventListener("htmx:afterSwap", function (ev) {
    var target = ev.detail && ev.detail.target;
    if (!target || target.id !== "agent-panel") {
      return;
    }
    syncPanelChrome(target);
    armPanelLinks(target);
    var elt = ev.detail.requestConfig && ev.detail.requestConfig.elt;
    var mode = elt && elt.getAttribute("data-hist-now");
    var href = elt && (elt.getAttribute("href") || elt.getAttribute("hx-get"));
    if (href && mode === "push") {
      history.pushState({ castbotPanel: true }, "", href);
    } else if (href && mode === "replace") {
      history.replaceState({ castbotPanel: true }, "", href);
    }
    var panelName = new URL(href || location.href, location.origin).searchParams.get("panel");
    if (panelName) {
      storeSet("castbot.panel", "open");
      storeSet("castbot.panelTab", panelName);
    }
    focusPanelTab(target);
  });
  document.body.addEventListener("htmx:afterRequest", function (ev) {
    var elt = ev.detail && ev.detail.elt;
    if (elt && elt.id === "composer" && ev.detail.successful) {
      var key = draftKey();
      if (key) {
        sessionSet(key, "");
      }
    }
  });
});

window.addEventListener("popstate", function () {
  var panel = new URLSearchParams(location.search).get("panel");
  if (!panel) {
    hidePanel();
    restoreFocus();
    return;
  }
  if (window.htmx) {
    window.htmx.ajax("GET", location.pathname + location.search, { target: "#agent-panel", swap: "outerHTML" });
  }
});

bootPanel();
