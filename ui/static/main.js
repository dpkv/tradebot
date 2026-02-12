(() => {
  const statusEl = document.getElementById('tb-status');
  const table = document.getElementById('tb-jobs-table');
  const tableBody = table ? table.querySelector('tbody') : null;
  const refreshBtn = document.getElementById('tb-refresh');

  /** @type {Array<any>} */
  let currentJobs = [];
  let sortState = {
    key: 'Name',
    dir: 'asc', // 'asc' | 'desc'
  };
  const headerMapping = [
    { index: 0, key: 'Name' },
    { index: 1, key: 'UID' },
    { index: 2, key: 'Type' },
    { index: 3, key: 'State' },
    { index: 4, key: 'ManualFlag' },
  ];
  let headerCells = [];

  function setStatus(message, kind = 'info') {
    if (!statusEl) return;
    statusEl.textContent = message || '';
    statusEl.className = `tb-status tb-status-${kind}`;
  }

  function clearTable() {
    if (!tableBody) return;
    while (tableBody.firstChild) {
      tableBody.removeChild(tableBody.firstChild);
    }
  }

  function compareValues(a, b) {
    if (a == null && b == null) return 0;
    if (a == null) return -1;
    if (b == null) return 1;
    const sa = String(a).toLowerCase();
    const sb = String(b).toLowerCase();
    if (sa < sb) return -1;
    if (sa > sb) return 1;
    return 0;
  }

  function sortJobs() {
    if (!currentJobs || currentJobs.length === 0) return;
    const { key, dir } = sortState;
    const factor = dir === 'asc' ? 1 : -1;
    currentJobs.sort((a, b) => {
      const av = a[key];
      const bv = b[key];
      return compareValues(av, bv) * factor;
    });
  }

  function updateSortIndicators() {
    if (!headerCells.length) return;
    headerCells.forEach((th, idx) => {
      th.classList.remove('tb-sort-asc', 'tb-sort-desc', 'tb-sortable');
      const mapEntry = headerMapping.find((m) => m.index === idx);
      if (!mapEntry) {
        return;
      }
      th.classList.add('tb-sortable');
      if (sortState.key === mapEntry.key) {
        th.classList.add(sortState.dir === 'asc' ? 'tb-sort-asc' : 'tb-sort-desc');
      }
    });
  }

  function renderJobs() {
    clearTable();
    if (!tableBody) return;
    if (!currentJobs || currentJobs.length === 0) {
      setStatus('No jobs found.', 'info');
      return;
    }

    sortJobs();

    for (const job of currentJobs) {
      const tr = document.createElement('tr');

      const name = job.Name || '(unnamed)';
      const uid = job.UID || '';
      const type = job.Type || '';
      const state = job.State || '';
      const manual = job.ManualFlag ? 'Yes' : 'No';

      tr.innerHTML = `
        <td title="${name}">${name}</td>
        <td><code title="${uid}">${uid}</code></td>
        <td>${type}</td>
        <td>${state}</td>
        <td>${manual}</td>
      `;

      tr.classList.add(`tb-state-${state.toLowerCase()}`);
      tableBody.appendChild(tr);
    }

    updateSortIndicators();
  }

  async function loadJobs() {
    setStatus('Loading jobs…', 'info');
    try {
      const resp = await fetch('/trader/job/list', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: '{}',
      });
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(`HTTP ${resp.status}: ${text}`);
      }
      const data = await resp.json();
      currentJobs = data.Jobs || data.jobs || [];
      renderJobs();
      setStatus(`Loaded ${currentJobs.length} job(s).`, 'success');
    } catch (err) {
      console.error('Failed to load jobs', err);
      setStatus(`Failed to load jobs: ${err.message}`, 'error');
    }
  }

  function initSorting() {
    if (!table) return;
    const headers = table.querySelectorAll('thead th');
    headerCells = Array.from(headers);

    headerCells.forEach((th, idx) => {
      const mapEntry = headerMapping.find((m) => m.index === idx);
      if (!mapEntry) return;
      th.addEventListener('click', () => {
        if (sortState.key === mapEntry.key) {
          sortState.dir = sortState.dir === 'asc' ? 'desc' : 'asc';
        } else {
          sortState.key = mapEntry.key;
          sortState.dir = 'asc';
        }
        renderJobs();
      });
    });

    updateSortIndicators();
  }

  if (refreshBtn) {
    refreshBtn.addEventListener('click', () => {
      loadJobs();
    });
  }

  initSorting();
  loadJobs();

  // --- Settings ---
  const MASK = '••••••••';
  const settingsBtn = document.getElementById('tb-settings-btn');
  const settingsBackdrop = document.getElementById('tb-settings-backdrop');
  const settingsClose = document.getElementById('tb-settings-close');
  const settingsSave = document.getElementById('tb-settings-save');
  const confirmBackdrop = document.getElementById('tb-confirm-backdrop');
  const confirmDiff = document.getElementById('tb-confirm-diff');
  const confirmSaveBtn = document.getElementById('tb-confirm-save');
  const confirmCancelBtn = document.getElementById('tb-confirm-cancel-btn');
  const confirmCancelX = document.getElementById('tb-confirm-cancel');
  const savedBackdrop = document.getElementById('tb-saved-backdrop');
  const savedOkBtn = document.getElementById('tb-saved-ok');

  let currentSettings = null;
  let pendingSavePayload = null;

  function showSettingsModal() {
    settingsBackdrop.hidden = false;
    loadSettings();
  }

  function hideSettingsModal() {
    settingsBackdrop.hidden = true;
  }

  function hideConfirmModal() {
    confirmBackdrop.hidden = true;
    pendingSavePayload = null;
  }

  function showSavedModal() {
    if (savedBackdrop) savedBackdrop.hidden = false;
  }

  function hideSavedModal() {
    if (savedBackdrop) savedBackdrop.hidden = true;
  }

  const emptySettings = () => ({
    exchanges: { coinbase: { enabled: false, config: {} }, coinex: { enabled: false, config: {} } },
    notifications: { telegram: { enabled: false, config: {} }, pushover: { enabled: false, config: {} } },
  });

  async function loadSettings() {
    try {
      const resp = await fetch('/api/settings', { method: 'GET' });
      if (resp.status === 404) {
        currentSettings = emptySettings();
        renderSettingsForm(currentSettings);
        return;
      }
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const data = await resp.json();
      currentSettings = data;
      renderSettingsForm(data);
    } catch (err) {
      console.error('Failed to load settings', err);
      if (statusEl) setStatus('Failed to load settings: ' + err.message, 'error');
    }
  }

  function renderSettingsForm(data) {
    const exchanges = data.exchanges || {};
    const notifications = data.notifications || {};
    for (const [key, block] of Object.entries(exchanges)) {
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      if (enableEl) {
        enableEl.checked = !!block.enabled;
        const parentBlock = enableEl.closest('.tb-settings-block');
        if (parentBlock) parentBlock.classList.toggle('tb-enabled', !!block.enabled);
      }
      if (block.config) {
        for (const [field, value] of Object.entries(block.config)) {
          const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
          if (input) input.value = value || '';
        }
      }
    }
    for (const [key, block] of Object.entries(notifications)) {
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      if (enableEl) {
        enableEl.checked = !!block.enabled;
        const parentBlock = enableEl.closest('.tb-settings-block');
        if (parentBlock) parentBlock.classList.toggle('tb-enabled', !!block.enabled);
      }
      if (block.config) {
        for (const [field, value] of Object.entries(block.config)) {
          const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
          if (input) input.value = value || '';
        }
      }
    }
  }

  function collectSettingsPayload() {
    const exchanges = {};
    const notifications = {};
    const exchangeKeys = ['coinbase', 'coinex'];
    const notificationKeys = ['telegram', 'pushover'];
    const fieldMap = {
      coinbase: ['kid', 'pem'],
      coinex: ['key', 'secret'],
      telegram: ['token', 'owner', 'admin', 'others'],
      pushover: ['application_key', 'user_key'],
    };
    for (const key of exchangeKeys) {
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      const enabled = enableEl ? enableEl.checked : false;
      const config = {};
      for (const field of fieldMap[key] || []) {
        const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
        config[field] = input ? input.value.trim() : '';
      }
      exchanges[key] = { enabled, config };
    }
    for (const key of notificationKeys) {
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      const enabled = enableEl ? enableEl.checked : false;
      const config = {};
      for (const field of fieldMap[key] || []) {
        const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
        config[field] = input ? input.value.trim() : '';
      }
      notifications[key] = { enabled, config };
    }
    return { exchanges, notifications };
  }

  const KEY_DISPLAY_NAMES = { coinbase: 'Coinbase', coinex: 'CoinEx', telegram: 'Telegram', pushover: 'Pushover' };
  const FIELD_LABELS = {
    coinbase: { kid: 'Key ID', pem: 'PEM' },
    coinex: { key: 'Key', secret: 'Secret' },
    telegram: { token: 'Bot token', owner: 'Owner ID', admin: 'Admin ID', others: 'Other IDs (comma-separated)' },
    pushover: { application_key: 'Application key', user_key: 'User key' },
  };

  function getDiffLabel(key, field) {
    const keyName = KEY_DISPLAY_NAMES[key] || key;
    if (field === 'enabled') return keyName + ' → Enabled';
    const fieldLabel = (FIELD_LABELS[key] && FIELD_LABELS[key][field]) || field;
    return keyName + ' → ' + fieldLabel;
  }

  function buildDiffLines(oldData, newData) {
    const lines = [];
    const sectionLabels = { exchanges: 'Exchanges', notifications: 'Notifications' };
    for (const [section, sectionLabel] of Object.entries(sectionLabels)) {
      const oldS = oldData[section] || {};
      const newS = newData[section] || {};
      const keys = new Set([...Object.keys(oldS), ...Object.keys(newS)]);
      for (const key of keys) {
        const oldBlock = oldS[key] || { enabled: false, config: {} };
        const newBlock = newS[key] || { enabled: false, config: {} };
        const oldEnabled = !!oldBlock.enabled;
        const newEnabled = !!newBlock.enabled;
        if (oldEnabled !== newEnabled) {
          lines.push({
            label: getDiffLabel(key, 'enabled'),
            oldVal: oldEnabled ? 'enabled' : 'disabled',
            newVal: newEnabled ? 'enabled' : 'disabled',
          });
        }
        const allFields = new Set([...Object.keys(oldBlock.config || {}), ...Object.keys(newBlock.config || {})]);
        for (const field of allFields) {
          const ov = (oldBlock.config && oldBlock.config[field]) || '';
          const nv = (newBlock.config && newBlock.config[field]) || '';
          if (ov === nv) continue;
          if (nv === MASK) continue;
          const displayOld = ov === '' ? '(empty)' : '******';
          const displayNew = nv !== '' ? nv : '(empty)';
          lines.push({
            label: getDiffLabel(key, field),
            oldVal: displayOld,
            newVal: displayNew,
          });
        }
      }
    }
    return lines;
  }

  function showConfirmDialog(oldData, newPayload) {
    const diffLines = buildDiffLines(oldData, newPayload);
    if (diffLines.length === 0) {
      if (statusEl) setStatus('No changes to save.', 'info');
      return;
    }
    confirmDiff.innerHTML = '';
    for (const line of diffLines) {
      const row = document.createElement('div');
      row.className = 'tb-confirm-row';
      row.innerHTML = `<span class="tb-confirm-label">${escapeHtml(line.label)}</span>
        <span class="tb-old-value">${escapeHtml(line.oldVal)}</span>
        <span class="tb-new-value">${escapeHtml(line.newVal)}</span>`;
      confirmDiff.appendChild(row);
    }
    pendingSavePayload = newPayload;
    confirmBackdrop.hidden = false;
  }

  function escapeHtml(s) {
    const div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  // On homepage load: if settings GET returns 404 (secrets not configured), open settings page.
  (async function trySettingsGet() {
    try {
      const resp = await fetch('/api/settings', { method: 'GET' });
      if (resp.status === 404 && settingsBackdrop) {
        settingsBackdrop.hidden = false;
        loadSettings();
      }
    } catch (_) { /* ignore */ }
  })();

  if (settingsBtn) settingsBtn.addEventListener('click', showSettingsModal);
  if (settingsClose) settingsClose.addEventListener('click', hideSettingsModal);
  const settingsModal = document.getElementById('tb-settings-modal');
  if (settingsModal) settingsModal.addEventListener('click', (e) => e.stopPropagation());
  if (settingsSave) settingsSave.addEventListener('click', () => {
    const payload = collectSettingsPayload();
    showConfirmDialog(currentSettings || { exchanges: {}, notifications: {} }, payload);
  });

  confirmBackdrop.addEventListener('click', (e) => {
    if (e.target === confirmBackdrop) hideConfirmModal();
  });
  const confirmModal = document.getElementById('tb-confirm-modal');
  if (confirmModal) {
    confirmModal.addEventListener('click', (e) => e.stopPropagation());
  }
  if (confirmCancelX) confirmCancelX.addEventListener('click', hideConfirmModal);
  if (confirmCancelBtn) confirmCancelBtn.addEventListener('click', hideConfirmModal);
  if (savedBackdrop) savedBackdrop.addEventListener('click', (e) => { if (e.target === savedBackdrop) hideSavedModal(); });
  const savedModal = document.getElementById('tb-saved-modal');
  if (savedModal) savedModal.addEventListener('click', (e) => e.stopPropagation());
  if (savedOkBtn) savedOkBtn.addEventListener('click', hideSavedModal);

  if (confirmSaveBtn) confirmSaveBtn.addEventListener('click', async () => {
    if (!pendingSavePayload) return;
    try {
      const resp = await fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(pendingSavePayload),
      });
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(text || `HTTP ${resp.status}`);
      }
      hideConfirmModal();
      hideSettingsModal();
      loadSettings();
      showSavedModal();
    } catch (err) {
      console.error('Failed to save settings', err);
      if (statusEl) setStatus('Failed to save: ' + err.message, 'error');
    }
  });

  document.querySelectorAll('input[data-enable]').forEach((el) => {
    el.addEventListener('change', () => {
      const block = el.closest('.tb-settings-block');
      if (block) block.classList.toggle('tb-enabled', el.checked);
    });
  });
})();

