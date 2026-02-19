(() => {
  const statusEl = document.getElementById('tb-status');
  const titleEl = document.getElementById('tb-job-title');
  const subtitleEl = document.getElementById('tb-job-subtitle');
  const summaryGrid = document.getElementById('tb-summary-grid');
  const statsSection = document.getElementById('tb-stats-section');
  const statsGrid = document.getElementById('tb-stats-grid');
  const pairsSection = document.getElementById('tb-pairs-section');
  const pairsTable = document.getElementById('tb-pairs-table');

  const uid = new URLSearchParams(window.location.search).get('uid');

  function setStatus(msg, kind = 'info') {
    if (!statusEl) return;
    statusEl.textContent = msg || '';
    statusEl.className = `tb-status tb-status-${kind}`;
  }

  function row(grid, label, value) {
    const dt = document.createElement('dt');
    dt.className = 'tb-detail-label';
    dt.textContent = label;
    const dd = document.createElement('dd');
    dd.className = 'tb-detail-value';
    dd.textContent = (value != null && value !== '') ? String(value) : '—';
    grid.appendChild(dt);
    grid.appendChild(dd);
  }

  function pct(v) {
    return (v != null && v !== '') ? v + '%' : '—';
  }

  async function load() {
    if (!uid) {
      setStatus('No job UID in URL.', 'error');
      return;
    }
    setStatus('Loading…', 'info');
    try {
      const resp = await fetch(`/trader/waller?uid=${encodeURIComponent(uid)}`);
      if (!resp.ok) {
        const text = await resp.text();
        throw new Error(`HTTP ${resp.status}: ${text}`);
      }
      render(await resp.json());
      setStatus('', 'info');
    } catch (err) {
      setStatus(`Failed to load job: ${err.message}`, 'error');
    }
  }

  function render(job) {
    const name = job.Name || '(unnamed)';
    if (titleEl) titleEl.textContent = name;
    if (subtitleEl) subtitleEl.textContent = job.UID;
    document.title = `${name} — Tradebot`;

    // -- Summary grid --
    row(summaryGrid, 'UID', job.UID);
    row(summaryGrid, 'Name', job.Name || '(unnamed)');
    row(summaryGrid, 'Type', job.Type);
    row(summaryGrid, 'Status', job.Status);
    row(summaryGrid, 'Product', job.ProductID);
    row(summaryGrid, 'Exchange', job.ExchangeName);

    if (job.HasStatus) {
      row(summaryGrid, 'Budget', job.Budget);
      row(summaryGrid, 'Return', pct(job.Return));
      row(summaryGrid, 'AnnualReturn', pct(job.AnnualReturn));
      row(summaryGrid, 'Days', job.Days);
      row(summaryGrid, 'Buys', job.Buys);
      row(summaryGrid, 'Sells', job.Sells);
      row(summaryGrid, 'Profit', job.Profit);
      row(summaryGrid, 'Fees', job.Fees);
    }

    // -- Stats grid --
    if (job.HasStatus) {
      statsSection.hidden = false;
      row(statsGrid, 'ProfitPerDay', job.ProfitPerDay);

      row(statsGrid, 'BoughtValue', job.BoughtValue);
      row(statsGrid, 'BoughtSize', job.BoughtSize);
      row(statsGrid, 'BoughtFees', job.BoughtFees);

      row(statsGrid, 'SoldValue', job.SoldValue);
      row(statsGrid, 'SoldSize', job.SoldSize);
      row(statsGrid, 'SoldFees', job.SoldFees);

      row(statsGrid, 'UnsoldValue', job.UnsoldValue);
      row(statsGrid, 'UnsoldSize', job.UnsoldSize);
      row(statsGrid, 'UnsoldFees', job.UnsoldFees);

      row(statsGrid, 'OversoldValue', job.OversoldValue);
      row(statsGrid, 'OversoldSize', job.OversoldSize);
      row(statsGrid, 'OversoldFees', job.OversoldFees);
    }

    // -- Pairs table --
    if (job.Pairs && job.Pairs.length > 0) {
      pairsSection.hidden = false;
      const tbody = pairsTable.querySelector('tbody');
      for (const p of job.Pairs.filter(p => p.Buys > 0)) {
        const tr = document.createElement('tr');
        tr.innerHTML = `
          <td>${p.Index}</td>
          <td>${p.Pair}</td>
          <td>${p.Budget}</td>
          <td>${pct(p.Return)}</td>
          <td>${pct(p.AnnualReturn)}</td>
          <td>${p.Days}</td>
          <td>${p.Buys}</td>
          <td>${p.Sells}</td>
          <td>${p.Profit}</td>
          <td>${p.Fees}</td>
          <td>${p.BoughtValue}</td>
          <td>${p.SoldValue}</td>
          <td>${p.UnsoldValue}</td>
          <td>${p.SoldSize}</td>
          <td>${p.UnsoldSize}</td>
        `;
        tbody.appendChild(tr);
      }
    }
  }

  load();
})();
