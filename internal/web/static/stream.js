(function () {
  function box() {
    return document.querySelector(".ch-body");
  }
  function scroll() {
    var el = box();
    if (el) el.scrollTop = el.scrollHeight;
  }
  function pinRetry() {
    var el = box();
    var btns = el && el.querySelectorAll(".errbox button");
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
    var bubble = t && t.classList && t.classList.contains("stream") && t.closest ? t.closest(".pending") : null;
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
  scroll();
  window.addEventListener("load", scroll);
})();
