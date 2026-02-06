(() => {
  const statusEl = document.getElementById('tb-status');
  const tableBody = document.querySelector('#tb-jobs-table tbody');
  const refreshBtn = document.getElementById('tb-refresh');

  function setStatus(message, kind = 'info') {
    statusEl.textContent = message || '';
    statusEl.className = `tb-status tb-status-${kind}`;
  }

  function clearTable() {
    while (tableBody.firstChild) {
      tableBody.removeChild(tableBody.firstChild);
    }
  }

  function renderJobs(jobs) {
    clearTable();
    if (!jobs || jobs.length === 0) {
      setStatus('No jobs found.', 'info');
      return;
    }

    for (const job of jobs) {
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
      renderJobs(data.Jobs || data.jobs || []);
      setStatus(`Loaded ${data.Jobs ? data.Jobs.length : (data.jobs || []).length} job(s).`, 'success');
    } catch (err) {
      console.error('Failed to load jobs', err);
      setStatus(`Failed to load jobs: ${err.message}`, 'error');
    }
  }

  if (refreshBtn) {
    refreshBtn.addEventListener('click', () => {
      loadJobs();
    });
  }

  // Initial load.
  loadJobs();
})();

