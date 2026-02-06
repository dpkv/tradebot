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
})();

