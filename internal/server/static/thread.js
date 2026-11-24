// Collapse and expand for the replies of a comment. Without this script the
// buttons stay hidden (the markup says hidden) and every thread stays open.
// It only changes attributes and text of elements already on the page: it
// builds no markup from strings and makes no request, and the fitness rule on
// internal/server/static/** holds it to that.
(function () {
  "use strict";

  function flip(event) {
    var button = event.currentTarget;
    var replies = document.getElementById(button.getAttribute("aria-controls"));
    if (!replies) {
      return;
    }
    var open = button.getAttribute("aria-expanded") === "true";
    replies.hidden = open;
    button.setAttribute("aria-expanded", open ? "false" : "true");
    button.textContent = open ? "Show replies" : "Hide replies";
  }

  var buttons = document.querySelectorAll("button.toggle");
  for (var i = 0; i < buttons.length; i++) {
    buttons[i].hidden = false;
    buttons[i].addEventListener("click", flip);
  }
})();
