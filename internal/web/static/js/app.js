// superkube web — Alpine root + helpers.
//
// Responsibilities:
//   * Wires the global x-data="App()" component on <body>.
//   * Sets the X-CSRF-Token header on every htmx request from the sk_csrf
//     cookie so handlers don't have to repeat the wiring per-page.
//   * Tracks ctx/ns/theme and pushes them into reactive state Alpine reads.
//   * Provides toast(), modal(), confirm() helpers used by all pages.
//   * Powers the exec page: instantiates xterm, opens a WebSocket, pipes
//     stdin/stdout in both directions.

(function () {
  // ---- htmx wiring --------------------------------------------------------

  function readCookie(name) {
    const parts = document.cookie.split('; ');
    for (const p of parts) {
      const eq = p.indexOf('=');
      if (eq < 0) continue;
      if (p.slice(0, eq) === name) return decodeURIComponent(p.slice(eq + 1));
    }
    return '';
  }

  document.addEventListener('htmx:configRequest', function (evt) {
    if (evt.detail.verb === 'get' || evt.detail.verb === 'head') return;
    const csrf = readCookie('sk_csrf');
    if (csrf) evt.detail.headers['X-CSRF-Token'] = csrf;
  });

  // Alpine doesn't observe DOM mutations on its own. Every time HTMX swaps
  // new content in, we need to re-process Alpine directives on the new tree.
  // htmx:load fires for ALL newly added content (initial + every swap).
  document.body.addEventListener('htmx:load', function (evt) {
    if (window.Alpine && evt.target) {
      try { Alpine.initTree(evt.target); } catch (e) { console.warn('Alpine.initTree failed', e); }
    }
    if (window.__currentPath) window.__currentPath();
  });

  // Track global htmx in-flight requests so we can drive a top loading bar
  // and let pages reactively show skeletons.
  document.body.addEventListener('htmx:beforeRequest', () => {
    const app = window.Alpine && Alpine.$data(document.body);
    if (app) app.htmxBusy = true;
  });
  document.body.addEventListener('htmx:afterRequest', () => {
    const app = window.Alpine && Alpine.$data(document.body);
    if (app) setTimeout(() => app.htmxBusy = false, 120);
  });
  document.body.addEventListener('htmx:responseError', () => {
    const app = window.Alpine && Alpine.$data(document.body);
    if (app) app.htmxBusy = false;
  });

  // ---- App component ------------------------------------------------------

  window.App = function () {
    return {
      version: '',
      kubectlVersion: '',
      ai: '',
      ctx: '',
      ns: '',
      ctxOptions: [],
      nsOptions: [],
      banner: { text: '', kind: '' },
      theme: localStorage.getItem('sk-theme') || 'dark',
      path: location.pathname,
      htmxBusy: false,
      paletteOpen: false,

      async init() {
        // CRITICAL: load the dropdown options BEFORE setting ctx/ns. If we
        // set ctx first, the <select>'s x-model tries to sync to a value
        // whose <option> doesn't exist yet — the browser snaps the select
        // to the first option and x-model writes that back, clobbering the
        // real value. Loading options first lets the model→view sync hit a
        // matching <option> on first assignment.
        await this.loadAll();
        window.addEventListener('popstate', () => {
          this.path = location.pathname;
          this.loadRoute(location.pathname);
        });
        window.__currentPath = () => { this.path = location.pathname; };
        this.applyTheme();
        window.addEventListener('keydown', (e) => this.onKey(e));
        // Deep-link routing: the SPA shell handles every URL; on first paint
        // we load the fragment matching the current path so reload + bookmarks
        // land where the user expected.
        this.loadRoute(location.pathname);
      },

      // loadRoute swaps the right HTMX fragment into #main based on path.
      loadRoute(path) {
        const map = [
          [/^\/$/, '/frag/dashboard'],
          [/^\/pods\/([^/]+)\/([^/]+)$/, (m) => `/frag/pod/${m[1]}/${m[2]}`],
          [/^\/pods$/, '/frag/pods'],
          [/^\/resources\/([^/]+)\/([^/]+)\/([^/]+)\/edit$/, (m) => `/frag/resources/${m[1]}/${m[2]}/${m[3]}/edit`],
          [/^\/resources\/([^/]+)$/, (m) => `/frag/resources/${m[1]}`],
          [/^\/apply$/, '/frag/apply'],
          [/^\/logs\/multi$/, '/frag/logs-multi'],
          [/^\/ai$/, '/frag/ai'],
          [/^\/pf$/, '/frag/pf'],
          [/^\/audit$/, '/frag/audit'],
          [/^\/config$/, '/frag/config'],
          [/^\/settings$/, '/frag/settings'],
          [/^\/exec\/([^/]+)\/([^/]+)$/, (m) => `/frag/exec/${m[1]}/${m[2]}`],
        ];
        for (const [re, target] of map) {
          const m = path.match(re);
          if (m) {
            const url = typeof target === 'function' ? target(m) : target;
            htmx.ajax('GET', url, { target: '#main', swap: 'innerHTML' });
            return;
          }
        }
        htmx.ajax('GET', '/frag/dashboard', { target: '#main', swap: 'innerHTML' });
      },

      async loadAll() {
        try {
          const [infoRes, ctxsRes, nsRes] = await Promise.all([
            fetch('/api/v1/info').then(r => r.json()).catch(() => ({})),
            fetch('/api/v1/contexts').then(r => r.json()).catch(() => ({ items: [] })),
            fetch('/api/v1/namespaces').then(r => r.json()).catch(() => ({ items: [] })),
          ]);
          // 1. Options FIRST.
          this.ctxOptions = ctxsRes.items || [];
          this.nsOptions = nsRes.items || [];
          // 2. Static info next.
          this.version = infoRes.version || '';
          this.kubectlVersion = infoRes.kubectl || '';
          this.ai = infoRes.ai || '';
          this.banner = infoRes.banner || { text: '', kind: '' };
          // 3. ctx/ns LAST, after the matching <option> elements exist.
          await this.$nextTick();
          this.ctx = infoRes.context || '';
          this.ns = infoRes.namespace || '';
        } catch (e) {
          console.warn('loadAll failed', e);
        }
      },

      async refreshInfo() {
        try {
          const r = await fetch('/api/v1/info');
          const d = await r.json();
          this.version = d.version || '';
          this.kubectlVersion = d.kubectl || '';
          this.ai = d.ai || '';
          this.banner = d.banner || { text: '', kind: '' };
          await this.$nextTick();
          this.ctx = d.context || '';
          this.ns = d.namespace || '';
        } catch (e) { console.warn('info failed', e); }
      },

      async refreshSwitchers() {
        try {
          const [cr, nr] = await Promise.all([
            fetch('/api/v1/contexts').then(r => r.json()),
            fetch('/api/v1/namespaces').then(r => r.json()),
          ]);
          this.ctxOptions = cr.items || [];
          this.nsOptions = nr.items || [];
        } catch (e) { console.warn('switchers failed', e); }
      },

      async switchContext(name) {
        if (!name || name === this.ctx) return;
        await fetch('/api/v1/contexts/switch', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('sk_csrf') },
          body: JSON.stringify({ context: name }),
        });
        this.ctx = name;
        await this.refreshInfo();
        await this.refreshSwitchers();
        this.toast('Context: ' + name, 'success');
        htmx.ajax('GET', '/frag/dashboard', { target: '#main', push: true });
        history.replaceState({}, '', '/');
      },

      async switchNamespace(name) {
        await fetch('/api/v1/namespaces/switch', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('sk_csrf') },
          body: JSON.stringify({ namespace: name }),
        });
        this.ns = name;
        this.toast('Namespace: ' + (name || 'all'), 'info');
        htmx.ajax('GET', '/frag/pods', { target: '#main', push: true });
      },

      toggleTheme() {
        if (this.theme === 'dark') this.theme = 'light';
        else this.theme = 'dark';
        localStorage.setItem('sk-theme', this.theme);
        this.applyTheme();
      },

      // shortCtx renders the tail of an EKS-style ARN context so the dropdown
      // stays readable. ARNs like
      //   arn:aws:eks:us-east-1:1234:cluster/my-cluster
      // are common; we keep the cluster name and abbreviate the prefix.
      shortCtx(c) {
        if (!c) return '';
        if (c.length <= 36) return c;
        const i = c.lastIndexOf('/');
        if (i >= 0 && i < c.length - 1) return '…/' + c.slice(i + 1);
        return '…' + c.slice(c.length - 32);
      },
      applyTheme() {
        if (this.theme === 'dark') document.body.classList.add('theme-dark');
        else document.body.classList.remove('theme-dark');
        if (this.theme === 'light') document.body.classList.add('theme-light');
        else document.body.classList.remove('theme-light');
      },

      toast(msg, kind) {
        const tray = document.getElementById('toasts');
        if (!tray) return;
        const el = document.createElement('div');
        el.className = 'toast ' + (kind || 'info');
        el.textContent = msg;
        tray.appendChild(el);
        setTimeout(() => el.remove(), 4500);
      },

      openPalette() {
        const verb = prompt('kubectl …');
        if (!verb) return;
        fetch('/api/v1/passthrough', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('sk_csrf') },
          body: JSON.stringify({ argv: verb.split(/\s+/) }),
        }).then(r => r.json()).then(d => {
          alert((d.stdout || '') + (d.stderr ? '\n--\n' + d.stderr : ''));
        });
      },

      onKey(e) {
        // Ctrl/Cmd+K opens command palette.
        if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
          e.preventDefault();
          this.openPalette();
        }
      },
    };
  };

  // ---- Page components: factories Alpine calls from each template -------

  function csrfHeaders() {
    return { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('sk_csrf') };
  }

  function pillForStatus(status) {
    const s = (status || '').toLowerCase();
    if (s === 'running' || s === 'ready' || s === 'true' || s === 'active' || s === 'completed' || s === 'bound' || s === 'succeeded') return 'pill pill-success';
    if (s.includes('pending') || s.includes('waiting') || s.includes('init') || s.includes('terminating') || s.includes('progress')) return 'pill pill-warning';
    if (s.includes('error') || s.includes('crash') || s.includes('fail') || s.includes('imagepull') || s === 'unknown' || s.includes('oomkilled')) return 'pill pill-danger';
    return '';
  }

  window.PodsPage = function () {
    return {
      ns: '', headers: [], rows: [], filter: '', selected: null,
      connected: false, sse: null, loading: true, error: '',
      async init() {
        const app = Alpine.$data(document.body);
        this.ns = app.ns;
        this.loading = true; this.error = '';
        try {
          const r = await fetch('/api/v1/resources/pods');
          if (!r.ok) throw new Error('pods snapshot ' + r.status);
          const d = await r.json();
          this.headers = d.headers || [];
          this.rows = d.rows || [];
        } catch (e) {
          this.error = String(e.message || e);
        } finally {
          this.loading = false;
        }
        this.openStream();
      },
      openStream() {
        if (this.sse) this.sse.close();
        this.sse = new EventSource('/api/v1/stream/watch/pods');
        this.connected = true;
        this.sse.addEventListener('replace', (e) => {
          try {
            const d = JSON.parse(e.data);
            this.headers = d.headers || this.headers;
            this.rows = d.rows || [];
          } catch {}
        });
        this.sse.addEventListener('end', () => { this.connected = false; this.sse.close(); });
        this.sse.onerror = () => { this.connected = false; };
      },
      reload() { this.init(); },
      visible() {
        const f = this.filter.trim().toLowerCase();
        if (!f) return this.rows;
        return this.rows.filter(r => r.some(c => (c || '').toLowerCase().includes(f)));
      },
      pillFor(cell, headerIdx) {
        const hdr = (this.headers[headerIdx] || '').toLowerCase();
        if (hdr === 'status' || hdr === 'state' || hdr === 'phase' || hdr === 'ready') {
          return pillForStatus(cell);
        }
        return '';
      },
      open(row) {
        this.selected = row;
        const name = row[0];
        const targetNs = this.ns || name;
        const podName = this.ns ? name : row[1];
        const podPath = '/pods/' + targetNs + '/' + podName;
        htmx.ajax('GET', '/frag/pod/' + targetNs + '/' + podName, { target: '#main', push: true });
        history.replaceState({}, '', podPath);
      },
    };
  };

  window.ResourcesPage = function (kind) {
    return {
      kind, ns: '', headers: [], rows: [], filter: '',
      connected: false, sse: null, loading: true, error: '',
      async init() {
        const app = Alpine.$data(document.body);
        this.ns = app.ns;
        this.loading = true; this.error = '';
        try {
          const r = await fetch('/api/v1/resources/' + this.kind);
          if (!r.ok) throw new Error('resources ' + r.status);
          const d = await r.json();
          this.headers = d.headers || [];
          this.rows = d.rows || [];
        } catch (e) {
          this.error = String(e.message || e);
        } finally {
          this.loading = false;
        }
        if (this.sse) this.sse.close();
        this.sse = new EventSource('/api/v1/stream/watch/' + this.kind);
        this.connected = true;
        this.sse.addEventListener('replace', (e) => {
          try {
            const d = JSON.parse(e.data);
            this.headers = d.headers; this.rows = d.rows;
          } catch {}
        });
        this.sse.onerror = () => { this.connected = false; };
      },
      reload() { this.init(); },
      visible() {
        const f = this.filter.trim().toLowerCase();
        if (!f) return this.rows;
        return this.rows.filter(r => r.some(c => (c || '').toLowerCase().includes(f)));
      },
      pillFor(cell, headerIdx) {
        const hdr = (this.headers[headerIdx] || '').toLowerCase();
        if (hdr === 'status' || hdr === 'state' || hdr === 'phase' || hdr === 'ready' || hdr === 'available') {
          return pillForStatus(cell);
        }
        return '';
      },
      openYAML(row) {
        const app = Alpine.$data(document.body);
        const name = rowField(this.headers, row, 'name') || row[0];
        const ns = rowField(this.headers, row, 'namespace') || app.ns || '';
        if (!name) return;
        // For editable kinds we route to the inline edit page (textarea +
        // diff preview + confirm). Non-editable kinds still get a YAML
        // toast — the row click stays useful for browsing.
        if (isEditableKind(this.kind)) {
          const path = '/resources/' + this.kind + '/' + (ns || '_') + '/' + name + '/edit';
          htmx.ajax('GET', '/frag/resources/' + this.kind + '/' + (ns || '_') + '/' + name + '/edit',
            { target: '#main', push: true });
          history.replaceState({}, '', path);
          return;
        }
        app.toast('YAML: sk get ' + this.kind + '/' + name + ' -o yaml', 'info');
      },
    };
  };

  // Helpers for the resources table — extract a column by header name.
  function rowField(headers, row, wantHeader) {
    if (!headers || !row) return '';
    const want = String(wantHeader).toLowerCase();
    for (let i = 0; i < headers.length; i++) {
      if (String(headers[i] || '').toLowerCase() === want) return row[i] || '';
    }
    return '';
  }
  function isEditableKind(kind) {
    return ['configmaps', 'configmap', 'cm',
            'secrets', 'secret',
            'ingresses', 'ingress', 'ing',
            'deployments', 'deployment',
            'services', 'service', 'svc'].includes(kind);
  }

  window.ResourceEditPage = function (kind, ns, name) {
    return {
      kind, ns, name,
      // Canonical short name used for kind-specific branches in the template.
      kindCanonical: canonicalKind(kind),
      tab: 'form',     // 'form' | 'yaml'
      editing: false,  // form tab: view vs edit
      reveal: false,   // yaml tab: secret base64 reveal
      revealAll: false, // form tab (secret): show decoded values as text
      form: null,      // structured form payload (per-kind shape)
      originalYAML: '',
      yaml: '',        // textarea-mode buffer (yaml tab)
      diff: '', applyOut: '', confirmToken: '',
      status: 'loading…', statusDetail: '',
      busy: false,

      get isSecret() { return this.kindCanonical === 'secret'; },

      async init() { await this.reload(); },

      // Reload pulls the live object and refreshes both the form and YAML
      // representations. The form is the canonical view; the YAML tab is just
      // a textarea over the same response.
      async reload() {
        this.busy = true; this.diff = ''; this.applyOut = ''; this.confirmToken = '';
        this.status = 'loading…'; this.statusDetail = '';
        try {
          // Form first (also returns yaml). Falls back to a yaml-only flow
          // when the server has no form for this kind.
          const r = await fetch('/api/v1/resources/' + this.kind + '/' + this.ns + '/' + this.name + '/form');
          if (r.ok) {
            const d = await r.json();
            this.form = d.form || null;
            this.originalYAML = d.yaml || '';
            // Initial YAML buffer for the YAML tab is the cluster's truth;
            // secret reveal is handled by a separate fetch in reloadYAML().
            await this.reloadYAML();
            this.status = 'ready';
          } else {
            await this.reloadYAML();
            this.status = r.status === 404 ? 'no form for this kind' : ('load failed: HTTP ' + r.status);
          }
        } catch (e) {
          this.status = 'load failed';
          this.statusDetail = String(e.message || e);
        } finally {
          this.busy = false;
        }
      },

      // reloadYAML re-fetches the YAML tab buffer. Secrets honor the reveal
      // checkbox via ?reveal=1; everything else fetches once.
      async reloadYAML() {
        try {
          let url = '/api/v1/resources/' + this.kind + '/' + this.ns + '/' + this.name + '/yaml';
          if (this.isSecret && this.reveal) url += '?reveal=1';
          const r = await fetch(url);
          this.yaml = await r.text();
        } catch (e) {
          this.statusDetail = String(e.message || e);
        }
      },

      toggleEditMode() {
        this.editing = !this.editing;
        this.diff = ''; this.confirmToken = '';
        this.status = this.editing ? 'editing — make changes, then preview' : 'view only';
      },

      // previewForm posts the structured form to /edit/preview alongside the
      // original yaml so the server can merge before diffing.
      async previewForm() {
        if (!this.form) return;
        this.busy = true; this.diff = ''; this.applyOut = ''; this.confirmToken = '';
        this.status = 'computing diff…'; this.statusDetail = '';
        try {
          const r = await fetch('/api/v1/resources/' + this.kind + '/' + this.ns + '/' + this.name + '/edit/preview', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('sk_csrf') },
            body: JSON.stringify({ form: this.form, original_yaml: this.originalYAML }),
          });
          const d = await r.json();
          if (!r.ok) {
            this.status = 'preview failed';
            this.statusDetail = d.error || d.forbid_reason || ('HTTP ' + r.status);
            return;
          }
          if (d.status === 'no_changes') { this.status = 'no changes'; return; }
          this.diff = d.diff_html || d.diff || '';
          this.confirmToken = d.confirm_token || '';
          this.status = 'review the diff, then confirm';
        } catch (e) {
          this.status = 'preview failed';
          this.statusDetail = String(e.message || e);
        } finally {
          this.busy = false;
        }
      },

      // previewYAML posts the raw textarea contents.
      async previewYAML() {
        this.busy = true; this.diff = ''; this.applyOut = ''; this.confirmToken = '';
        this.status = 'computing diff…'; this.statusDetail = '';
        try {
          const r = await fetch('/api/v1/resources/' + this.kind + '/' + this.ns + '/' + this.name + '/edit/preview', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('sk_csrf') },
            body: JSON.stringify({ yaml: this.yaml }),
          });
          const d = await r.json();
          if (!r.ok) {
            this.status = 'preview failed';
            this.statusDetail = d.error || d.forbid_reason || ('HTTP ' + r.status);
            return;
          }
          if (d.status === 'no_changes') { this.status = 'no changes'; return; }
          this.diff = d.diff_html || d.diff || '';
          this.confirmToken = d.confirm_token || '';
          this.status = 'review the diff, then confirm';
        } catch (e) {
          this.status = 'preview failed';
          this.statusDetail = String(e.message || e);
        } finally {
          this.busy = false;
        }
      },

      // commit consumes the token and applies the YAML or form-derived YAML.
      // The same endpoint accepts either body shape; we pass whichever tab
      // produced the diff.
      async commit() {
        if (!this.confirmToken) return;
        this.busy = true; this.applyOut = ''; this.statusDetail = '';
        const body = (this.tab === 'form')
          ? { form: this.form, original_yaml: this.originalYAML, confirm_token: this.confirmToken }
          : { yaml: this.yaml, confirm_token: this.confirmToken };
        try {
          const r = await fetch('/api/v1/resources/' + this.kind + '/' + this.ns + '/' + this.name + '/edit/commit', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('sk_csrf') },
            body: JSON.stringify(body),
          });
          const d = await r.json();
          this.applyOut = d.output || '';
          if (r.ok && d.status === 'applied') {
            this.status = 'applied';
            this.confirmToken = '';
            // Refresh so the form + YAML match what the cluster actually has.
            setTimeout(() => this.reload(), 200);
          } else {
            this.status = 'apply failed';
            this.statusDetail = d.error || '';
          }
        } catch (e) {
          this.status = 'apply failed';
          this.statusDetail = String(e.message || e);
        } finally {
          this.busy = false;
        }
      },

      cancelPreview() {
        this.diff = ''; this.confirmToken = ''; this.applyOut = '';
        this.status = this.editing ? 'editing' : 'ready';
      },
    };
  };

  function canonicalKind(k) {
    switch (k) {
      case 'cm': case 'configmap': case 'configmaps': return 'configmap';
      case 'secret': case 'secrets': return 'secret';
      case 'ing': case 'ingress': case 'ingresses': return 'ingress';
      case 'deploy': case 'deployment': case 'deployments': return 'deployment';
      case 'svc': case 'service': case 'services': return 'service';
    }
    return k;
  }

  window.PodDetail = function (ns, name) {
    return {
      ns, name, tab: 'logs', text: '', aiText: '', aiError: '', aiBusy: false,
      aiVerb: '', owner: '', loadingText: false,
      follow: true, logFilter: '', sse: null,
      async init() {
        try {
          const r = await fetch('/api/v1/resources/pods/' + ns + '/' + name + '/describe');
          this.owner = ((await r.text()).split('\n').find(l => l.startsWith('Controlled By:')) || '').replace('Controlled By:', '').trim();
        } catch {}
        this.openLogs();
      },
      openLogs() {
        if (this.sse) this.sse.close();
        const u = '/api/v1/stream/logs/' + this.ns + '/' + this.name + '?follow=' + (this.follow ? '1' : '0');
        this.sse = new EventSource(u);
        this.sse.addEventListener('line', (e) => {
          try {
            const d = JSON.parse(e.data);
            const line = (this.logFilter && !d.line.toLowerCase().includes(this.logFilter.toLowerCase())) ? null : d.line;
            if (line == null) return;
            const el = this.$refs.logs; if (!el) return;
            const span = document.createElement('span'); span.className = 'logs-line'; span.textContent = line + '\n';
            el.appendChild(span);
            el.scrollTop = el.scrollHeight;
          } catch {}
        });
      },
      reopen() { this.openLogs(); },
      clearLogs() { if (this.$refs.logs) this.$refs.logs.textContent = ''; },
      async load(tab) {
        this.text = '';
        this.loadingText = true;
        try {
          const r = await fetch('/api/v1/resources/pods/' + this.ns + '/' + this.name + '/' + tab);
          this.text = await r.text();
        } catch (e) {
          this.text = 'load failed: ' + e;
        } finally {
          this.loadingText = false;
        }
      },
      diagnose() {
        this.tab = 'ai'; this.aiText = ''; this.aiError = ''; this.aiVerb = 'Diagnose';
        this.streamAI('/api/v1/ai/diagnose');
      },
      why() {
        this.tab = 'ai'; this.aiText = ''; this.aiError = ''; this.aiVerb = 'Why?';
        this.streamAI('/api/v1/ai/why');
      },
      async streamAI(url) {
        this.aiBusy = true;
        try {
          await streamSSEPost(url, {
            kind: 'pod', name: this.name, namespace: this.ns,
          }, {
            onChunk: (text) => { this.aiText += text; },
            onError: (msg) => { this.aiError = msg; },
          });
        } catch (e) {
          this.aiError = String(e.message || e);
        } finally {
          this.aiBusy = false;
        }
      },
      openExec() {
        htmx.ajax('GET', '/frag/exec/' + this.ns + '/' + this.name, { target: '#main', push: true });
        history.replaceState({}, '', '/exec/' + this.ns + '/' + this.name);
      },
      async askDelete() {
        const r1 = await fetch('/api/v1/destructive/delete', {
          method: 'POST', headers: csrfHeaders(),
          body: JSON.stringify({ kind: 'pod', name: this.name, namespace: this.ns }),
        });
        const d1 = await r1.json();
        if (d1.status === 'blocked') { Alpine.$data(document.body).toast('Forbidden: ' + (d1.forbid_reason || d1.detail), 'error'); return; }
        if (d1.status !== 'needs_confirmation') return;
        const typed = prompt(d1.prompt + ' (expected: ' + d1.expect + ')');
        if (typed == null) return;
        const r2 = await fetch('/api/v1/destructive/delete', {
          method: 'POST', headers: csrfHeaders(),
          body: JSON.stringify({ kind: 'pod', name: this.name, namespace: this.ns, confirm_token: d1.token, confirm_value: typed }),
        });
        const d2 = await r2.json();
        Alpine.$data(document.body).toast(d2.exit_code === 0 ? ('Deleted ' + this.name) : ('Failed: ' + (d2.output || '').slice(0, 200)),
          d2.exit_code === 0 ? 'success' : 'error');
      },
    };
  };

  window.ApplyPage = function () {
    return {
      yaml: '', diff: '', status: '', applyOut: '', confirmToken: '', busy: false,
      init() {},
      async preview() {
        if (!this.yaml.trim()) { this.status = 'paste a manifest first'; return; }
        this.busy = true; this.status = 'computing diff…'; this.diff = ''; this.applyOut = '';
        try {
          const r = await fetch('/api/v1/apply/preview', { method: 'POST', headers: csrfHeaders(), body: JSON.stringify({ yaml: this.yaml }) });
          const d = await r.json();
          if (d.status === 'no_changes') { this.status = 'no differences (cluster matches manifest)'; return; }
          this.diff = d.diff_html || '';
          this.confirmToken = d.confirm_token || '';
          this.status = 'review the diff above, then confirm';
        } catch (e) { this.status = 'preview failed: ' + e; }
        finally { this.busy = false; }
      },
      async commit() {
        this.busy = true; this.applyOut = '';
        try {
          const r = await fetch('/api/v1/apply/commit', { method: 'POST', headers: csrfHeaders(), body: JSON.stringify({ yaml: this.yaml, confirm_token: this.confirmToken }) });
          const d = await r.json();
          this.applyOut = d.output || '';
          this.status = d.status === 'applied' ? 'applied' : 'apply failed';
        } catch (e) { this.status = 'apply failed: ' + e; }
        finally { this.busy = false; }
      },
      reset() { this.diff = ''; this.applyOut = ''; this.confirmToken = ''; this.status = ''; },
    };
  };

  window.MultiLogsPage = function () {
    return {
      target: 'deploy/web', tail: 200, follow: true, sse: null, streaming: false,
      init() {},
      start() {
        if (this.sse) this.sse.close();
        if (this.$refs.out) this.$refs.out.textContent = '';
        const u = '/api/v1/stream/logs-multi?target=' + encodeURIComponent(this.target) +
          '&tail=' + this.tail + '&follow=' + (this.follow ? '1' : '0');
        this.sse = new EventSource(u);
        this.streaming = true;
        this.sse.addEventListener('line', (e) => {
          try {
            const d = JSON.parse(e.data);
            const el = this.$refs.out; if (!el) return;
            const wrap = document.createElement('div'); wrap.className = 'logs-line';
            const tag = document.createElement('span'); tag.className = 'logs-pod'; tag.textContent = '[' + (d.pod || '?') + '] ';
            wrap.appendChild(tag); wrap.appendChild(document.createTextNode(d.line || ''));
            el.appendChild(wrap); el.scrollTop = el.scrollHeight;
          } catch {}
        });
        this.sse.onerror = () => { this.streaming = false; };
      },
      stop() {
        if (this.sse) this.sse.close();
        this.streaming = false;
      },
    };
  };

  window.AIPage = function () {
    return {
      question: '', withContext: true, busy: false, provider: '', answer: '', error: '',
      examples: [
        'Why is my pod in CrashLoopBackOff?',
        'Which Deployments are unhealthy right now?',
        'What does the readiness probe failure mean here?',
        'Summarize the most recent error events in this namespace.',
      ],
      async init() {
        try { const d = await (await fetch('/api/v1/info')).json(); this.provider = d.ai || ''; } catch {}
      },
      useExample(q) { this.question = q; },
      async ask() {
        if (!this.question.trim()) return;
        this.busy = true; this.answer = ''; this.error = '';
        try {
          await streamSSEPost('/api/v1/ai/explain', {
            question: this.question, with_context: this.withContext,
          }, {
            onChunk: (text) => { this.answer += text; },
            onError: (msg) => { this.error = msg; },
          });
        } catch (e) {
          this.error = String(e.message || e);
        } finally {
          this.busy = false;
        }
      },
    };
  };

  window.PFPage = function () {
    return {
      target: 'svc/web', ports: '8080:80', entries: [], showLogs: '', sse: null,
      async init() { await this.reload(); },
      async reload() {
        const d = await (await fetch('/api/v1/portforward')).json();
        this.entries = d.entries || [];
      },
      async start() {
        if (!this.target.trim() || !this.ports.trim()) return;
        const ports = this.ports.split(/\s+/);
        const r = await fetch('/api/v1/portforward', {
          method: 'POST', headers: csrfHeaders(),
          body: JSON.stringify({ target: this.target, ports }),
        });
        if (!r.ok) { Alpine.$data(document.body).toast('start failed', 'error'); return; }
        await this.reload();
        Alpine.$data(document.body).toast('Forward started', 'success');
      },
      async stop(id) {
        await fetch('/api/v1/portforward/' + id, { method: 'DELETE', headers: csrfHeaders() });
        await this.reload();
        Alpine.$data(document.body).toast('Forward stopped', 'info');
      },
      logs(id) {
        this.showLogs = id;
        if (this.sse) this.sse.close();
        if (this.$refs.logout) this.$refs.logout.textContent = '';
        this.sse = new EventSource('/api/v1/stream/portforward/' + id);
        this.sse.addEventListener('line', (e) => {
          try {
            const d = JSON.parse(e.data);
            const el = this.$refs.logout; if (!el) return;
            el.appendChild(document.createTextNode(d.line + '\n'));
            el.scrollTop = el.scrollHeight;
          } catch {}
        });
      },
    };
  };

  window.AuditPage = function () {
    return {
      entries: [], path: '', since: '', verb: '', ctxFilter: '',
      failed: false, follow: false, sse: null, loading: true,
      async init() {
        try {
          const p = await (await fetch('/api/v1/audit/path')).json(); this.path = p.path;
        } catch {}
        await this.reload();
      },
      async reload() {
        this.loading = true;
        try {
          const q = new URLSearchParams();
          if (this.since) q.set('since', this.since);
          if (this.verb) q.set('verb', this.verb);
          if (this.ctxFilter) q.set('context', this.ctxFilter);
          if (this.failed) q.set('failed', '1');
          q.set('last', '200');
          const d = await (await fetch('/api/v1/audit?' + q)).json();
          this.entries = d.entries || [];
        } finally {
          this.loading = false;
        }
      },
      reopen() {
        if (this.sse) this.sse.close();
        if (!this.follow) return;
        this.sse = new EventSource('/api/v1/stream/audit?follow=1');
        this.sse.addEventListener('entry', (e) => {
          try { this.entries.push(JSON.parse(e.data)); } catch {}
        });
      },
    };
  };

  window.ConfigPage = function () {
    return {
      path: '', yaml: '', status: '', loading: true,
      async init() { await this.reload(); },
      async reload() {
        this.loading = true;
        try {
          const d = await (await fetch('/api/v1/config')).json();
          this.path = d.path; this.yaml = d.yaml;
          this.status = 'loaded from disk';
        } finally {
          this.loading = false;
        }
      },
      async save() {
        const r = await fetch('/api/v1/config', { method: 'PUT', headers: csrfHeaders(), body: JSON.stringify({ yaml: this.yaml }) });
        const d = await r.json();
        this.status = r.ok ? 'saved' : ('error: ' + (d.error || 'unknown'));
        if (r.ok) Alpine.$data(document.body).toast('Config saved', 'success');
      },
    };
  };

  window.ExecPage = function (ns, pod, container) {
    return {
      ns, pod, container, session: null,
      init() {
        // Belt-and-braces idempotency: Alpine's auto-init plus any leftover
        // x-init would otherwise create a second xterm canvas in the same
        // #term container. session is set on the first call, so we no-op
        // every subsequent attempt for the lifetime of this component.
        if (this.session) return;
        if (!window.Terminal) {
          const s = document.createElement('script'); s.src = '/static/js/xterm.min.js'; document.head.appendChild(s);
          const f = document.createElement('script'); f.src = '/static/js/xterm-addon-fit.min.js'; document.head.appendChild(f);
          const css = document.createElement('link'); css.rel = 'stylesheet'; css.href = '/static/css/xterm.min.css'; document.head.appendChild(css);
          f.onload = () => {
            if (this.session) return;
            this.session = window.openExecTerminal('term', ns, pod);
          };
          return;
        }
        this.session = window.openExecTerminal('term', ns, pod);
      },
    };
  };

  window.SettingsPage = function () {
    return {
      theme: localStorage.getItem('sk-theme') || 'dark',
      info: {}, upgradeStatus: '', loading: true,
      async init() {
        try {
          this.info = await (await fetch('/api/v1/info')).json();
        } finally {
          this.loading = false;
        }
      },
      apply() {
        localStorage.setItem('sk-theme', this.theme);
        Alpine.$data(document.body).theme = this.theme;
        Alpine.$data(document.body).applyTheme();
        Alpine.$data(document.body).toast('Theme: ' + this.theme, 'success');
      },
      async checkUpgrade() {
        this.upgradeStatus = 'checking…';
        const r = await fetch('/api/v1/upgrade/check');
        const d = await r.json();
        this.upgradeStatus = 'Current: ' + d.current_version + '. ' + (d.note || '');
      },
    };
  };

  window.DashboardPage = function () {
    return {
      loading: true, podStats: null, error: '',
      ctx: '', ns: '', kubectlVersion: '', ai: '',
      async init() {
        this.loading = true;
        // Mirror the App-level state so the template doesn't need to reach
        // through $root (Alpine scopes don't pierce across components).
        const app = Alpine.$data(document.body);
        this.ctx = app.ctx;
        this.ns = app.ns;
        this.kubectlVersion = app.kubectlVersion;
        this.ai = app.ai;
        try {
          const podsRes = await fetch('/api/v1/resources/pods').then(r => r.json()).catch(() => ({ rows: [] }));
          const rows = podsRes.rows || [];
          const headers = podsRes.headers || [];
          this.podStats = this.summarize(rows, headers);
        } catch (e) {
          this.error = String(e.message || e);
        } finally {
          this.loading = false;
        }
      },
      summarize(rows, headers) {
        const statusIdx = headers.findIndex(h => (h || '').toLowerCase() === 'status');
        const total = rows.length;
        let running = 0, warning = 0, failed = 0;
        for (const r of rows) {
          const s = (r[statusIdx] || '').toLowerCase();
          if (s === 'running' || s === 'completed' || s === 'succeeded') running++;
          else if (s.includes('pending') || s.includes('init') || s.includes('terminating')) warning++;
          else failed++;
        }
        return { total, running, warning, failed };
      },
    };
  };

  // ---- SSE helpers --------------------------------------------------------
  //
  // streamSSEPost makes a POST request that the server answers with a
  // text/event-stream body. We read the body manually (so we can carry the
  // CSRF header), parse SSE frames, and dispatch to onChunk/onError. The
  // server emits:
  //   event: chunk   data: {"text":"..."}    one per Write of the AI provider
  //   event: end     data: {"ms":N}          marks completion
  //   event: error   data: {"message":"..."} provider error mid-stream
  //
  // Errors come through TWO paths:
  //   1. HTTP status non-2xx (no AI provider, CSRF reject, etc.) — body is
  //      a plain text/error, not SSE.
  //   2. SSE "error" event mid-stream.
  // Both are reported via onError.
  async function streamSSEPost(url, body, { onChunk, onError, onEnd } = {}) {
    let r;
    try {
      r = await fetch(url, {
        method: 'POST', headers: csrfHeaders(),
        body: JSON.stringify(body),
      });
    } catch (e) {
      if (onError) onError('network error: ' + e.message);
      return;
    }
    if (!r.ok) {
      // Non-SSE error body — read as text and surface to caller.
      let msg = 'HTTP ' + r.status;
      try {
        const txt = await r.text();
        if (txt) msg = txt.trim();
      } catch {}
      if (onError) onError(msg);
      return;
    }
    if (!r.body) {
      if (onError) onError('empty response body');
      return;
    }
    const reader = r.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      // SSE frames are separated by "\n\n".
      let nl;
      while ((nl = buf.indexOf('\n\n')) >= 0) {
        const frame = buf.slice(0, nl);
        buf = buf.slice(nl + 2);
        const ev = parseSSE(frame);
        if (!ev.event && !ev.data) continue;
        if (ev.event === 'chunk' && ev.data) {
          try {
            const j = JSON.parse(ev.data);
            if (j && typeof j.text === 'string' && onChunk) onChunk(j.text);
          } catch {}
        } else if (ev.event === 'error' && ev.data) {
          try {
            const j = JSON.parse(ev.data);
            if (onError) onError(j.message || 'AI error');
          } catch {
            if (onError) onError('AI error');
          }
        } else if (ev.event === 'end') {
          if (onEnd) onEnd();
        }
      }
    }
  }

  // parseSSE turns one event block ("event: x\ndata: y") into { event, data }.
  // Comment lines (": …") and unknown fields are ignored, matching the
  // EventSource spec.
  function parseSSE(block) {
    const out = { event: '', data: '' };
    for (const raw of block.split('\n')) {
      if (!raw || raw.startsWith(':')) continue; // comment / heartbeat
      const colon = raw.indexOf(':');
      let field, value;
      if (colon < 0) { field = raw; value = ''; }
      else { field = raw.slice(0, colon); value = raw.slice(colon + 1); }
      // The spec says one leading space is stripped from the value.
      if (value.startsWith(' ')) value = value.slice(1);
      if (field === 'event') out.event = value;
      else if (field === 'data') out.data += (out.data ? '\n' : '') + value;
    }
    return out;
  }

  // Expose for tests / other handlers.
  window.__sk = { streamSSEPost, parseSSE };

  // ---- exec terminal helper ----------------------------------------------
  window.openExecTerminal = function (containerId, ns, pod) {
    const Term = window.Terminal;
    const Fit = window.FitAddon ? window.FitAddon.FitAddon : null;
    if (!Term) { console.error('xterm not loaded'); return; }
    const el = document.getElementById(containerId);
    if (!el) return;
    // Defensive: if the container has any prior xterm DOM we'd otherwise
    // stack a second terminal on top. Clear before mounting.
    el.replaceChildren();
    const term = new Term({
      fontFamily: 'ui-monospace, "SF Mono", Menlo, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: { background: '#06070d', foreground: '#e6eaf5' },
    });
    const fit = Fit ? new Fit() : null;
    if (fit) term.loadAddon(fit);
    term.open(el);
    if (fit) fit.fit();

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = `${proto}//${location.host}/ws/exec/${ns}/${pod}?cols=${term.cols}&rows=${term.rows}`;
    const ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';

    ws.onopen = function () {
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    };
    ws.onmessage = function (ev) {
      let data = ev.data;
      if (data instanceof ArrayBuffer) data = new TextDecoder().decode(data);
      try {
        const obj = JSON.parse(data);
        if (obj.type === 'output') term.write(obj.data);
        else if (obj.type === 'exit') { term.write('\r\n[connection closed]\r\n'); ws.close(); }
      } catch { term.write(data); }
    };
    ws.onclose = function () { term.write('\r\n[disconnected]\r\n'); };
    term.onData(function (d) { if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'input', data: d })); });

    if (fit) {
      window.addEventListener('resize', function () {
        fit.fit();
        if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      });
    }
    return { term, ws };
  };
})();
