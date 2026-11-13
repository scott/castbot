(function () {
  function box() {
    return document.querySelector(".ch-body");
  }
  function scroll() {
    var el = box();
    if (el && stick) el.scrollTop = el.scrollHeight;
  }
  var scrollQueued = false;
  function scrollSoon() {
    if (scrollQueued) return;
    scrollQueued = true;
    requestAnimationFrame(function () {
      scrollQueued = false;
      scroll();
    });
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
      // A plain id is frame 0. Keep the dot so a reconnect does not skip the rest of the batch.
      var resume = id.indexOf(".") >= 0 ? id : id + ".0";
      bubble.setAttribute("sse-connect", "/runs/" + bubble.id.slice(4) + "/events?after=" + resume);
    }
    if (d.type === "fail") settle();
    else if ((t && t.closest && t.closest("#msgs")) || d.type === "done") scrollSoon();
  });
  document.addEventListener("htmx:afterSettle", function () { pinRetry(); });
  document.addEventListener("htmx:afterSwap", function (ev) {
    var t = ev.detail && ev.detail.target;
    if (!t) return;
    // A live reply swaps several nodes per batch. Scroll once for the frame.
    if (t.id === "msgs" || (t.classList && t.classList.contains("errbox"))) settle();
    else if (t.closest && t.closest("#msgs")) scrollSoon();
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
      if (!acts[i].hasAttribute("data-hold")) {
        acts[i].setAttribute("data-hold", acts[i].hidden ? "1" : "0");
      }
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
        // The label swap target lives inside the row. Restoring the saved
        // markup has to bind that target again.
        if (window.htmx) htmx.process(acts[i]);
      }
    }
    var sub = document.querySelector(".ag.on .sub");
    if (sub && sub.getAttribute("data-prev")) {
      sub.innerHTML = sub.getAttribute("data-prev");
      sub.removeAttribute("data-prev");
    }
    var holds = document.querySelectorAll(".pending .act-hold");
    for (var h = 0; h < holds.length; h++) {
      if (holds[h].getAttribute("data-hold") === "1") holds[h].hidden = true;
      holds[h].removeAttribute("data-hold");
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
