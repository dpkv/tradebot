(() => {
  const statusEl = document.getElementById('tb-status');
  const titleEl = document.getElementById('tb-job-title');
  const subtitleEl = document.getElementById('tb-job-subtitle');
  const summaryGrid = document.getElementById('tb-summary-grid');
  const statsSection = document.getElementById('tb-stats-section');
  const statsGrid = document.getElementById('tb-stats-grid');
  const pairsSection = document.getElementById('tb-pairs-section');
  const pairsTable = document.getElementById('tb-pairs-table');
  const showAllCheckbox = document.getElementById('tb-show-all-zero-buys');

  const uid = new URLSearchParams(window.location.search).get('uid');

  /** @type {object|null} last loaded job for re-rendering pairs */
  let lastJob = null;
  /** When false, hide rows with 0 buys; when true, show all rows. */
  let showAllZeroBuys = false;

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
    dd.textContent = (value != null && value !== '') ? fmtNum(value) : '—';
    grid.appendChild(dt);
    grid.appendChild(dd);
  }

  /** Format number, omitting trailing zeros after decimal; non-numeric values passed through. */
  function fmtNum(v) {
    if (v == null || v === '') return '—';
    const n = Number(v);
    if (Number.isNaN(n)) return String(v);
    const s = String(n);
    return s.replace(/\.0+$/, '');
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

  function pct(v) {
    return (v != null && v !== '') ? fmtNum(v) + '%' : '—';
  }

  /** Format "buy-sell" pair string with min decimals so buy ≠ sell; used when API only gives p.Pair. */
  function fmtPairRange(pairStr) {
    if (pairStr == null || pairStr === '') return '—';
    const parts = String(pairStr).split(/-/, 2);
    if (parts.length !== 2) return pairStr;
    const b = Number(parts[0]);
    const s = Number(parts[1]);
    if (Number.isNaN(b) || Number.isNaN(s)) return pairStr;
    const b5 = Math.round(b * 1e5) / 1e5;
    const s5 = Math.round(s * 1e5) / 1e5;
    let decimals = 5;
    for (let d = 2; d <= 5; d++) {
      if (b5.toFixed(d) !== s5.toFixed(d)) {
        decimals = d;
        break;
      }
    }
    return b5.toFixed(decimals) + '-' + s5.toFixed(decimals);
  }

  /**
   * When showAll is true, compress runs of more than 4 consecutive 0-buy rows
   * to first two + ellipsis placeholder + last two. Returns a list of pair
   * objects or { _ellipsis: true } placeholders.
   */
  function getPairsToDisplay(job, showAll) {
    if (!job.Pairs || job.Pairs.length === 0) return [];
    if (!showAll) return job.Pairs.filter(p => (p.Buys || 0) > 0);

    const out = [];
    const pairs = job.Pairs;
    let i = 0;
    while (i < pairs.length) {
      if ((pairs[i].Buys || 0) !== 0) {
        out.push(pairs[i]);
        i++;
        continue;
      }
      let j = i;
      while (j < pairs.length && (pairs[j].Buys || 0) === 0) j++;
      const runLength = j - i;
      if (runLength > 4) {
        out.push(pairs[i], pairs[i + 1]);
        out.push({ _ellipsis: true });
        out.push(pairs[j - 2], pairs[j - 1]);
      } else {
        for (let k = i; k < j; k++) out.push(pairs[k]);
      }
      i = j;
    }
    return out;
  }

  const PAIRS_COLUMN_COUNT = 15;

  function renderPairsTable(job, showAll) {
    if (!pairsTable || !job.Pairs || job.Pairs.length === 0) return;
    const tbody = pairsTable.querySelector('tbody');
    if (!tbody) return;
    tbody.innerHTML = '';
    const toShow = getPairsToDisplay(job, showAll);
    for (const p of toShow) {
      if (p._ellipsis) {
        const tr = document.createElement('tr');
        tr.className = 'tb-pairs-ellipsis';
        tr.innerHTML = `<td colspan="${PAIRS_COLUMN_COUNT}">…</td>`;
        tbody.appendChild(tr);
        continue;
      }
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${fmtNum(p.Index)}</td>
        <td>${fmtPairRange(p.Pair)}</td>
        <td>${fmtBudget(p.Budget)}</td>
        <td>${pct(p.Return)}</td>
        <td>${pct(p.AnnualReturn)}</td>
        <td>${fmtNum(p.Days)}</td>
        <td>${fmtNum(p.Buys)}</td>
        <td>${fmtNum(p.Sells)}</td>
        <td>${fmtNum(p.Profit)}</td>
        <td>${fmtNum(p.Fees)}</td>
        <td>${fmtNum(p.BoughtValue)}</td>
        <td>${fmtNum(p.SoldValue)}</td>
        <td>${fmtNum(p.UnsoldValue)}</td>
        <td>${fmtNum(p.SoldSize)}</td>
        <td>${fmtNum(p.UnsoldSize)}</td>
      `;
      tbody.appendChild(tr);
    }
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
      row(summaryGrid, 'Budget', fmtBudget(job.Budget));
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
      lastJob = job;
      const allZeroBuys = job.Pairs.every(p => (p.Buys || 0) === 0);
      if (allZeroBuys) {
        showAllZeroBuys = true;
        if (showAllCheckbox) showAllCheckbox.checked = true;
      }
      renderPairsTable(job, showAllZeroBuys);
    } else {
      lastJob = null;
    }
  }

  if (showAllCheckbox) {
    showAllCheckbox.addEventListener('change', () => {
      showAllZeroBuys = showAllCheckbox.checked;
      if (lastJob) renderPairsTable(lastJob, showAllZeroBuys);
    });
  }

  load();
})();
