(function () {
  function box() {
    return document.querySelector(".ch-body");
  }
  function scroll() {
    var el = box();
    if (el && stick) el.scrollTop = el.scrollHeight;
  }
  var stick = true;
  function nearBottom(el) {
    return el.scrollHeight - el.scrollTop - el.clientHeight < 64;
  }
  document.addEventListener("scroll", function (ev) {
    var el = box();
    if (!el) return;
    if (ev.target === el || ev.target === document) stick = nearBottom(el);
  }, true);
  function pinRetry() {
    var el = box();
    var btns = el && el.querySelectorAll(".calm button, .errbox button");
    var btn = btns && btns.length ? btns[btns.length - 1] : null;
    if (!el || !btn) return;
    var a = el.getBoundingClientRect();
    var b = btn.getBoundingClientRect();
    if (b.bottom > a.bottom) el.scrollTop += Math.ceil(b.bottom - a.bottom);
    b = btn.getBoundingClientRect();
    if (b.top < a.top) el.scrollTop -= Math.ceil(a.top - b.top);
  }
  function settle() {
    scroll();
    pinRetry();
    requestAnimationFrame(function () {
      pinRetry();
      requestAnimationFrame(pinRetry);
    });
  }
  document.addEventListener("htmx:sseMessage", function (ev) {
    var t = ev.target, d = ev.detail || {}, id = d.lastEventId || (d.event && d.event.lastEventId) || "";
    var bubble = t && t.closest ? t.closest(".pending") : null;
    if (bubble && id) {
      bubble.setAttribute("data-seq", id);
      bubble.setAttribute("sse-connect", "/runs/" + bubble.id.slice(4) + "/events?after=" + id);
    }
    if (d.type === "fail") settle();
    else if ((t && t.closest && t.closest("#msgs")) || d.type === "done") scroll();
  });
  document.addEventListener("htmx:afterSettle", function () { pinRetry(); });
  document.addEventListener("htmx:afterSwap", function (ev) {
    var t = ev.detail && ev.detail.target;
    if (t && (t.id === "msgs" || (t.closest && t.closest("#msgs")) || (t.classList && t.classList.contains("errbox")))) settle();
  });
  document.addEventListener("htmx:afterRequest", function (ev) {
    var elt = ev.detail && ev.detail.elt;
    if (elt && elt.id === "composer" && ev.detail.successful) scroll();
  });
  function reconnectNode() {
    var tpl = document.getElementById("act-reconnect");
    return tpl && tpl.content ? tpl.content.querySelector(".act") : null;
  }
  function showReconnect() {
    var bar = document.getElementById("netbar");
    if (bar) bar.hidden = false;
    var sample = reconnectNode();
    var acts = document.querySelectorAll(".pending .act-hold");
    for (var i = 0; i < acts.length; i++) {
      acts[i].hidden = false;
      var act = acts[i].querySelector(".act");
      if (act && sample && !act.getAttribute("data-prev")) {
        act.setAttribute("data-prev", act.innerHTML);
        act.innerHTML = sample.innerHTML;
      }
    }
    var sub = document.querySelector(".ag.on .sub");
    if (sub && sample && !sub.getAttribute("data-prev")) {
      sub.setAttribute("data-prev", sub.innerHTML);
      sub.classList.add("act-sub");
      sub.innerHTML = sample.querySelector(".actico").outerHTML.replace("actico", "actico sm") + "<span>Reconnecting</span>";
    }
  }
  function clearReconnect() {
    var bar = document.getElementById("netbar");
    if (bar) bar.hidden = true;
    var acts = document.querySelectorAll(".pending .act");
    for (var i = 0; i < acts.length; i++) {
      var prev = acts[i].getAttribute("data-prev");
      if (prev) {
        acts[i].innerHTML = prev;
        acts[i].removeAttribute("data-prev");
      }
    }
    var sub = document.querySelector(".ag.on .sub");
    if (sub && sub.getAttribute("data-prev")) {
      sub.innerHTML = sub.getAttribute("data-prev");
      sub.removeAttribute("data-prev");
    }
  }
  document.addEventListener("htmx:sseError", function (ev) {
    var t = ev.target;
    if (t && t.closest && t.closest(".pending")) showReconnect();
  });
  document.addEventListener("htmx:sseOpen", function (ev) {
    var t = ev.target;
    if (t && t.closest && t.closest(".pending")) clearReconnect();
  });
  document.addEventListener("click", function (ev) {
    var b = ev.target && ev.target.closest && ev.target.closest("#netbar-retry");
    if (b) window.location.reload();
  });
  scroll();
  window.addEventListener("load", scroll);
})();
