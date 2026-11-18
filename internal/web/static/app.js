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
      if (t && t.id === "conn-change" && t.checked) {
        window.requestAnimationFrame(function () {
          var token = document.getElementById("conn-token");
          if (token) {
            token.focus();
          }
        });
        return;
      }
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

    var swatches = document.querySelector(".swatches");
    var preview = document.getElementById("color-preview");
    if (swatches && preview) {
      swatches.addEventListener("change", function () {
        var on = swatches.querySelector('input[name="color"]:checked');
        if (!on) return;
        preview.className = "av lg " + on.value;
      });
    }

    document.addEventListener("click", function (ev) {
      var t = ev.target;
      if (!t || !t.closest) return;
      var codeBtn = t.closest("[data-copy='code']");
      if (codeBtn) {
        var fig = codeBtn.closest("figure");
        var pre = fig && fig.querySelector("pre");
        if (pre) copyText(pre.innerText, codeBtn);
        return;
      }
      var msgBtn = t.closest("[aria-label='Copy message as markdown']");
      if (msgBtn && msgBtn.getAttribute("data-md")) {
        copyText(decodeMd(msgBtn.getAttribute("data-md")), msgBtn);
      }
    });

    function decodeMd(value) {
      try {
        var bin = atob(value);
        var bytes = new Uint8Array(bin.length);
        for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        return new TextDecoder().decode(bytes);
      } catch (e) {
        return "";
      }
    }
    function copyText(text, btn) {
      var finish = function () { announceCopy(btn); };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(finish, function () { fallbackCopy(text); finish(); });
      } else {
        fallbackCopy(text);
        finish();
      }
    }
    function fallbackCopy(text) {
      var area = document.createElement("textarea");
      area.value = text;
      document.body.appendChild(area);
      area.select();
      try { document.execCommand("copy"); } catch (e) {}
      document.body.removeChild(area);
    }
    function announceCopy(btn) {
      var live = document.getElementById("copy-status");
      if (live) live.textContent = "Copied";
      if (!btn) return;
      btn.classList.add("done");
      var svg = btn.querySelector("svg");
      var label = btn.querySelector("span");
      var old = svg ? svg.innerHTML : "";
      var oldLabel = label ? label.textContent : "";
      if (svg) {
        svg.innerHTML = '<path d="M4.5 12.5l5 5L19.5 7" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>';
      }
      if (label && oldLabel === "Copy") label.textContent = "Copied";
      window.setTimeout(function () {
        btn.classList.remove("done");
        if (svg) svg.innerHTML = old;
        if (label && oldLabel) label.textContent = oldLabel;
        if (live) live.textContent = "";
      }, 1500);
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

function onGroupChat() {
  return /^\/groups\/[^/]+$/.test(location.pathname);
}

function groupLetter(name) {
  var chars = Array.from(String(name || "").trim());
  if (!chars.length) {
    return "?";
  }
  return chars[0].toUpperCase();
}

function applyGroupTitle(raw, saved) {
  var name = String(raw || "").trim();
  if (!name) {
    name = String(saved || "").trim();
  }
  if (!name) {
    return;
  }
  var letter = groupLetter(name);
  document.querySelectorAll(".s19h .who-btn .nm, .group-pane .pane-h .nm").forEach(function (el) {
    el.textContent = name;
  });
  document.querySelectorAll(".s19h .who-btn .av, .group-pane .pane-h .av").forEach(function (el) {
    el.textContent = letter;
  });
  document.querySelectorAll(".sb a.ag").forEach(function (link) {
    if (link.getAttribute("href") !== location.pathname) {
      return;
    }
    var nm = link.querySelector(".nm");
    var av = link.querySelector(".av");
    if (nm) {
      nm.textContent = name;
    }
    if (av) {
      av.textContent = letter;
    }
  });
  var who = document.querySelector(".s19h .who-btn");
  if (who) {
    who.setAttribute("aria-label", "Open " + name);
  }
  var pane = document.querySelector(".group-pane");
  if (pane) {
    pane.setAttribute("aria-label", name + " panel");
  }
  document.querySelectorAll("[data-group-title]").forEach(function (input) {
    input.value = name;
  });
}

function applyGroupRoster(form) {
  var intro = document.querySelector(".group-intro .sub");
  if (!intro || !form) {
    return;
  }
  var names = [];
  form.querySelectorAll('input[name="agent"]:checked').forEach(function (box) {
    var label = box.closest("label");
    var text = label && label.querySelector(".t1");
    if (text && text.textContent) {
      names.push(text.textContent);
    }
  });
  var people = [];
  form.querySelectorAll('input[name="member"]:checked').forEach(function (box) {
    var label = box.closest("label");
    var text = label && label.querySelector(".t1");
    if (text && text.textContent) {
      people.push(text.textContent);
    }
  });
  if (people.length) {
    var me = document.querySelector(".acct .nm");
    if (me && me.textContent) {
      names.push(me.textContent);
    }
    people.forEach(function (name) {
      names.push(name);
    });
  }
  var who = "";
  if (names.length === 1) {
    who = "With " + names[0] + ".";
  } else if (names.length === 2) {
    who = "With " + names[0] + " and " + names[1] + ".";
  } else if (names.length > 2) {
    who = "With " + names.slice(0, -1).join(", ") + ", and " + names[names.length - 1] + ".";
  }
  var rest = "Agents here can reply. Mention one with @Name when you want that agent to reply.";
  intro.textContent = who ? who + " " + rest : rest;
}

function applyGroupPlace() {
  var pin = document.querySelector('.group-pane form[action$="/pin"] button');
  var sel = document.querySelector('.group-pane select[name="section"]');
  var pinned = !!(pin && pin.textContent.trim() === "Unpin");
  var section = "";
  if (sel && sel.value && sel.value !== "groups" && sel.selectedIndex >= 0) {
    section = sel.options[sel.selectedIndex].text;
  }
  var text = pinned ? "Pinned" : "Group chat";
  if (section) {
    text += " · " + section;
  }
  document.querySelectorAll(".s19h .who-btn .sub, .group-pane .pane-h .sub").forEach(function (el) {
    el.textContent = text;
  });
}

function copyGroupTitle(form) {
  var title = document.querySelector('.group-pane form[action$="/edit"] input[name="title"]');
  if (!title || !form) {
    return;
  }
  form.querySelectorAll("[data-group-title]").forEach(function (input) {
    input.value = title.value;
  });
}

function groupPaneQuery(scope, selector) {
  if (!scope || !scope.querySelector) {
    scope = document;
  }
  if (scope.classList && scope.classList.contains("group-pane")) {
    return scope.querySelector(selector);
  }
  return scope.querySelector(".group-pane " + selector);
}

function bindGroupSetup(root) {
  var scope = root && root.querySelector ? root : document;
  var edit = groupPaneQuery(scope, 'form[action$="/edit"]');
  if (edit && !edit.dataset.bound) {
    edit.dataset.bound = "1";
    var title = edit.querySelector('input[name="title"]');
    var cancel = edit.querySelector("a.btn");
    if (cancel) {
      cancel.addEventListener("mousedown", function () {
        edit.dataset.skip = "1";
      });
    }
    if (title) {
      title.addEventListener("input", function () {
        applyGroupTitle(title.value, title.getAttribute("data-saved") || "");
      });
      title.addEventListener("change", function () {
        window.setTimeout(function () {
          if (edit.dataset.skip) {
            delete edit.dataset.skip;
            return;
          }
          if (title.value.trim() === (title.getAttribute("data-saved") || "").trim()) {
            return;
          }
          edit.requestSubmit();
        }, 0);
      });
    }
    edit.querySelectorAll('input[name="agent"], input[name="member"]').forEach(function (box) {
      box.addEventListener("mousedown", function () {
        edit.dataset.skip = "1";
      });
      box.addEventListener("change", function () {
        applyGroupRoster(edit);
        edit.requestSubmit();
      });
    });
  }
  var forms = [];
  var pinForm = groupPaneQuery(scope, 'form[action$="/pin"]');
  var sectionForm = groupPaneQuery(scope, 'form[action$="/section"]');
  if (pinForm) {
    forms.push(pinForm);
  }
  if (sectionForm) {
    forms.push(sectionForm);
  }
  forms.forEach(function (form) {
    if (form.dataset.bound) {
      return;
    }
    form.dataset.bound = "1";
    form.addEventListener("submit", function () {
      copyGroupTitle(form);
    });
  });
  var section = groupPaneQuery(scope, 'select[name="section"]');
  if (section && !section.dataset.bound) {
    section.dataset.bound = "1";
    section.addEventListener("mousedown", function () {
      if (edit) {
        edit.dataset.skip = "1";
      }
    });
    section.addEventListener("change", function () {
      applyGroupPlace();
      if (section.form) {
        section.form.requestSubmit();
      }
    });
  }
  var pin = pinForm && pinForm.querySelector("button");
  if (pin && !pin.dataset.bound) {
    pin.dataset.bound = "1";
    pin.addEventListener("mousedown", function () {
      if (edit) {
        edit.dataset.skip = "1";
      }
    });
  }
}

function agentPanelTab() {
  var tab = storeGet("castbot.panelTab") || "agent";
  if (!tab || tab === "closed" || tab === "group") {
    return "agent";
  }
  return tab;
}

function panelOpen() {
  var panel = document.getElementById("agent-panel");
  return !!(panel && !panel.hidden && panel.classList.contains("s19pane"));
}

function chatURL() {
  var path = location.pathname;
  var groupForm = path.match(/^\/groups\/([^/]+)\/(?:edit|delete)$/);
  if (groupForm) {
    path = "/groups/" + groupForm[1];
  }
  var q = new URLSearchParams(location.search);
  q.delete("panel");
  q.delete("routine");
  q.delete("file");
  q.delete("edit");
  q.delete("view");
  q.delete("give");
  q.delete("revoke");
  q.delete("confirm");
  var next = path;
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
  var onGroup = onGroupChat();
  var panel = q.get("panel");
  if ((onAgent || onGroup) && !panel) {
    var wide = window.matchMedia && window.matchMedia("(min-width: 1280px)").matches;
    var pref = storeGet("castbot.panel");
    if (wide && pref !== "closed") {
      window.__castbotOpen = onGroup ? "group" : agentPanelTab();
    }
  }
  if (panel) {
    storeSet("castbot.panel", "open");
    if (panel !== "group") {
      storeSet("castbot.panelTab", panel);
    }
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
  if (tab !== "group") {
    storeSet("castbot.panelTab", tab);
  }
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
  var closes = document.querySelectorAll("a.sclose[href]");
  for (var i = 0; i < closes.length; i++) {
    if (closes[i].offsetParent !== null) {
      ev.preventDefault();
      location.assign(closes[i].getAttribute("href"));
      return;
    }
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
    return;
  }
  var items = panel.querySelectorAll("a, button, input, select, textarea");
  for (var i = 0; i < items.length; i++) {
    if (items[i].offsetParent !== null) {
      items[i].focus();
      return;
    }
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
    if (href.charAt(0) === "#" || href.indexOf("panel=") < 0) {
      continue;
    }
    if (href.indexOf("/agents/") !== 0 && href.indexOf("/groups/") !== 0) {
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

var approvalHistoryShown = 0;
var approvalHistoryAgent = "";

function approvalPageSize(list) {
  var n = parseInt(list.getAttribute("data-page") || "8", 10);
  if (!n || n < 1) return 8;
  return n;
}

function rememberApprovalAgent(list) {
  var pane = document.getElementById("approvals-pane");
  var agent = pane ? (pane.getAttribute("data-agent") || "") : "";
  var page = approvalPageSize(list);
  if (agent !== approvalHistoryAgent) {
    approvalHistoryAgent = agent;
    approvalHistoryShown = page;
  }
  if (approvalHistoryShown < page) approvalHistoryShown = page;
  return page;
}

function filterApprovals() {
  var filter = document.getElementById("approval-filter");
  var list = document.getElementById("approval-earlier");
  if (!list) return;
  rememberApprovalAgent(list);
  var q = filter ? (filter.value || "").trim().toLowerCase() : "";
  var rows = list.querySelectorAll("[data-find]");
  var matched = 0;
  var visible = 0;
  for (var i = 0; i < rows.length; i++) {
    var hit = q === "" || (rows[i].getAttribute("data-find") || "").indexOf(q) !== -1;
    if (!hit) {
      rows[i].hidden = true;
      continue;
    }
    matched++;
    if (q === "" && visible >= approvalHistoryShown) {
      rows[i].hidden = true;
      continue;
    }
    rows[i].hidden = false;
    visible++;
  }
  var days = list.querySelectorAll(".appr-day");
  for (var d = 0; d < days.length; d++) {
    var cards = days[d].querySelectorAll("[data-find]");
    var any = false;
    for (var c = 0; c < cards.length; c++) {
      if (!cards[c].hidden) any = true;
    }
    days[d].hidden = !any;
  }
  var empty = document.getElementById("approval-filter-empty");
  if (empty) empty.hidden = q === "" || matched > 0;
  var more = document.getElementById("approval-earlier-more");
  var note = document.getElementById("approval-earlier-count");
  var clipped = q === "" && matched > visible;
  if (more) more.hidden = !clipped;
  if (note) {
    note.hidden = !clipped;
    if (clipped) note.textContent = "Showing " + visible + " of " + matched;
  }
}

document.addEventListener("input", function (ev) {
  if (ev.target && ev.target.closest && ev.target.closest("#composer")) {
    saveDraft();
  }
  if (ev.target && ev.target.id === "approval-filter") filterApprovals();
});

document.addEventListener("search", function (ev) {
  if (ev.target && ev.target.id === "approval-filter") filterApprovals();
});

document.addEventListener("htmx:afterSettle", function () {
  filterApprovals();
});

document.addEventListener("click", function (ev) {
  var more = ev.target && ev.target.closest && ev.target.closest("#approval-earlier-more");
  if (!more) return;
  var list = document.getElementById("approval-earlier");
  if (!list) return;
  ev.preventDefault();
  var page = rememberApprovalAgent(list);
  approvalHistoryShown += page;
  filterApprovals();
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
    var tab = onGroupChat() ? "group" : agentPanelTab();
    var url = new URL(tog.getAttribute("href"), location.origin);
    url.searchParams.set("panel", tab);
    var next = url.pathname + url.search;
    tog.setAttribute("href", next);
    tog.setAttribute("hx-get", next);
  }
  armPanelLinks();
  armToasts();
  filterApprovals();
  syncPanelChrome(document.getElementById("agent-panel"));
  bindGroupSetup(document);
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
    // outerHTML leaves detail.target on the detached placeholder.
    // The pane class is restored on settle, so chrome is synced again then.
    var panel = document.getElementById("agent-panel");
    syncPanelChrome(panel);
    armPanelLinks(panel);
    bindGroupSetup(panel);
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
      if (panelName !== "group") {
        storeSet("castbot.panelTab", panelName);
      }
    }
    focusPanelTab(panel);
  });
  document.body.addEventListener("htmx:afterSettle", function (ev) {
    var target = ev.detail && ev.detail.target;
    if (!target || target.id !== "agent-panel") {
      return;
    }
    syncPanelChrome(document.getElementById("agent-panel"));
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
