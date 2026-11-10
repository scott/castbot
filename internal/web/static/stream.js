(function () {
  function scroll() {
    var box = document.querySelector(".ch-body");
    if (box) box.scrollTop = box.scrollHeight;
  }
  document.addEventListener("htmx:sseMessage", function (ev) {
    var t = ev.target, d = ev.detail || {}, id = d.lastEventId || (d.event && d.event.lastEventId) || "";
    var bubble = t && t.classList && t.classList.contains("stream") && t.closest ? t.closest(".pending") : null;
    if (bubble && id) {
      bubble.setAttribute("data-seq", id);
      bubble.setAttribute("sse-connect", "/runs/" + bubble.id.slice(4) + "/events?after=" + id);
    }
    if ((t && t.closest && t.closest("#msgs")) || d.type === "fail" || d.type === "done") scroll();
    if (d.type === "fail") requestAnimationFrame(scroll);
  });
  document.addEventListener("htmx:afterSwap", function (ev) {
    var t = ev.detail && ev.detail.target;
    if (t && (t.id === "msgs" || (t.closest && t.closest("#msgs")) || (t.classList && t.classList.contains("errbox")))) scroll();
  });
  document.addEventListener("htmx:afterRequest", function (ev) {
    var elt = ev.detail && ev.detail.elt;
    if (elt && elt.id === "composer" && ev.detail.successful) scroll();
  });
  scroll();
  window.addEventListener("load", scroll);
})();
