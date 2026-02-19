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
    { index: 0,  key: 'Name',        filterType: 'string'  },
    { index: 1,  key: 'Status',      filterType: 'enum'    },
    { index: 2,  key: 'ProductID',   filterType: 'enum'    },
    { index: 3,  key: 'Budget',      filterType: 'numeric' },
    { index: 4,  key: 'Return',      filterType: 'numeric' },
    { index: 5,  key: 'AnnualReturn',filterType: 'numeric' },
    { index: 6,  key: 'Days',        filterType: 'numeric' },
    { index: 7,  key: 'Buys',        filterType: 'numeric' },
    { index: 8,  key: 'Sells',       filterType: 'numeric' },
    { index: 9,  key: 'Profit',      filterType: 'numeric' },
    { index: 10, key: 'Fees',        filterType: 'numeric' },
    { index: 11, key: 'BoughtValue', filterType: 'numeric' },
    { index: 12, key: 'SoldValue',   filterType: 'numeric' },
    { index: 13, key: 'UnsoldValue', filterType: 'numeric' },
    { index: 14, key: 'SoldSize',    filterType: 'numeric' },
    { index: 15, key: 'UnsoldSize',  filterType: 'numeric' },
  ];

  let headerCells = [];

  // --- Filter state ---
  // filters[key] = { type: 'string', value } | { type: 'enum', included: Set } | { type: 'numeric', op, value }
  let filters = {};
  const filterControls = {}; // key -> { input?, numInput?, opSelect?, btn? }

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

  const NUMERIC_KEYS = new Set([
    'Budget', 'Return', 'AnnualReturn', 'Days', 'Buys', 'Sells',
    'Profit', 'Fees', 'BoughtValue', 'SoldValue', 'UnsoldValue', 'SoldSize', 'UnsoldSize',
  ]);

  function parseNumeric(v) {
    if (v == null || v === '' || v === '—') return -Infinity;
    const n = parseFloat(String(v).replace(/[^0-9.+\-eE]/g, ''));
    return isNaN(n) ? -Infinity : n;
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
    const isNumeric = NUMERIC_KEYS.has(key);
    currentJobs.sort((a, b) => {
      const av = a[key];
      const bv = b[key];
      if (isNumeric) {
        return (parseNumeric(av) - parseNumeric(bv)) * factor;
      }
      return compareValues(av, bv) * factor;
    });
  }

  // --- Filtering ---

  function getEnumValues(key) {
    const vals = new Set();
    for (const job of currentJobs) {
      vals.add(String(job[key] != null ? job[key] : ''));
    }
    return vals;
  }

  function isFilterActive(key) {
    const f = filters[key];
    if (!f) return false;
    if (f.type === 'string') return f.value !== '';
    if (f.type === 'enum') {
      const allVals = getEnumValues(key);
      for (const v of allVals) {
        if (!f.included.has(v)) return true;
      }
      return false;
    }
    if (f.type === 'numeric') return f.value !== null && !isNaN(f.value);
    return false;
  }

  function applyFilters(jobs) {
    return jobs.filter((job) => {
      for (const entry of headerMapping) {
        const { key } = entry;
        if (!isFilterActive(key)) continue;
        const f = filters[key];
        if (f.type === 'string') {
          const v = String(job[key] != null ? job[key] : '').toLowerCase();
          if (!v.includes(f.value.toLowerCase())) return false;
        } else if (f.type === 'enum') {
          const v = String(job[key] != null ? job[key] : '');
          if (!f.included.has(v)) return false;
        } else if (f.type === 'numeric') {
          const n = parseNumeric(job[key]);
          const fv = f.value;
          switch (f.op) {
            case '>':  if (!(n >  fv)) return false; break;
            case '>=': if (!(n >= fv)) return false; break;
            case '<':  if (!(n <  fv)) return false; break;
            case '<=': if (!(n <= fv)) return false; break;
            case '=':  if (n !== fv)   return false; break;
            case '!=': if (n === fv)   return false; break;
          }
        }
      }
      return true;
    });
  }

  function removeFilter(key) {
    delete filters[key];
    const ctrl = filterControls[key];
    if (ctrl) {
      if (ctrl.input)    ctrl.input.value = '';
      if (ctrl.numInput) ctrl.numInput.value = '';
      if (ctrl.btn) {
        ctrl.btn.textContent = 'All ▾';
        ctrl.btn.classList.remove('tb-filter-active');
      }
    }
    renderJobs();
  }

  function renderFilterChips() {
    const chipsEl = document.getElementById('tb-filter-chips');
    if (!chipsEl) return;
    chipsEl.innerHTML = '';
    let hasAny = false;
    for (const entry of headerMapping) {
      const { key } = entry;
      if (!isFilterActive(key)) continue;
      hasAny = true;
      const f = filters[key];
      let label = '';
      if (f.type === 'string') {
        label = `${key}: "${f.value}"`;
      } else if (f.type === 'enum') {
        label = `${key}: ${[...f.included].join(', ')}`;
      } else if (f.type === 'numeric') {
        label = `${key} ${f.op} ${f.value}`;
      }
      const chip = document.createElement('span');
      chip.className = 'tb-filter-chip';
      const text = document.createElement('span');
      text.textContent = label;
      const removeBtn = document.createElement('button');
      removeBtn.className = 'tb-filter-chip-remove';
      removeBtn.textContent = '×';
      removeBtn.setAttribute('aria-label', `Remove ${key} filter`);
      removeBtn.addEventListener('click', () => removeFilter(key));
      chip.appendChild(text);
      chip.appendChild(removeBtn);
      chipsEl.appendChild(chip);
    }
    chipsEl.hidden = !hasAny;
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

    renderFilterChips();

    if (!currentJobs || currentJobs.length === 0) {
      setStatus('No jobs found.', 'info');
      return;
    }

    sortJobs();
    const filtered = applyFilters(currentJobs);

    if (filtered.length === 0) {
      const tr = document.createElement('tr');
      const td = document.createElement('td');
      td.colSpan = headerMapping.length;
      td.className = 'tb-no-results';
      td.textContent = 'No jobs match the current filters.';
      tr.appendChild(td);
      tableBody.appendChild(tr);
      updateSortIndicators();
      return;
    }

    for (const job of filtered) {
      const tr = document.createElement('tr');

      const name = job.Name || '(unnamed)';
      const nameTitle = job.UID ? `${name} / ${job.UID}` : name;
      const product = job.ProductID || '';
      const status = job.Status || '';
      const budget = job.HasStatus ? (job.Budget || '') : '—';
      const ret = job.HasStatus ? (job.Return || '') : '—';
      const annualRet = job.HasStatus ? (job.AnnualReturn || '') : '—';
      const days = job.HasStatus ? (job.Days || '') : '—';
      const buys = job.HasStatus ? job.Buys : '—';
      const sells = job.HasStatus ? job.Sells : '—';
      const profit = job.HasStatus ? (job.Profit || '') : '—';
      const fees = job.HasStatus ? (job.Fees || '') : '—';
      const boughtValue = job.HasStatus ? (job.BoughtValue || '') : '—';
      const soldValue = job.HasStatus ? (job.SoldValue || '') : '—';
      const unsoldValue = job.HasStatus ? (job.UnsoldValue || '') : '—';
      const soldSize = job.HasStatus ? (job.SoldSize || '') : '—';
      const unsoldSize = job.HasStatus ? (job.UnsoldSize || '') : '—';

      tr.innerHTML = `
        <td title="${nameTitle}">${name}</td>
        <td>${status}</td>
        <td>${product}</td>
        <td>${budget}</td>
        <td>${ret}</td>
        <td>${annualRet}</td>
        <td>${days}</td>
        <td>${buys}</td>
        <td>${sells}</td>
        <td>${profit}</td>
        <td>${fees}</td>
        <td>${boughtValue}</td>
        <td>${soldValue}</td>
        <td>${unsoldValue}</td>
        <td>${soldSize}</td>
        <td>${unsoldSize}</td>
      `;

      tr.classList.add(`tb-state-${status.toLowerCase()}`);
      tr.classList.add('tb-clickable');
      tr.addEventListener('click', () => {
        window.location.href = `/job?uid=${encodeURIComponent(job.UID)}`;
      });
      tableBody.appendChild(tr);
    }

    updateSortIndicators();
  }

  async function loadJobs() {
    setStatus('Loading jobs…', 'info');
    try {
      const resp = await fetch('/trader/job/status', {
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
      // Refresh enum button labels now that data is available
      for (const entry of headerMapping) {
        if (entry.filterType === 'enum') updateEnumBtn(entry.key);
      }
    } catch (err) {
      console.error('Failed to load jobs', err);
      setStatus(`Failed to load jobs: ${err.message}`, 'error');
    }
  }

  // --- Enum dropdown ---
  let activeEnumKey = null;

  function toggleEnumDropdown(key, btn) {
    const existing = document.getElementById('tb-enum-dropdown');
    if (existing && activeEnumKey === key) {
      existing.remove();
      activeEnumKey = null;
      return;
    }
    if (existing) existing.remove();
    activeEnumKey = key;

    const dropdown = document.createElement('div');
    dropdown.id = 'tb-enum-dropdown';
    dropdown.className = 'tb-enum-dropdown';

    const allVals = getEnumValues(key);
    const f = filters[key];
    const included = f ? f.included : new Set(allVals);

    const sorted = [...allVals].sort();
    if (sorted.length === 0) {
      const msg = document.createElement('span');
      msg.className = 'tb-enum-empty';
      msg.textContent = 'No values';
      dropdown.appendChild(msg);
    }
    for (const val of sorted) {
      const label = document.createElement('label');
      label.className = 'tb-enum-option';
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.value = val;
      cb.checked = included.has(val);
      cb.addEventListener('change', () => onEnumChange(key));
      label.appendChild(cb);
      label.appendChild(document.createTextNode(' ' + (val || '(empty)')));
      dropdown.appendChild(label);
    }

    const rect = btn.getBoundingClientRect();
    dropdown.style.top = `${rect.bottom + 4}px`;
    dropdown.style.left = `${rect.left}px`;
    document.body.appendChild(dropdown);

    requestAnimationFrame(() => {
      document.addEventListener('click', onOutsideEnumClick, { capture: true });
    });
  }

  function onOutsideEnumClick(e) {
    const dropdown = document.getElementById('tb-enum-dropdown');
    if (!dropdown) {
      document.removeEventListener('click', onOutsideEnumClick, { capture: true });
      return;
    }
    if (dropdown.contains(e.target)) return;
    dropdown.remove();
    activeEnumKey = null;
    document.removeEventListener('click', onOutsideEnumClick, { capture: true });
  }

  function onEnumChange(key) {
    const dropdown = document.getElementById('tb-enum-dropdown');
    if (!dropdown) return;
    const checkboxes = dropdown.querySelectorAll('input[type="checkbox"]');
    const included = new Set();
    checkboxes.forEach((cb) => { if (cb.checked) included.add(cb.value); });

    const allVals = getEnumValues(key);
    let allIncluded = true;
    for (const v of allVals) {
      if (!included.has(v)) { allIncluded = false; break; }
    }

    if (allIncluded || included.size === 0) {
      delete filters[key];
    } else {
      filters[key] = { type: 'enum', included };
    }
    updateEnumBtn(key);
    renderJobs();
  }

  function updateEnumBtn(key) {
    const ctrl = filterControls[key];
    if (!ctrl || !ctrl.btn) return;
    if (isFilterActive(key)) {
      ctrl.btn.textContent = `${filters[key].included.size} ▾`;
      ctrl.btn.classList.add('tb-filter-active');
    } else {
      ctrl.btn.textContent = 'All ▾';
      ctrl.btn.classList.remove('tb-filter-active');
    }
  }

  // --- Filter row ---
  function initFilters() {
    if (!table) return;
    const thead = table.querySelector('thead');
    if (!thead) return;
    const filterRow = document.createElement('tr');
    filterRow.className = 'tb-filter-row';

    for (const entry of headerMapping) {
      const td = document.createElement('td');
      td.className = 'tb-filter-cell';

      if (entry.filterType === 'string') {
        const input = document.createElement('input');
        input.type = 'text';
        input.className = 'tb-filter-input';
        input.placeholder = 'Search…';
        input.addEventListener('input', () => {
          const v = input.value;
          if (v) {
            filters[entry.key] = { type: 'string', value: v };
          } else {
            delete filters[entry.key];
          }
          renderJobs();
        });
        filterControls[entry.key] = { input };
        td.appendChild(input);

      } else if (entry.filterType === 'enum') {
        const btn = document.createElement('button');
        btn.className = 'tb-filter-enum-btn';
        btn.textContent = 'All ▾';
        btn.addEventListener('click', (e) => {
          e.stopPropagation();
          toggleEnumDropdown(entry.key, btn);
        });
        filterControls[entry.key] = { btn };
        td.appendChild(btn);

      } else if (entry.filterType === 'numeric') {
        const wrap = document.createElement('div');
        wrap.className = 'tb-filter-num-wrap';

        const opSelect = document.createElement('select');
        opSelect.className = 'tb-filter-select';
        for (const op of ['>', '>=', '<', '<=', '=', '!=']) {
          const opt = document.createElement('option');
          opt.value = op;
          opt.textContent = op;
          opSelect.appendChild(opt);
        }

        const numInput = document.createElement('input');
        numInput.type = 'number';
        numInput.className = 'tb-filter-num';
        numInput.placeholder = '#';

        const onChange = () => {
          const v = numInput.value;
          if (v !== '') {
            filters[entry.key] = { type: 'numeric', op: opSelect.value, value: parseFloat(v) };
          } else {
            delete filters[entry.key];
          }
          renderJobs();
        };
        numInput.addEventListener('input', onChange);
        opSelect.addEventListener('change', () => {
          if (filters[entry.key]) {
            filters[entry.key].op = opSelect.value;
            renderJobs();
          }
        });

        filterControls[entry.key] = { numInput, opSelect };
        wrap.appendChild(opSelect);
        wrap.appendChild(numInput);
        td.appendChild(wrap);
      }

      filterRow.appendChild(td);
    }

    thead.appendChild(filterRow);
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
  initFilters();
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
  const errorBackdrop = document.getElementById('tb-error-backdrop');
  const errorMessageEl = document.getElementById('tb-error-message');
  const errorOkBtn = document.getElementById('tb-error-ok');
  const errorCloseBtn = document.getElementById('tb-error-close');

  let currentSettings = null;
  let pendingSavePayload = null;
  let settingsForceShown = false; // true when opened due to 404 (no secrets file)

  function showSettingsModal() {
    settingsForceShown = false;
    if (settingsClose) settingsClose.hidden = false;
    settingsBackdrop.hidden = false;
    if (settingsSave) settingsSave.disabled = true;
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

  function showSettingsError(message) {
    if (errorMessageEl) errorMessageEl.textContent = message || 'Something went wrong.';
    if (errorBackdrop) errorBackdrop.hidden = false;
  }

  function hideSettingsError() {
    if (errorBackdrop) errorBackdrop.hidden = true;
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
      showSettingsError('Failed to load settings: ' + err.message);
    }
  }

  function hasConfigData(block) {
    if (!block || !block.config) return false;
    return Object.values(block.config).some((v) => v != null && String(v).trim() !== '');
  }

  function renderSettingsForm(data) {
    const exchanges = data.exchanges || {};
    const notifications = data.notifications || {};
    for (const [key, block] of Object.entries(exchanges)) {
      const hasData = !!block.enabled || hasConfigData(block);
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      if (enableEl) {
        enableEl.checked = hasData;
        const parentBlock = enableEl.closest('.tb-settings-block');
        if (parentBlock) parentBlock.classList.toggle('tb-enabled', hasData);
      }
      if (block.config) {
        for (const [field, value] of Object.entries(block.config)) {
          const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
          if (input) input.value = value && String(value).trim() !== '' ? MASK : value || '';
        }
      }
    }
    for (const [key, block] of Object.entries(notifications)) {
      const hasData = !!block.enabled || hasConfigData(block);
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      if (enableEl) {
        enableEl.checked = hasData;
        const parentBlock = enableEl.closest('.tb-settings-block');
        if (parentBlock) parentBlock.classList.toggle('tb-enabled', hasData);
      }
      if (block.config) {
        for (const [field, value] of Object.entries(block.config)) {
          const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
          if (input) input.value = value && String(value).trim() !== '' ? MASK : value || '';
        }
      }
    }
    updateSaveButtonState();
  }

  function getConfigValue(key, field, input) {
    const raw = input ? input.value.trim() : '';
    if (raw === MASK && currentSettings) {
      const block = (currentSettings.exchanges && currentSettings.exchanges[key]) ||
        (currentSettings.notifications && currentSettings.notifications[key]);
      const stored = block && block.config && block.config[field];
      return stored != null ? String(stored).trim() : '';
    }
    return raw;
  }

  const fieldMap = {
    coinbase: ['kid', 'pem'],
    coinex: ['key', 'secret'],
    telegram: ['token', 'owner', 'admin', 'others'],
    pushover: ['application_key', 'user_key'],
  };

  function collectSettingsPayload() {
    const exchanges = {};
    const notifications = {};
    const exchangeKeys = ['coinbase', 'coinex'];
    const notificationKeys = ['telegram', 'pushover'];
    for (const key of exchangeKeys) {
      const config = {};
      for (const field of fieldMap[key] || []) {
        const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
        config[field] = getConfigValue(key, field, input);
      }
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      const enabled = enableEl ? enableEl.checked : false;
      exchanges[key] = { enabled, config };
    }
    for (const key of notificationKeys) {
      const config = {};
      for (const field of fieldMap[key] || []) {
        const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
        config[field] = getConfigValue(key, field, input);
      }
      const enableEl = document.querySelector(`input[data-enable="${key}"]`);
      const enabled = enableEl ? enableEl.checked : false;
      notifications[key] = { enabled, config };
    }
    return { exchanges, notifications };
  }

  function validateSettingsPayload(payload) {
    const errors = [];
    const keys = ['coinbase', 'coinex', 'telegram', 'pushover'];
    for (const key of keys) {
      const block = (payload.exchanges && payload.exchanges[key]) || (payload.notifications && payload.notifications[key]);
      if (!block || !block.enabled) continue;
      const required = REQUIRED_FIELDS[key];
      if (!required) continue;
      for (const field of required) {
        const value = (block.config && block.config[field]) ? String(block.config[field]).trim() : '';
        if (value === '') {
          const label = (FIELD_LABELS[key] && FIELD_LABELS[key][field]) || field;
          errors.push((KEY_DISPLAY_NAMES[key] || key) + ': ' + label + ' is required');
        }
      }
    }
    return errors;
  }

  const KEY_DISPLAY_NAMES = { coinbase: 'Coinbase', coinex: 'CoinEx', telegram: 'Telegram', pushover: 'Pushover' };
  const FIELD_LABELS = {
    coinbase: { kid: 'Key ID', pem: 'PEM' },
    coinex: { key: 'Key', secret: 'Secret' },
    telegram: { token: 'Bot token', owner: 'Owner ID', admin: 'Admin ID', others: 'Other IDs (comma-separated)' },
    pushover: { application_key: 'Application key', user_key: 'User key' },
  };
  const REQUIRED_FIELDS = {
    coinbase: ['kid', 'pem'],
    coinex: ['key', 'secret'],
    telegram: ['token', 'owner'],
    pushover: ['application_key', 'user_key'],
  };

  function getDiffLabel(key, field) {
    const keyName = KEY_DISPLAY_NAMES[key] || key;
    if (field === 'enabled') return keyName + ' → Enabled';
    const fieldLabel = (FIELD_LABELS[key] && FIELD_LABELS[key][field]) || field;
    return keyName + ' → ' + fieldLabel;
  }

  function hasSettingsChanges() {
    if (!currentSettings) return true;
    const payload = collectSettingsPayload();
    return buildDiffLines(currentSettings, payload).length > 0;
  }

  function updateSaveButtonState() {
    if (!settingsSave) return;
    settingsSave.disabled = !hasSettingsChanges();
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
        settingsForceShown = true;
        if (settingsClose) settingsClose.hidden = true;
        settingsBackdrop.hidden = false;
        if (settingsSave) settingsSave.disabled = true;
        loadSettings();
      }
    } catch (_) { /* ignore */ }
  })();

  if (settingsBtn) settingsBtn.addEventListener('click', showSettingsModal);
  if (settingsClose) settingsClose.addEventListener('click', hideSettingsModal);
  const settingsModal = document.getElementById('tb-settings-modal');
  if (settingsModal) settingsModal.addEventListener('click', (e) => e.stopPropagation());
  if (settingsSave) settingsSave.addEventListener('click', () => {
    if (!hasSettingsChanges()) return;
    const payload = collectSettingsPayload();
    const validationErrors = validateSettingsPayload(payload);
    if (validationErrors.length > 0) {
      showSettingsError(validationErrors.join('\n'));
      return;
    }
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

  if (errorBackdrop) errorBackdrop.addEventListener('click', (e) => { if (e.target === errorBackdrop) hideSettingsError(); });
  const errorModal = document.getElementById('tb-error-modal');
  if (errorModal) errorModal.addEventListener('click', (e) => e.stopPropagation());
  if (errorOkBtn) errorOkBtn.addEventListener('click', hideSettingsError);
  if (errorCloseBtn) errorCloseBtn.addEventListener('click', hideSettingsError);

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
      hideConfirmModal();
      showSettingsError('Failed to save: ' + err.message);
    }
  });

  document.querySelectorAll('input[data-enable]').forEach((el) => {
    el.addEventListener('change', () => {
      const block = el.closest('.tb-settings-block');
      if (block) block.classList.toggle('tb-enabled', el.checked);
      updateSaveButtonState();
    });
  });

  function syncSectionEnable(key) {
    const enableEl = document.querySelector(`input[data-enable="${key}"]`);
    if (!enableEl) return;
    const fields = fieldMap[key] || [];
    let hasData = false;
    for (const field of fields) {
      const input = document.querySelector(`input[data-config="${key}"][data-field="${field}"]`);
      const val = input ? getConfigValue(key, field, input) : '';
      if (val && String(val).trim() !== '') {
        hasData = true;
        break;
      }
    }
    if (hasData) {
      enableEl.checked = true;
      const parentBlock = enableEl.closest('.tb-settings-block');
      if (parentBlock) parentBlock.classList.add('tb-enabled');
    }
  }

  document.querySelectorAll('input[data-config]').forEach((input) => {
    const key = input.getAttribute('data-config');
    if (!key) return;
    input.addEventListener('input', () => {
      syncSectionEnable(key);
      updateSaveButtonState();
    });
    input.addEventListener('change', () => {
      syncSectionEnable(key);
      updateSaveButtonState();
    });
  });
})();
