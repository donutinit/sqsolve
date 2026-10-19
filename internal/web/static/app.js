// SPDX-License-Identifier: AGPL-3.0-only
// sqsolve: el único JavaScript del panel. Sin dependencias.
"use strict";

(function () {
  document.addEventListener("DOMContentLoaded", function () {
    // Copy buttons.
    document.querySelectorAll("[data-copy]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        var el = document.querySelector(btn.getAttribute("data-copy"));
        if (!el) return;
        var text = el.textContent.trim();
        var done = function () {
          var old = btn.textContent;
          btn.textContent = "Copiado";
          setTimeout(function () { btn.textContent = old; }, 1500);
        };
        if (navigator.clipboard && window.isSecureContext) {
          navigator.clipboard.writeText(text).then(done, function () { select(el); });
        } else {
          select(el);
        }
      });
    });

    // Typed confirmations enable the dangerous button.
    document.querySelectorAll("[data-confirm-match]").forEach(function (input) {
      var form = input.form;
      var btn = form && form.querySelector("[data-needs-match]");
      if (!btn) return;
      var check = function () { btn.disabled = input.value.trim() !== input.getAttribute("data-confirm-match"); };
      input.addEventListener("input", check);
      check();
    });

    // Simple confirmations.
    document.querySelectorAll("form[data-confirm]").forEach(function (form) {
      form.addEventListener("submit", function (ev) {
        if (!window.confirm(form.getAttribute("data-confirm"))) ev.preventDefault();
      });
    });

    // Slug suggestion from the visible name.
    document.querySelectorAll("[data-slug-source]").forEach(function (input) {
      var target = document.querySelector(input.getAttribute("data-slug-source"));
      if (!target) return;
      var touched = target.value !== "";
      target.addEventListener("input", function () { touched = true; });
      input.addEventListener("input", function () {
        if (!touched) target.value = slugify(input.value);
      });
    });

    document.querySelectorAll("[data-print]").forEach(function (b) {
      b.addEventListener("click", function () { window.print(); });
    });

    // Pages that wait for something (library initialization). With a
    // data-watch URL the page polls the state and only reloads when it
    // changes, so copying values is never interrupted.
    var auto = document.querySelector("[data-autorefresh]");
    if (auto) {
      var secs = parseInt(auto.getAttribute("data-autorefresh"), 10) || 5;
      var watch = auto.getAttribute("data-watch");
      if (watch && "fetch" in window) {
        var was = auto.getAttribute("data-watch-state");
        var poll = function () {
          fetch(watch, { credentials: "same-origin", headers: { "Accept": "application/json" } })
            .then(function (r) { return r.ok ? r.json() : null; })
            .then(function (j) {
              if (j && j.state && j.state !== was) location.reload();
              else setTimeout(poll, secs * 1000);
            }, function () { setTimeout(poll, secs * 1000); });
        };
        setTimeout(poll, secs * 1000);
      } else {
        setTimeout(function () { location.reload(); }, secs * 1000);
      }
    }

    // Live job progress.
    var job = document.querySelector("[data-job]");
    if (job && "EventSource" in window) {
      var state = job.getAttribute("data-job-state");
      if (state === "queued" || state === "running") follow(job);
    }
  });

  function select(el) {
    var range = document.createRange();
    range.selectNodeContents(el);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }

  function slugify(s) {
    var map = { "á": "a", "é": "e", "í": "i", "ó": "o", "ú": "u", "ü": "u", "ñ": "n" };
    s = s.toLowerCase().replace(/[áéíóúüñ]/g, function (c) { return map[c]; });
    s = s.replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "");
    if (s && !/^[a-z]/.test(s)) s = "biblioteca_" + s;
    return s.slice(0, 40).replace(/_+$/, "");
  }

  var stepNames = {};

  function follow(job) {
    var id = job.getAttribute("data-job");
    var bar = job.querySelector("[data-job-bar]");
    var pct = job.querySelector("[data-job-pct]");
    var step = job.querySelector("[data-job-step]");
    var log = document.querySelector("[data-job-log]");
    var es = new EventSource("/tareas/" + id + "/eventos");
    es.addEventListener("state", function (ev) {
      var u = JSON.parse(ev.data);
      if (bar) bar.value = u.progress || 0;
      if (pct) pct.textContent = (u.progress || 0) + " %";
      if (step && u.step) step.textContent = stepNames[u.step] || u.step.replace(/_/g, " ");
      if (log && u.log) {
        var t = new Date(u.log.ts);
        log.textContent += t.toLocaleString() + "  " + u.log.message + "\n";
        log.scrollTop = log.scrollHeight;
      }
    });
    es.addEventListener("done", function () {
      es.close();
      setTimeout(function () { location.reload(); }, 600);
    });
    es.onerror = function () {
      es.close();
      setTimeout(function () { location.reload(); }, 3000);
    };
  }
})();
