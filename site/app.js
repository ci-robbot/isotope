// The "which one do I pick?" guide. Without JavaScript the page shows every
// ISO and the guide stays hidden; this only reads the buttons build.py wrote.
(function () {
  "use strict";
  var form = document.getElementById("picker");
  if (!form) return;
  var desk = form.querySelector(".picker-desk");
  var button = form.querySelector(".pick-button");
  var file = form.querySelector(".pick-file");
  var label = form.querySelector(".pick-label");

  function value(name) {
    var el = form.querySelector('input[name="' + name + '"]:checked');
    return el ? el.value : "";
  }

  function update() {
    var machine = value("machine");
    var server = machine === "server";
    desk.disabled = server;
    var flavor = server ? "server" : value("desktop");
    var line = server ? "full" : machine;

    var cells = document.querySelectorAll(".dl");
    var picked = null;
    for (var i = 0; i < cells.length; i++) {
      var match = cells[i].getAttribute("data-flavor") === flavor &&
        cells[i].getAttribute("data-line") === line;
      cells[i].classList.toggle("is-picked", match);
      if (match) picked = cells[i];
    }
    var link = picked && picked.querySelector(".dl-button");
    if (!link) return;
    button.href = link.href;
    var name = link.querySelector(".dl-file");
    var size = link.getAttribute("data-size");
    var what = link.querySelector(".dl-what");
    label.textContent = what ? what.textContent : "Your ISO";
    button.textContent = size ? "Download, " + size : "Open the release page";
    file.textContent = size && name ? name.textContent : "";
  }

  form.addEventListener("change", update);
  form.addEventListener("submit", function (e) { e.preventDefault(); });
  form.hidden = false;
  update();
})();
