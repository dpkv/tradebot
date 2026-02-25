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
    { index: 0,  key: '_action',     filterType: 'none'    },
    { index: 1,  key: 'Name',        filterType: 'string'  },
    { index: 2,  key: 'Status',      filterType: 'enum'    },
    { index: 3,  key: 'ProductID',   filterType: 'enum'    },
    { index: 4,  key: 'Budget',      filterType: 'numeric' },
    { index: 5,  key: 'Return',      filterType: 'numeric' },
    { index: 6,  key: 'AnnualReturn',filterType: 'numeric' },
    { index: 7,  key: 'Days',        filterType: 'numeric' },
    { index: 8,  key: 'Buys',        filterType: 'numeric' },
    { index: 9,  key: 'Sells',       filterType: 'numeric' },
    { index: 10, key: 'Profit',      filterType: 'numeric' },
    { index: 11, key: 'Fees',        filterType: 'numeric' },
    { index: 12, key: 'BoughtValue', filterType: 'numeric' },
    { index: 13, key: 'SoldValue',   filterType: 'numeric' },
    { index: 14, key: 'UnsoldValue', filterType: 'numeric' },
    { index: 15, key: 'SoldSize',    filterType: 'numeric' },
    { index: 16, key: 'UnsoldSize',  filterType: 'numeric' },
  ];

  let headerCells = [];

  // --- Add job dialog state ---
  let addJobValidated = false;
  let addJobValidatedResponse = null;

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
      if (!mapEntry || mapEntry.key === '_action') return;
      th.classList.add('tb-sortable');
      if (sortState.key === mapEntry.key) {
        th.classList.add(sortState.dir === 'asc' ? 'tb-sort-asc' : 'tb-sort-desc');
      }
    });
  }

  async function onPauseResumeClick(job, isPaused) {
    const action = isPaused ? 'Resume' : 'Pause';
    const name = job.Name || job.UID || 'this job';
    if (!confirm(`${action} job "${name}"?`)) return;
    const path = isPaused ? '/trader/job/resume' : '/trader/job/pause';
    setStatus(`${action}ing job…`, 'info');
    try {
      const resp = await fetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ UID: job.UID }),
      });
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(text || `HTTP ${resp.status}`);
      }
      setStatus(`Job ${action.toLowerCase()}d.`, 'success');
      await loadJobs();
    } catch (err) {
      setStatus(`${action} failed: ${err.message}`, 'error');
    }
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
      const status = (job.Status || '').toLowerCase();
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

      const actionTd = document.createElement('td');
      actionTd.className = 'tb-col-action';
      if (status === 'running' || status === 'paused') {
        const isPaused = status === 'paused';
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = isPaused ? 'tb-job-play-btn' : 'tb-job-pause-btn';
        btn.title = isPaused ? 'Resume job' : 'Pause job';
        btn.setAttribute('aria-label', isPaused ? 'Resume' : 'Pause');
        btn.innerHTML = isPaused ? '&#9658;' : '&#10074;&#10074;'; // play ▶, pause ⏸
        btn.addEventListener('click', (e) => {
          e.stopPropagation();
          onPauseResumeClick(job, isPaused);
        });
        actionTd.appendChild(btn);
      }

      tr.innerHTML = `
        <td title="${nameTitle}">${name}</td>
        <td>${job.Status || ''}</td>
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
      tr.insertBefore(actionTd, tr.firstChild);

      tr.classList.add(`tb-state-${status}`);
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

  // --- Add job dialog ---

  const addJobBackdrop = document.getElementById('tb-add-job-backdrop');
  const addJobModal = document.getElementById('tb-add-job-modal');
  const addJobBtn = document.getElementById('tb-add-job');
  const addJobCloseBtn = document.getElementById('tb-add-job-close');
  const addJobForm = document.getElementById('tb-add-job-form');
  const addJobErrorEl = document.getElementById('tb-add-job-error');
  const addJobPreviewEl = document.getElementById('tb-add-job-preview');
  const addJobOverviewBody = document.getElementById('tb-add-job-overview-body');
  const addJobLoopBody = document.getElementById('tb-add-job-loop-body');
  const addJobPriceBody = document.getElementById('tb-add-job-price-body');
  const addJobProfitBody = document.getElementById('tb-add-job-profit-body');
  const addJobAprBody = document.getElementById('tb-add-job-apr-body');
  const addJobPairsBody = document.getElementById('tb-add-job-pairs-body');
  const addJobValidateBtn = document.getElementById('tb-add-job-validate');
  const addJobCreateBtn = document.getElementById('tb-add-job-create');

  function openAddJobDialog() {
    if (!addJobBackdrop) return;
    addJobValidated = false;
    addJobValidatedResponse = null;
    if (addJobForm) addJobForm.reset();
    const cancelOffset = document.getElementById('tb-add-cancel-offset-pct');
    const feePct = document.getElementById('tb-add-fee-pct');
    if (cancelOffset && !cancelOffset.value) cancelOffset.value = '5';
    if (feePct && !feePct.value) feePct.value = '0.25';
    if (addJobErrorEl) {
      addJobErrorEl.hidden = true;
      addJobErrorEl.textContent = '';
    }
    if (addJobPreviewEl) {
      addJobPreviewEl.hidden = true;
    }
    if (addJobCreateBtn) {
      addJobCreateBtn.disabled = true;
    }
    // Ensure default radio state
    const buyModeAbs = document.querySelector('input[name="tb-add-buy-spacing"][value="abs"]');
    const profitModeAbs = document.querySelector('input[name="tb-add-profit-mode"][value="abs"]');
    if (buyModeAbs) buyModeAbs.checked = true;
    if (profitModeAbs) profitModeAbs.checked = true;
    const buyInterval = document.getElementById('tb-add-buy-interval');
    const buyIntervalPct = document.getElementById('tb-add-buy-interval-pct');
    if (buyInterval && buyIntervalPct) {
      buyInterval.disabled = false;
      buyIntervalPct.disabled = true;
      buyIntervalPct.value = '';
    }
    const profitMargin = document.getElementById('tb-add-profit-margin');
    const profitMarginPct = document.getElementById('tb-add-profit-margin-pct');
    if (profitMargin && profitMarginPct) {
      profitMargin.disabled = false;
      profitMarginPct.disabled = true;
      profitMarginPct.value = '';
    }
    addJobBackdrop.hidden = false;
    if (addJobModal) addJobModal.addEventListener('click', (e) => e.stopPropagation(), { once: true });
    updateAddJobBudgetDisplay();
  }

  function closeAddJobDialog() {
    if (addJobBackdrop) addJobBackdrop.hidden = true;
  }

  function invalidateAddJobValidation() {
    addJobValidated = false;
    addJobValidatedResponse = null;
    if (addJobCreateBtn) addJobCreateBtn.disabled = true;
    if (addJobPreviewEl) addJobPreviewEl.hidden = true;
    if (addJobErrorEl) {
      addJobErrorEl.hidden = true;
      addJobErrorEl.textContent = '';
    }
  }

  /** Compute budget from begin, end, interval (or interval %), size, fee %. Mirrors server pair generation. */
  function computeAddJobBudget() {
    const getNum = (id) => {
      const el = document.getElementById(id);
      if (!el) return 0;
      const v = el.value.trim();
      return v === '' ? 0 : Number(v);
    };
    const begin = getNum('tb-add-begin-price');
    const end = getNum('tb-add-end-price');
    const buySize = getNum('tb-add-buy-size');
    const feePct = getNum('tb-add-fee-pct') || 0;
    const buySpacing = document.querySelector('input[name="tb-add-buy-spacing"]:checked');
    const usePct = buySpacing && buySpacing.value === 'pct';
    const interval = getNum('tb-add-buy-interval');
    const intervalPct = getNum('tb-add-buy-interval-pct');

    if (begin <= 0 || end <= begin || buySize <= 0) return null;
    const stepAbs = !usePct ? interval : 0;
    const stepPct = usePct ? intervalPct : 0;
    if (stepAbs <= 0 && stepPct <= 0) return null;

    const feeFactor = 1 + (2 * feePct) / 100;
    let total = 0;
    let price = begin;
    const maxPairs = 100000;
    let count = 0;
    while (price < end && count < maxPairs) {
      total += price * buySize * feeFactor;
      const step = stepAbs > 0 ? stepAbs : price * (stepPct / 100);
      if (step <= 0) break;
      price += step;
      count++;
    }
    return count >= maxPairs ? null : total;
  }

  function updateAddJobBudgetDisplay() {
    const el = document.getElementById('tb-add-job-budget-value');
    if (!el) return;
    const budget = computeAddJobBudget();
    el.textContent = budget != null ? fmtBudget(budget) : '—';
  }

  function collectWallerSpecFromForm() {
    const getNumber = (id) => {
      const el = document.getElementById(id);
      if (!el) return 0;
      const v = el.value.trim();
      return v === '' ? 0 : Number(v);
    };

    const buySpacing = document.querySelector('input[name="tb-add-buy-spacing"]:checked');
    const profitMode = document.querySelector('input[name="tb-add-profit-mode"]:checked');

    const beginPrice = getNumber('tb-add-begin-price');
    const endPrice = getNumber('tb-add-end-price');
    let buyInterval = 0;
    let buyIntervalPct = 0;
    if (buySpacing && buySpacing.value === 'pct') {
      buyIntervalPct = getNumber('tb-add-buy-interval-pct');
    } else {
      buyInterval = getNumber('tb-add-buy-interval');
    }

    let profitMargin = 0;
    let profitMarginPct = 0;
    if (profitMode && profitMode.value === 'pct') {
      profitMarginPct = getNumber('tb-add-profit-margin-pct');
    } else {
      profitMargin = getNumber('tb-add-profit-margin');
    }

    const buySize = getNumber('tb-add-buy-size');
    const cancelOffsetPct = getNumber('tb-add-cancel-offset-pct') || 5;
    const feePct = getNumber('tb-add-fee-pct') || 0.25;

    return {
      BeginPrice: beginPrice,
      EndPrice: endPrice,
      BuyInterval: buyInterval,
      BuyIntervalPct: buyIntervalPct,
      ProfitMargin: profitMargin,
      ProfitMarginPct: profitMarginPct,
      BuySize: buySize,
      CancelOffsetPct: cancelOffsetPct,
      FeePct: feePct,
    };
  }

  function renderAddJobSummary(summary) {
    if (!summary) return;
    const makeRow = (label, value) => {
      const tr = document.createElement('tr');
      const th = document.createElement('th');
      th.textContent = label;
      const td = document.createElement('td');
      td.textContent = value != null && value !== '' ? String(value) : '—';
      tr.appendChild(th);
      tr.appendChild(td);
      return tr;
    };

    if (addJobOverviewBody) {
      addJobOverviewBody.innerHTML = '';
      addJobOverviewBody.appendChild(makeRow('Budget required', fmtBudget(summary.Budget)));
      addJobOverviewBody.appendChild(makeRow('Fee %', summary.FeePct));
      addJobOverviewBody.appendChild(makeRow('Num pairs', summary.NumPairs));
    }

    if (addJobLoopBody) {
      addJobLoopBody.innerHTML = '';
      addJobLoopBody.appendChild(makeRow('Min loop fee', summary.MinLoopFee));
      addJobLoopBody.appendChild(makeRow('Avg loop fee', summary.AvgLoopFee));
      addJobLoopBody.appendChild(makeRow('Max loop fee', summary.MaxLoopFee));
    }

    if (addJobPriceBody) {
      addJobPriceBody.innerHTML = '';
      addJobPriceBody.appendChild(makeRow('Min price margin', summary.MinPriceMargin));
      addJobPriceBody.appendChild(makeRow('Avg price margin', summary.AvgPriceMargin));
      addJobPriceBody.appendChild(makeRow('Max price margin', summary.MaxPriceMargin));
    }

    if (addJobProfitBody) {
      addJobProfitBody.innerHTML = '';
      addJobProfitBody.appendChild(makeRow('Min profit margin', summary.MinProfitMargin));
      addJobProfitBody.appendChild(makeRow('Avg profit margin', summary.AvgProfitMargin));
      addJobProfitBody.appendChild(makeRow('Max profit margin', summary.MaxProfitMargin));
    }

    if (addJobAprBody) {
      addJobAprBody.innerHTML = '';
      if (summary.APRs && summary.APRs.length) {
        for (const apr of summary.APRs) {
          const tr = document.createElement('tr');
          tr.innerHTML = `
            <td>${apr.RatePct}</td>
            <td>${apr.NumSellsPerDay}</td>
            <td>${apr.NumSellsPerMonth}</td>
            <td>${apr.NumSellsPerYear}</td>
          `;
          addJobAprBody.appendChild(tr);
        }
      }
    }
  }

  /** Buy/sell prices: min 2 decimals; show 3rd (4th, 5th) digit only if rounding to fewer would make buy and sell display the same. */
  function fmtPricePair(buyPrice, sellPrice) {
    const b = Number(buyPrice);
    const s = Number(sellPrice);
    if (Number.isNaN(b) || Number.isNaN(s)) {
      return { buy: buyPrice == null || buyPrice === '' ? '—' : String(buyPrice), sell: sellPrice == null || sellPrice === '' ? '—' : String(sellPrice) };
    }
    const b5 = Math.round(b * 1e5) / 1e5;
    const s5 = Math.round(s * 1e5) / 1e5;
    let decimals = 5;
    for (let d = 2; d <= 5; d++) {
      if (b5.toFixed(d) !== s5.toFixed(d)) {
        decimals = d;
        break;
      }
    }
    return { buy: b5.toFixed(decimals), sell: s5.toFixed(decimals) };
  }

  /** Budget: 2 decimals; extra digits (3–5) only if |value| < 0.01 (so 2 decimals would show 0.00). */
  function fmtBudget(v) {
    if (v == null || v === '') return '—';
    const n = Number(v);
    if (Number.isNaN(n)) return String(v);
    const r = Math.round(n * 1e5) / 1e5;
    if (r === 0) return '0.00';
    let decimals = 2;
    if (Math.abs(r) < 0.01) {
      for (let d = 3; d <= 5; d++) {
        if (Math.round(r * 10 ** d) / 10 ** d !== 0) {
          decimals = d;
          break;
        }
      }
    }
    return r.toFixed(decimals);
  }

  function renderAddJobPairs(previewPairs) {
    if (!addJobPairsBody) return;
    addJobPairsBody.innerHTML = '';
    for (const p of previewPairs || []) {
      const prices = fmtPricePair(p.BuyPrice, p.SellPrice);
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${p.BuySize}</td>
        <td>${prices.buy}</td>
        <td>${p.SellSize}</td>
        <td>${prices.sell}</td>
        <td>${p.PriceMargin}</td>
        <td>${p.Profit}</td>
      `;
      addJobPairsBody.appendChild(tr);
    }
  }

  async function onAddJobValidateClick() {
    if (!addJobErrorEl || !addJobPreviewEl || !addJobValidateBtn) return;
    addJobErrorEl.hidden = true;
    addJobErrorEl.textContent = '';
    addJobPreviewEl.hidden = true;
    if (addJobCreateBtn) addJobCreateBtn.disabled = true;

    const spec = collectWallerSpecFromForm();

    try {
      addJobValidateBtn.disabled = true;
      const resp = await fetch('/trader/waller/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(spec),
      });
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(text || `HTTP ${resp.status}`);
      }
      const data = await resp.json();
      if (!data.Valid) {
        addJobValidated = false;
        addJobValidatedResponse = null;
        addJobErrorEl.textContent = data.Error || 'Validation failed.';
        addJobErrorEl.hidden = false;
        return;
      }
      addJobValidated = true;
      addJobValidatedResponse = data;
      renderAddJobSummary(data.Summary || {});
      renderAddJobPairs(data.PreviewPairs || []);
      addJobPreviewEl.hidden = false;
      if (addJobCreateBtn) addJobCreateBtn.disabled = false;
    } catch (err) {
      addJobValidated = false;
      addJobValidatedResponse = null;
      addJobErrorEl.textContent = `Validation failed: ${err.message || err}`;
      addJobErrorEl.hidden = false;
    } finally {
      addJobValidateBtn.disabled = false;
    }
  }

  async function onAddJobCreateClick() {
    if (!addJobValidated || !addJobValidatedResponse || !addJobErrorEl) return;

    const nameEl = document.getElementById('tb-add-job-name');
    const productEl = document.getElementById('tb-add-job-product');
    const exchangeEl = document.getElementById('tb-add-job-exchange');
    const jobName = nameEl ? nameEl.value.trim() : '';
    const productID = productEl ? productEl.value.trim() : '';
    const exchangeName = exchangeEl ? exchangeEl.value.trim() : '';

    if (!jobName || !productID || !exchangeName) {
      addJobErrorEl.textContent = 'Name, exchange, and product are required.';
      addJobErrorEl.hidden = false;
      return;
    }

    try {
      addJobErrorEl.hidden = true;
      addJobErrorEl.textContent = '';

      const previewPairs = addJobValidatedResponse.PreviewPairs || [];
      if (!Array.isArray(previewPairs) || previewPairs.length === 0) {
        addJobErrorEl.textContent = 'Validated job has no pairs to create.';
        addJobErrorEl.hidden = false;
        return;
      }

      const wallReq = {
        ExchangeName: exchangeName,
        ProductID: productID,
        Pairs: previewPairs.map((p) => ({
          Buy: {
            Size: p.BuySize,
            Price: p.BuyPrice,
            Cancel: p.BuyCancel,
          },
          Sell: {
            Size: p.SellSize,
            Price: p.SellPrice,
            Cancel: p.SellCancel,
          },
        })),
      };

      const resp1 = await fetch('/trader/wall', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(wallReq),
      });
      if (!resp1.ok) {
        const text = await resp1.text();
        throw new Error(text || `HTTP ${resp1.status}`);
      }
      const wallData = await resp1.json();
      const uid = wallData.UID;
      if (!uid) {
        throw new Error('Server did not return a job UID.');
      }

      const nameReq = { UID: uid, JobName: jobName };
      const resp2 = await fetch('/trader/set-job-name', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(nameReq),
      });
      if (!resp2.ok) {
        const text = await resp2.text();
        console.error('Job is created, but could not set the job name', text);
      }

      closeAddJobDialog();
      await loadJobs();
      setStatus(`Created job ${jobName}`, 'success');
    } catch (err) {
      addJobErrorEl.textContent = `Failed to create job: ${err.message || err}`;
      addJobErrorEl.hidden = false;
    }
  }

  // Attach radio toggles for mutually exclusive fields and invalidate on change.
  const buySpacingRadios = document.querySelectorAll('input[name="tb-add-buy-spacing"]');
  buySpacingRadios.forEach((radio) => {
    radio.addEventListener('change', () => {
      const buyInterval = document.getElementById('tb-add-buy-interval');
      const buyIntervalPct = document.getElementById('tb-add-buy-interval-pct');
      if (!buyInterval || !buyIntervalPct) return;
      if (radio.value === 'pct' && radio.checked) {
        buyInterval.disabled = true;
        buyInterval.value = '';
        buyIntervalPct.disabled = false;
      } else if (radio.value === 'abs' && radio.checked) {
        buyInterval.disabled = false;
        buyIntervalPct.disabled = true;
        buyIntervalPct.value = '';
      }
      invalidateAddJobValidation();
      updateAddJobBudgetDisplay();
    });
  });

  const profitModeRadios = document.querySelectorAll('input[name="tb-add-profit-mode"]');
  profitModeRadios.forEach((radio) => {
    radio.addEventListener('change', () => {
      const profitMargin = document.getElementById('tb-add-profit-margin');
      const profitMarginPct = document.getElementById('tb-add-profit-margin-pct');
      if (!profitMargin || !profitMarginPct) return;
      if (radio.value === 'pct' && radio.checked) {
        profitMargin.disabled = true;
        profitMargin.value = '';
        profitMarginPct.disabled = false;
      } else if (radio.value === 'abs' && radio.checked) {
        profitMargin.disabled = false;
        profitMarginPct.disabled = true;
        profitMarginPct.value = '';
      }
      invalidateAddJobValidation();
    });
  });

  if (addJobForm) {
    addJobForm.addEventListener('input', (e) => {
      const target = e.target;
      if (target && target.name && (target.name === 'tb-add-buy-spacing' || target.name === 'tb-add-profit-mode')) {
        return;
      }
      invalidateAddJobValidation();
      updateAddJobBudgetDisplay();
    });
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

      if (entry.filterType === 'none') {
        filterRow.appendChild(td);
        continue;
      }
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
      if (mapEntry.key === '_action') {
        th.classList.remove('tb-sortable');
        return;
      }
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

  // --- Alerts modal ---
  const alertsBtn = document.getElementById('tb-alerts-btn');
  const alertsBackdrop = document.getElementById('tb-alerts-backdrop');
  const alertsModal = document.getElementById('tb-alerts-modal');
  const alertsCloseBtn = document.getElementById('tb-alerts-close');
  const alertsCurrentEl = document.getElementById('tb-alerts-current');
  const alertsExchangeSelect = document.getElementById('tb-alerts-exchange');
  const alertsCurrencyInput = document.getElementById('tb-alerts-currency');
  const alertsLimitInput = document.getElementById('tb-alerts-limit');
  const alertsAddRowBtn = document.getElementById('tb-alerts-add-row');
  const alertsPendingListEl = document.getElementById('tb-alerts-pending-list');
  const alertsSaveBtn = document.getElementById('tb-alerts-save');

  /** @type {Array<{ currency: string, limit: string }>} */
  let alertsPendingRows = [];

  async function loadAlertsData() {
    const resp = await fetch('/api/alerts');
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
  }

  function renderAlertsCurrent(data) {
    if (!alertsCurrentEl) return;
    alertsCurrentEl.innerHTML = '';
    const defaultLimits = data.lowBalanceLimits || {};
    const perExchange = data.perExchange || {};
    const defaultEntries = Object.entries(defaultLimits);
    const hasAny = defaultEntries.length > 0 || Object.keys(perExchange).length > 0;
    if (!hasAny) {
      const p = document.createElement('p');
      p.className = 'tb-alerts-empty';
      p.textContent = 'No low balance alerts configured.';
      alertsCurrentEl.appendChild(p);
      return;
    }
    if (defaultEntries.length > 0) {
      const h4 = document.createElement('h4');
      h4.textContent = 'Default (all exchanges)';
      h4.className = 'tb-alerts-subtitle';
      alertsCurrentEl.appendChild(h4);
      const ul = document.createElement('ul');
      ul.className = 'tb-alerts-list';
      for (const [ccy, lim] of defaultEntries) {
        const li = document.createElement('li');
        li.textContent = `${ccy}: ${lim}`;
        ul.appendChild(li);
      }
      alertsCurrentEl.appendChild(ul);
    }
    for (const [ex, cfg] of Object.entries(perExchange)) {
      const limits = (cfg && cfg.lowBalanceLimits) ? Object.entries(cfg.lowBalanceLimits) : [];
      if (limits.length === 0) continue;
      const h4 = document.createElement('h4');
      h4.textContent = ex;
      h4.className = 'tb-alerts-subtitle';
      alertsCurrentEl.appendChild(h4);
      const ul = document.createElement('ul');
      ul.className = 'tb-alerts-list';
      for (const [ccy, lim] of limits) {
        const li = document.createElement('li');
        li.textContent = `${ccy}: ${lim}`;
        ul.appendChild(li);
      }
      alertsCurrentEl.appendChild(ul);
    }
  }

  function renderAlertsPending() {
    if (!alertsPendingListEl) return;
    alertsPendingListEl.innerHTML = '';
    for (let i = 0; i < alertsPendingRows.length; i++) {
      const row = alertsPendingRows[i];
      const div = document.createElement('div');
      div.className = 'tb-alerts-pending-row';
      div.innerHTML = `<span>${row.currency}: ${row.limit}</span> <button type="button" class="tb-alerts-remove" data-index="${i}" aria-label="Remove">×</button>`;
      alertsPendingListEl.appendChild(div);
    }
    alertsPendingListEl.querySelectorAll('.tb-alerts-remove').forEach((btn) => {
      btn.addEventListener('click', () => {
        const idx = parseInt(btn.getAttribute('data-index'), 10);
        alertsPendingRows.splice(idx, 1);
        renderAlertsPending();
      });
    });
  }

  async function openAlertsModal() {
    if (!alertsBackdrop) return;
    alertsPendingRows = [];
    renderAlertsPending();
    setStatus('Loading alerts…', 'info');
    try {
      const data = await loadAlertsData();
      renderAlertsCurrent(data);
      setStatus('', 'info');
    } catch (err) {
      setStatus(`Failed to load alerts: ${err.message}`, 'error');
    }
    alertsBackdrop.hidden = false;
  }

  function closeAlertsModal() {
    if (alertsBackdrop) alertsBackdrop.hidden = true;
  }

  function addAlertsRow() {
    const currency = (alertsCurrencyInput && alertsCurrencyInput.value.trim().toUpperCase()) || '';
    const limit = (alertsLimitInput && alertsLimitInput.value.trim()) || '';
    if (!currency || !limit) return;
    alertsPendingRows.push({ currency, limit });
    renderAlertsPending();
    if (alertsCurrencyInput) alertsCurrencyInput.value = '';
    if (alertsLimitInput) alertsLimitInput.value = '';
  }

  async function saveAlerts() {
    if (alertsPendingRows.length === 0) {
      setStatus('Add at least one currency limit before saving.', 'error');
      return;
    }
    const lowBalanceLimits = {};
    for (const row of alertsPendingRows) {
      lowBalanceLimits[row.currency] = row.limit;
    }
    const exchange = (alertsExchangeSelect && alertsExchangeSelect.value) || '';
    setStatus('Saving…', 'info');
    try {
      const resp = await fetch('/api/alerts', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ lowBalanceLimits, exchange }),
      });
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(text || `HTTP ${resp.status}`);
      }
      setStatus('Alerts saved.', 'success');
      const data = await loadAlertsData();
      renderAlertsCurrent(data);
      alertsPendingRows = [];
      renderAlertsPending();
    } catch (err) {
      setStatus(`Save failed: ${err.message}`, 'error');
    }
  }

  if (alertsBtn) alertsBtn.addEventListener('click', openAlertsModal);
  if (alertsCloseBtn) alertsCloseBtn.addEventListener('click', closeAlertsModal);
  if (alertsBackdrop) alertsBackdrop.addEventListener('click', (e) => { if (e.target === alertsBackdrop) closeAlertsModal(); });
  if (alertsModal) alertsModal.addEventListener('click', (e) => e.stopPropagation());
  if (alertsAddRowBtn) alertsAddRowBtn.addEventListener('click', addAlertsRow);
  if (alertsSaveBtn) alertsSaveBtn.addEventListener('click', saveAlerts);

  // Wire Add job dialog buttons.
  if (addJobBtn) {
    addJobBtn.addEventListener('click', openAddJobDialog);
  }
  if (addJobCloseBtn && addJobBackdrop) {
    addJobCloseBtn.addEventListener('click', closeAddJobDialog);
    addJobBackdrop.addEventListener('click', (e) => {
      if (e.target === addJobBackdrop) closeAddJobDialog();
    });
  }
  if (addJobValidateBtn) {
    addJobValidateBtn.addEventListener('click', onAddJobValidateClick);
  }
  if (addJobCreateBtn) {
    addJobCreateBtn.addEventListener('click', onAddJobCreateClick);
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
