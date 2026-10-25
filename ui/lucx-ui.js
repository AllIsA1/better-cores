// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT
//
// lucx-ui.js — the page side of the panel bridge. Copy this file into your plugin's ui/ directory
// and load it with a classic script: <script src="lucx-ui.js"></script>. It adds one global, `lucx`.
(function (window) {
  'use strict';

  var CALL_TIMEOUT_MS = 35000;
  var context = null;
  var listeners = [];
  var pending = {};
  var nextId = 1;
  var resolveReady;
  var readyPromise = new Promise(function (resolve) {
    resolveReady = resolve;
  });

  function post(message) {
    window.parent.postMessage(message, '*');
  }

  function remember(message) {
    context = { pluginId: message.pluginId || (context && context.pluginId) || '', theme: message.theme, lang: message.lang };
    listeners.slice().forEach(function (listener) {
      listener(context);
    });
  }

  window.addEventListener('message', function (event) {
    var message = event.data;
    if (event.source !== window.parent || !message || typeof message !== 'object' || message.lucx !== 1) return;
    if (message.type === 'init' || message.type === 'context') {
      remember(message);
      if (message.type === 'init') resolveReady(context);
    } else if (message.type === 'fetch-result' && Object.prototype.hasOwnProperty.call(pending, message.id)) {
      var call = pending[message.id];
      delete pending[message.id];
      window.clearTimeout(call.timer);
      var result = { ok: message.ok === true, status: message.status };
      if ('body' in message) result.body = message.body;
      if ('error' in message) result.error = message.error;
      call.resolve(result);
    }
  });

  window.lucx = {
    // Resolves once the panel has said who the plugin is and what theme and language are in use.
    ready: function () {
      return readyPromise;
    },
    // The latest { pluginId, theme: { dark, ultra, palette }, lang }, or null before ready().
    context: function () {
      return context;
    },
    // Calls back whenever the theme or language changes. Returns a function that stops it.
    onContext: function (callback) {
      listeners.push(callback);
      return function () {
        listeners = listeners.filter(function (l) {
          return l !== callback;
        });
      };
    },
    // Calls your plugin's own backend: path must start with /api/ (the process sees it as given).
    // Always resolves with { ok, status, body?, error? }; it never calls the panel itself.
    api: function (path, options) {
      if (typeof path !== 'string') return Promise.reject(new TypeError('path must be a string'));
      var opts = options || {};
      var id = nextId++;
      return new Promise(function (resolve) {
        var timer = window.setTimeout(function () {
          delete pending[id];
          resolve({ ok: false, status: 0, error: 'timeout' });
        }, CALL_TIMEOUT_MS);
        pending[id] = { resolve: resolve, timer: timer };
        post({ lucx: 1, type: 'fetch', id: id, method: String(opts.method || 'GET').toUpperCase(), path: path, body: opts.body });
      });
    },
    // Marks the document with the panel's theme so your CSS can follow it: data-theme="dark|light",
    // data-palette and lang. Call it from ready() and onContext().
    applyTheme: function (root) {
      var target = root || window.document.documentElement;
      if (!context) return;
      target.setAttribute('data-theme', context.theme.dark ? 'dark' : 'light');
      target.setAttribute('data-palette', context.theme.palette);
      target.setAttribute('lang', context.lang);
    },
  };

  post({ lucx: 1, type: 'ready' });
})(window);
