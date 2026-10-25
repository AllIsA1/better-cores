// Copyright (c) 2026 AllIsA1. SPDX-License-Identifier: MIT
(function () {
  'use strict';

  var TEXT = {
    en: {
      title: 'Cores',
      intro:
        'The programs behind the panel’s tunnel protocols: whether each one is in the panel and running, and the last release its project published. Updating them from here is not built yet; other plugins (Better OpenFlux) need this one to be installed and on.',
      colCore: 'Core',
      colPanel: 'In the panel',
      colRunning: 'Running',
      colLatest: 'Last release of the project',
      refresh: 'Refresh',
      present: 'installed',
      missing: 'missing',
      yes: 'yes',
      no: 'no',
      unknown: '—',
      failed: function (e) { return 'Failed: ' + e; },
    },
    ru: {
      title: 'Ядра',
      intro:
        'Программы за туннельными протоколами панели: есть ли каждая в панели, запущена ли, и последний релиз её проекта. Обновление отсюда пока не сделано; другие плагины (Better OpenFlux) требуют, чтобы этот был установлен и включён.',
      colCore: 'Ядро',
      colPanel: 'В панели',
      colRunning: 'Запущено',
      colLatest: 'Последний релиз проекта',
      refresh: 'Обновить',
      present: 'установлено',
      missing: 'нет',
      yes: 'да',
      no: 'нет',
      unknown: '—',
      failed: function (e) { return 'Не удалось: ' + e; },
    },
  };
  var lang = 'en';
  var last = null;

  function T() { return TEXT[lang] || TEXT.en; }
  function $(id) { return document.getElementById(id); }

  function message(text, kind) {
    var m = $('message');
    m.textContent = text || '';
    m.className = 'message' + (kind ? ' ' + kind : '');
  }

  function applyText() {
    document.querySelectorAll('[data-i18n]').forEach(function (n) { n.textContent = T()[n.getAttribute('data-i18n')]; });
    if (last) render(last);
  }

  function cell(tr, text) {
    var td = document.createElement('td');
    td.textContent = text;
    tr.appendChild(td);
    return td;
  }

  function render(list) {
    last = list;
    var t = T();
    var body = $('cores').querySelector('tbody');
    body.textContent = '';
    list.forEach(function (c) {
      var tr = document.createElement('tr');
      cell(tr, c.name);
      cell(tr, c.error ? c.error : c.present ? t.present : t.missing);
      cell(tr, c.error ? t.unknown : c.running ? t.yes : t.no);
      var td = cell(tr, '');
      if (c.latest && c.latest.tag) {
        var when = c.latest.publishedAt ? ' (' + new Date(c.latest.publishedAt).toLocaleDateString() + ')' : '';
        if (/^https:\/\/github\.com\//.test(c.latest.url || '')) {
          var a = document.createElement('a');
          a.href = c.latest.url;
          a.target = '_blank';
          a.rel = 'noopener noreferrer';
          a.textContent = c.latest.tag;
          td.appendChild(a);
          td.appendChild(document.createTextNode(when));
        } else {
          td.textContent = c.latest.tag + when;
        }
      } else {
        td.textContent = t.unknown;
      }
      body.appendChild(tr);
    });
  }

  function load() {
    return lucx.api('/api/status').then(function (res) {
      if (!res.ok) return message(T().failed(res.error || res.status), 'error');
      message('');
      render(res.body);
    });
  }

  $('refresh').onclick = function () { load(); };

  function setLang(ctx) {
    lang = ctx && ctx.lang && String(ctx.lang).slice(0, 2) === 'ru' ? 'ru' : 'en';
    lucx.applyTheme();
    applyText();
  }

  lucx.ready().then(function (ctx) { setLang(ctx); load(); });
  lucx.onContext(setLang);
})();
