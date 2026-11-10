(function () {
  function scrollChat() {
    var body = document.querySelector(".ch-body");
    if (body) {
      body.scrollTop = body.scrollHeight;
    }
  }

  document.addEventListener("htmx:sseMessage", function (ev) {
    var t = ev.target;
    if (t && t.closest && t.closest("#msgs")) {
      scrollChat();
    }
  });

  document.addEventListener("htmx:afterSwap", function (ev) {
    var t = ev.detail && ev.detail.target;
    if (!t) {
      return;
    }
    if (t.id === "msgs" || (t.closest && t.closest("#msgs"))) {
      scrollChat();
    }
  });
})();
