(function () {
  var CAP = 32 * 1024 * 1024;
  var queue = [];
  var active = null;
  var uploading = false;

  function csrf() {
    var el = document.querySelector('input[name="csrf"]');
    return el ? el.value : "";
  }

  function mb(n) {
    return (n / (1024 * 1024)).toFixed(1);
  }

  function uploadURL() {
    var root = document.getElementById("files-root");
    if (root && root.getAttribute("data-upload")) return root.getAttribute("data-upload");
    var form = document.getElementById("composer");
    return form ? form.getAttribute("data-upload") : "";
  }

  function agentName() {
    var root = document.getElementById("files-root");
    if (root && root.getAttribute("data-agent")) return root.getAttribute("data-agent");
    var form = document.getElementById("composer");
    return form ? form.getAttribute("data-agent") || "Atlas" : "Atlas";
  }

  function threadID() {
    var root = document.getElementById("files-root");
    if (root && root.getAttribute("data-thread")) return root.getAttribute("data-thread");
    var el = document.querySelector('#composer input[name="thread"]');
    return el ? el.value : "";
  }

  function low() {
    var root = document.getElementById("files-root");
    if (root && root.getAttribute("data-low") === "1") return true;
    var form = document.getElementById("composer");
    return !!(form && form.getAttribute("data-low") === "1");
  }

  function wide() {
    return window.matchMedia && window.matchMedia("(min-width: 761px)").matches;
  }

  function setSending(on) {
    uploading = on;
    var btn = document.getElementById("send-btn");
    var hint = document.getElementById("send-hint");
    if (btn) {
      btn.disabled = on;
      btn.setAttribute("aria-label", on ? "Send, waiting for the upload" : "Send");
    }
    if (hint) {
      hint.textContent = on ? "Send is ready when the upload finishes" : "Enter to send · Shift+Enter for a new line";
    }
  }

  function rowEl(row) {
    var div = document.createElement("div");
    div.className = "fprog" + (row.state === "err" ? " err" : row.state === "wait" ? " wait" : "");
    var icon = row.state === "err" ? "!" : row.state === "wait" ? "…" : row.state === "done" ? "✓" : "↑";
    div.innerHTML = '<span class="fst" aria-hidden="true">' + icon + '</span><div class="grow"><div class="t1"></div><div class="t2 line"></div><div class="fbar"><i></i></div></div>';
    div.querySelector(".t1").textContent = row.name + (row.meta ? " " : "");
    if (row.meta) {
      var meta = document.createElement("span");
      meta.className = "t2";
      meta.textContent = row.meta;
      div.querySelector(".t1").appendChild(document.createTextNode(" "));
      div.querySelector(".t1").appendChild(meta);
    }
    div.querySelector(".line").textContent = row.line || "";
    var bar = div.querySelector(".fbar i");
    if (row.state === "done" || row.state === "err" || row.state === "wait") {
      div.querySelector(".fbar").hidden = row.state !== "up";
    }
    if (typeof row.pct === "number") bar.style.width = row.pct + "%";
    if (row.state === "up") {
      var cancel = document.createElement("button");
      cancel.className = "ibtn";
      cancel.type = "button";
      cancel.setAttribute("aria-label", "Cancel upload");
      cancel.textContent = "Cancel";
      cancel.addEventListener("click", function () {
        if (row.xhr) row.xhr.abort();
      });
      div.appendChild(cancel);
    }
    if (row.state === "err" && row.retry) {
      var retry = document.createElement("button");
      retry.className = "btn sm";
      retry.type = "button";
      retry.textContent = "Retry";
      retry.addEventListener("click", function () {
        enqueueFile(row.retry, "panel");
        div.remove();
      });
      div.appendChild(retry);
    } else if (row.state === "err") {
      var dismiss = document.createElement("button");
      dismiss.className = "ibtn line";
      dismiss.type = "button";
      dismiss.setAttribute("aria-label", "Dismiss " + row.name);
      dismiss.innerHTML = '<svg class="i sm" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';
      dismiss.addEventListener("click", function () { div.remove(); });
      div.appendChild(dismiss);
    }
    row.node = div;
    return div;
  }

  function paintQueue() {
    var box = document.getElementById("fqueue");
    if (!box) return;
    var rows = [];
    if (active && active.where !== "composer-only") rows.push(active);
    for (var i = 0; i < queue.length; i++) {
      if (queue[i].where !== "composer-only") rows.push(queue[i]);
    }
    box.innerHTML = "";
    if (!rows.length) {
      box.hidden = true;
      return;
    }
    box.hidden = false;
    for (var j = 0; j < rows.length; j++) box.appendChild(rowEl(rows[j]));
  }

  function paintChips() {
    var box = document.getElementById("attach-chips");
    if (!box) return;
    var rows = [];
    if (active && (active.where === "composer" || active.where === "composer-only")) rows.push(active);
    for (var i = 0; i < queue.length; i++) {
      if (queue[i].where === "composer" || queue[i].where === "composer-only") rows.push(queue[i]);
    }
    box.innerHTML = "";
    if (!rows.length) {
      box.hidden = true;
      return;
    }
    box.hidden = false;
    for (var j = 0; j < rows.length; j++) {
      var chip = document.createElement("div");
      chip.className = "fout";
      var name = document.createElement("span");
      name.className = "fn";
      name.textContent = rows[j].name;
      var st = document.createElement("span");
      st.className = "st";
      st.textContent = rows[j].state === "wait" ? "Waiting" : rows[j].state === "err" ? "Stopped" : rows[j].state === "done" ? "Ready" : "Uploading";
      chip.appendChild(name);
      chip.appendChild(st);
      box.appendChild(chip);
    }
  }

  function refresh() {
    paintQueue();
    paintChips();
    setSending(!!active || queue.length > 0);
  }

  function enqueueFile(file, where) {
    var item = { file: file, name: file.name, where: where, state: "wait", line: "Waiting", meta: "", pct: 0 };
    if (file.size > CAP) {
      item.state = "err";
      item.line = "Too large. Files can be up to 32 MB. Split it or share a link in the chat instead.";
      item.where = where;
      var box = document.getElementById("fqueue");
      if (box && where !== "composer-only") {
        box.hidden = false;
        box.appendChild(rowEl(item));
      }
      if (where === "composer" || where === "composer-only") {
        var chips = document.getElementById("attach-chips");
        if (chips) {
          chips.hidden = false;
          var chip = document.createElement("div");
          chip.className = "fout";
          chip.textContent = file.name + " · " + item.line;
          chips.appendChild(chip);
        }
      }
      return;
    }
    queue.push(item);
    refresh();
    pump();
  }

  function pump() {
    if (active || !queue.length) return;
    active = queue.shift();
    active.state = "up";
    active.line = mb(0) + " of " + mb(active.file.size) + " MB · keep this tab open";
    refresh();
    sendItem(active);
  }

  function finishActive() {
    active = null;
    if (!queue.length) setSending(false);
    refresh();
    pump();
  }

  function sendItem(item) {
    var url = uploadURL();
    if (!url) {
      item.state = "err";
      item.line = "Stopped at 0.0 of " + mb(item.file.size) + " MB: the connection dropped. Nothing was saved.";
      finishActive();
      return;
    }
    var xhr = new XMLHttpRequest();
    item.xhr = xhr;
    var started = Date.now();
    xhr.open("POST", url);
    xhr.setRequestHeader("X-CSRF-Token", csrf());
    xhr.setRequestHeader("X-File-Name", item.file.name);
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    var thread = threadID();
    if (thread) xhr.setRequestHeader("X-Thread-ID", thread);
    xhr.upload.onprogress = function (ev) {
      var total = ev.lengthComputable ? ev.total : item.file.size;
      var loaded = ev.loaded || 0;
      item.loaded = loaded;
      item.total = total;
      var pct = total ? Math.min(100, Math.round((loaded / total) * 100)) : 0;
      item.pct = pct;
      var secs = 0;
      var dt = (Date.now() - started) / 1000;
      if (dt > 0.2 && loaded > 0 && loaded < total) secs = Math.max(1, Math.round(((total - loaded) / loaded) * dt));
      var left = secs ? " · about " + secs + " s left" : "";
      item.line = mb(loaded) + " of " + mb(total) + " MB" + left + " · keep this tab open";
      item.state = "up";
      refresh();
    };
    xhr.onload = function () {
      if (xhr.status >= 200 && xhr.status < 300) {
        var data = {};
        try { data = JSON.parse(xhr.responseText); } catch (e) { data = {}; }
        item.state = "done";
        item.pct = 100;
        item.line = data.note || data.line || "";
        item.name = data.name || item.name;
        refresh();
        active = null;
        if (!queue.length) setSending(false);
        paintChips();
        pump();
        return;
      }
      item.state = "err";
      item.line = (xhr.responseText || "Stopped").trim();
      item.retry = item.file;
      finishActive();
    };
    xhr.onerror = function () {
      var got = item.loaded || 0;
      var total = item.total || item.file.size;
      item.state = "err";
      item.line = "Stopped at " + mb(got) + " of " + mb(total) + " MB: the connection dropped. Nothing was saved.";
      item.retry = item.file;
      finishActive();
    };
    xhr.onabort = function () {
      active = null;
      refresh();
      pump();
    };
    xhr.send(item.file);
  }

  function takeFiles(list, where) {
    if (!list || low()) return;
    for (var i = 0; i < list.length; i++) enqueueFile(list[i], where);
  }

  window.castbotFiles = {
    enqueue: takeFiles,
    render: function (rows) {
      var box = document.getElementById("fqueue");
      if (!box) return;
      box.hidden = false;
      box.innerHTML = "";
      for (var i = 0; i < rows.length; i++) box.appendChild(rowEl(rows[i]));
    }
  };

  document.addEventListener("DOMContentLoaded", function () {
    var input = document.getElementById("upload-input");
    var btn = document.getElementById("upload-btn");
    var empty = document.getElementById("upload-empty");
    if (btn && input) btn.addEventListener("click", function () { if (!btn.disabled) input.click(); });
    if (empty && input) empty.addEventListener("click", function () { input.click(); });
    if (input) input.addEventListener("change", function () { takeFiles(input.files, "panel"); input.value = ""; });

    var attach = document.getElementById("attach");
    var attachInput = document.getElementById("attach-input");
    if (attach && attachInput) {
      attach.addEventListener("click", function () {
        if (low()) {
          var note = document.getElementById("low-space-note");
          if (note) {
            note.hidden = false;
            note.setAttribute("role", "alert");
          }
          attach.disabled = true;
          attach.classList.add("dis");
          return;
        }
        attachInput.click();
      });
      attachInput.addEventListener("change", function () {
        takeFiles(attachInput.files, "composer");
        attachInput.value = "";
      });
    }

    var composer = document.getElementById("composer");
    if (composer) {
      composer.addEventListener("submit", function (ev) {
        if (uploading || active || queue.length) {
          ev.preventDefault();
          ev.stopImmediatePropagation();
        }
      }, true);
      var box = composer.querySelector("textarea");
      var agent = document.body.getAttribute("data-agent") || "";
      if (!agent) {
        var root = document.getElementById("files-root");
        agent = root ? root.getAttribute("data-agent") : "";
      }
      var key = "draft:" + (location.pathname || agent);
      if (box) {
        var saved = "";
        try { saved = sessionStorage.getItem(key) || ""; } catch (e) { saved = ""; }
        if (saved && !box.value) box.value = saved;
        var hint = document.getElementById("send-hint");
        if (saved && document.body.classList.contains("p-files") && hint && !uploading) hint.textContent = "Draft kept";
        box.addEventListener("input", function () {
          try { sessionStorage.setItem(key, box.value); } catch (e) {}
          if (document.body.classList.contains("p-files") && hint && !uploading && box.value) hint.textContent = "Draft kept";
        });
      }
      document.body.addEventListener("htmx:afterRequest", function (ev) {
        if (!ev.detail || ev.detail.elt !== composer || !ev.detail.successful) return;
        try { sessionStorage.removeItem(key); } catch (e) {}
        if (box) box.value = "";
      });
    }

    document.addEventListener("input", function (ev) {
      var filter = ev.target;
      if (!filter || (filter.id !== "file-filter" && filter.id !== "machine-file-filter")) return;
      var q = (filter.value || "").trim().toLowerCase();
      var root = document;
      if (filter.id === "machine-file-filter") root = document.getElementById("machine-files") || document;
      var rows = root.querySelectorAll(".frow");
      for (var i = 0; i < rows.length; i++) {
        var name = (rows[i].querySelector(".t1") || {}).textContent || "";
        rows[i].hidden = q !== "" && name.toLowerCase().indexOf(q) === -1;
      }
    });

    var mention = document.getElementById("mention-file");
    if (mention) {
      mention.addEventListener("click", function () {
        var area = document.querySelector("#composer textarea");
        if (!area) return;
        var path = mention.getAttribute("data-mention") || "";
        area.value = area.value ? area.value.replace(/\s*$/, " ") + path : path;
        area.focus();
      });
    }
    var copyBtn = document.getElementById("copy-path");
    if (copyBtn) {
      copyBtn.addEventListener("click", function () {
        var path = copyBtn.getAttribute("data-copy") || "";
        if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(path);
      });
    }

    document.addEventListener("click", function (ev) {
      var t = ev.target;
      if (!t || !t.closest) return;
      var a = t.closest("[data-wake]");
      if (!a) return;
      var chip = document.querySelector(".ch-h .chip");
      if (chip) chip.textContent = "Waking";
    });

    var panel = document.getElementById("agent-panel");
    if (panel && !low()) {
      var drop = document.getElementById("fdrop");
      var depth = 0;
      document.addEventListener("dragenter", function (ev) {
        if (!wide() || low()) return;
        depth++;
        if (drop) drop.hidden = false;
        ev.preventDefault();
      });
      document.addEventListener("dragover", function (ev) {
        if (!wide() || low()) return;
        ev.preventDefault();
      });
      document.addEventListener("dragleave", function () {
        depth--;
        if (depth <= 0) {
          depth = 0;
          if (drop) drop.hidden = true;
        }
      });
      document.addEventListener("drop", function (ev) {
        if (!wide() || low()) return;
        ev.preventDefault();
        depth = 0;
        if (drop) drop.hidden = true;
        if (ev.dataTransfer && ev.dataTransfer.files) takeFiles(ev.dataTransfer.files, "panel");
      });
    }

    var search = document.getElementById("search");
    var pop = document.getElementById("search-pop");
    if (search && pop) {
      var timer = 0;
      search.addEventListener("input", function () {
        clearTimeout(timer);
        var q = search.value;
        if (!q.trim()) {
          pop.hidden = true;
          pop.innerHTML = "";
          return;
        }
        timer = setTimeout(function () {
          fetch("/search?q=" + encodeURIComponent(q), { headers: { Accept: "text/html" }, credentials: "same-origin" })
            .then(function (res) { return res.text(); })
            .then(function (html) {
              pop.innerHTML = html;
              pop.hidden = false;
            })
            .catch(function () {});
        }, 150);
      });
      search.addEventListener("keydown", function (ev) {
        if (ev.key === "Enter") {
          var link = pop.querySelector("a.sitem");
          if (link) {
            ev.preventDefault();
            window.location.href = link.getAttribute("href");
          }
        }
        if (ev.key === "Escape" && search.value) {
          ev.preventDefault();
          ev.stopPropagation();
          search.value = "";
          pop.hidden = true;
          pop.innerHTML = "";
        }
      });
    }

    document.addEventListener("keydown", function (ev) {
      if (ev.key !== "Escape") return;
      if (!document.body.classList.contains("p-files")) return;
      if (search && document.activeElement === search && search.value) return;
      var close = document.querySelector('a[aria-label="Close agent panel (Esc)"]') || document.querySelector('a[aria-label="Back to chat"]');
      if (close) window.location.href = close.getAttribute("href");
    });
  });
})();
