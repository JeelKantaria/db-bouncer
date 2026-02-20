package api

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>DBBouncer Dashboard</title>
<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
:root,[data-theme="dark"]{
  --bg:#0f1117;--bg-card:#161b22;--bg-card-hover:#1c2129;--bg-input:#0d1117;
  --border:#30363d;--text:#e1e4e8;--text-muted:#8b949e;--text-dim:#484f58;
  --primary:#58a6ff;--primary-hover:#79b8ff;
  --green:#3fb950;--red:#f85149;--yellow:#d29922;--orange:#db6d28;
  --radius:8px;--radius-sm:4px;
}
[data-theme="light"]{
  --bg:#f6f8fa;--bg-card:#ffffff;--bg-card-hover:#f3f4f6;--bg-input:#f0f1f3;
  --border:#d0d7de;--text:#1f2328;--text-muted:#656d76;--text-dim:#8b949e;
  --primary:#0969da;--primary-hover:#0550ae;
  --green:#1a7f37;--red:#cf222e;--yellow:#9a6700;--orange:#bc4c00;
}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;background:var(--bg);color:var(--text);line-height:1.5;min-height:100vh}
a{color:var(--primary);text-decoration:none}
button{cursor:pointer;font-family:inherit;font-size:inherit}

/* Layout */
.container{max-width:1400px;margin:0 auto;padding:0 24px 48px}

/* Header */
header{background:var(--bg-card);border-bottom:1px solid var(--border);padding:12px 24px;position:sticky;top:0;z-index:100}
.header-inner{max-width:1400px;margin:0 auto;display:flex;align-items:center;gap:16px;flex-wrap:wrap}
.header-title{font-size:20px;font-weight:700;display:flex;align-items:center;gap:10px}
.header-title svg{width:24px;height:24px;fill:var(--primary)}
.header-badges{display:flex;gap:8px;align-items:center;margin-left:auto}
.badge{display:inline-flex;align-items:center;gap:4px;padding:2px 10px;border-radius:12px;font-size:12px;font-weight:600;border:1px solid var(--border)}
.badge-healthy{color:var(--green);border-color:var(--green)}
.badge-unhealthy{color:var(--red);border-color:var(--red)}
.badge-port{color:var(--text-muted);font-weight:400}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.dot-green{background:var(--green)}.dot-red{background:var(--red)}.dot-gray{background:var(--text-dim)}
.refresh-controls{display:flex;align-items:center;gap:8px}
.refresh-controls label{font-size:13px;color:var(--text-muted)}
.refresh-controls select{background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);padding:2px 6px;font-size:13px}
.toggle{position:relative;width:36px;height:20px;display:inline-block}
.toggle input{opacity:0;width:0;height:0}
.toggle .slider{position:absolute;inset:0;background:var(--border);border-radius:10px;transition:.2s}
.toggle .slider::before{content:'';position:absolute;width:16px;height:16px;left:2px;bottom:2px;background:var(--text-muted);border-radius:50%;transition:.2s}
.toggle input:checked+.slider{background:var(--primary)}
.toggle input:checked+.slider::before{transform:translateX(16px);background:#fff}

/* Summary cards */
.summary{display:grid;grid-template-columns:repeat(4,1fr);gap:16px;margin:24px 0}
.card{background:var(--bg-card);border:1px solid var(--border);border-radius:var(--radius);padding:20px}
.card-label{font-size:12px;text-transform:uppercase;letter-spacing:.5px;color:var(--text-muted);margin-bottom:4px}
.card-value{font-size:32px;font-weight:700;line-height:1.2}
.card-value.danger{color:var(--red)}
.card.danger-card{border-color:var(--red)}

/* Toolbar */
.toolbar{display:flex;align-items:center;gap:12px;margin-bottom:16px;flex-wrap:wrap}
.toolbar .search{flex:1;min-width:200px;background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius);padding:8px 12px;font-size:14px;outline:none}
.toolbar .search:focus{border-color:var(--primary)}
.btn{display:inline-flex;align-items:center;gap:6px;padding:8px 16px;border-radius:var(--radius);font-size:14px;font-weight:500;border:1px solid var(--border);background:var(--bg-card);color:var(--text);transition:.15s}
.btn:hover{background:var(--bg-card-hover)}
.btn-primary{background:var(--primary);border-color:var(--primary);color:#fff}
.btn-primary:hover{background:var(--primary-hover);border-color:var(--primary-hover)}
.btn-danger{color:var(--red);border-color:var(--red)}
.btn-danger:hover{background:rgba(248,81,73,.15)}
.btn-sm{padding:4px 10px;font-size:12px}
.btn-icon{padding:4px 8px}

/* Add tenant form */
.add-panel{background:var(--bg-card);border:1px solid var(--border);border-radius:var(--radius);margin-bottom:16px;overflow:hidden;max-height:0;opacity:0;transition:max-height .3s,opacity .3s,padding .3s;padding:0 20px}
.add-panel.open{max-height:600px;opacity:1;padding:20px}
.add-panel h3{margin-bottom:16px;font-size:16px}
.form-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px}
.form-group{display:flex;flex-direction:column;gap:4px}
.form-group label{font-size:12px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.3px}
.form-group input,.form-group select{background:var(--bg-input);color:var(--text);border:1px solid var(--border);border-radius:var(--radius-sm);padding:8px 10px;font-size:14px;outline:none}
.form-group input:focus,.form-group select:focus{border-color:var(--primary)}
.form-actions{display:flex;gap:8px;margin-top:16px}

/* Table */
.table-wrap{background:var(--bg-card);border:1px solid var(--border);border-radius:var(--radius);overflow:auto}
table{width:100%;border-collapse:collapse;font-size:14px}
thead{position:sticky;top:0;background:var(--bg-card);z-index:1}
th{text-align:left;padding:12px 16px;font-weight:600;color:var(--text-muted);border-bottom:1px solid var(--border);white-space:nowrap;font-size:12px;text-transform:uppercase;letter-spacing:.5px}
td{padding:10px 16px;border-bottom:1px solid var(--border);white-space:nowrap}
tbody tr{cursor:pointer;transition:.1s}
tbody tr:hover{background:var(--bg-card-hover)}
tbody tr:last-child td{border-bottom:none}
.health-badge{display:inline-flex;align-items:center;gap:5px;padding:2px 8px;border-radius:12px;font-size:12px;font-weight:600}
.health-healthy{color:var(--green);background:rgba(63,185,80,.12)}
.health-unhealthy{color:var(--red);background:rgba(248,81,73,.12)}
.health-unknown{color:var(--text-dim);background:rgba(72,79,88,.2)}
.pool-bar{width:120px;height:8px;background:var(--border);border-radius:4px;overflow:hidden;display:flex}
.pool-bar .active{background:var(--primary);height:100%}
.pool-bar .idle{background:var(--green);height:100%}
.actions-cell{display:flex;gap:4px}
.empty-state{text-align:center;padding:60px 20px;color:var(--text-muted)}
.empty-state h3{margin-bottom:8px;font-size:18px;color:var(--text)}

/* Modal */
.modal-overlay{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:200;display:flex;align-items:center;justify-content:center;opacity:0;pointer-events:none;transition:.2s}
.modal-overlay.open{opacity:1;pointer-events:auto}
.modal{background:var(--bg-card);border:1px solid var(--border);border-radius:var(--radius);width:90%;max-width:700px;max-height:85vh;overflow-y:auto;padding:24px}
.modal h2{margin-bottom:20px;display:flex;align-items:center;gap:10px}
.modal-close{margin-left:auto;background:none;border:none;color:var(--text-muted);font-size:20px;padding:4px 8px;border-radius:var(--radius-sm)}
.modal-close:hover{color:var(--text);background:var(--border)}
.modal-section{margin-bottom:20px}
.modal-section h4{font-size:13px;text-transform:uppercase;letter-spacing:.5px;color:var(--text-muted);margin-bottom:8px}
.detail-grid{display:grid;grid-template-columns:repeat(2,1fr);gap:8px}
.detail-item{display:flex;flex-direction:column}
.detail-item .label{font-size:11px;color:var(--text-dim);text-transform:uppercase}
.detail-item .value{font-size:14px;font-weight:500}
.modal-actions{display:flex;gap:8px;padding-top:16px;border-top:1px solid var(--border)}

/* Confirm dialog */
.confirm-overlay{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:300;display:flex;align-items:center;justify-content:center;opacity:0;pointer-events:none;transition:.2s}
.confirm-overlay.open{opacity:1;pointer-events:auto}
.confirm-box{background:var(--bg-card);border:1px solid var(--border);border-radius:var(--radius);padding:24px;max-width:420px;text-align:center}
.confirm-box p{margin:12px 0 20px;color:var(--text-muted)}
.confirm-box .confirm-actions{display:flex;justify-content:center;gap:8px}

/* Toast */
.toast-stack{position:fixed;bottom:20px;right:20px;z-index:400;display:flex;flex-direction:column-reverse;gap:8px}
.toast{padding:12px 16px;border-radius:var(--radius);font-size:14px;font-weight:500;box-shadow:0 4px 12px rgba(0,0,0,.3);animation:toast-in .3s ease;min-width:280px;display:flex;align-items:center;gap:8px}
.toast-success{background:var(--bg-card);border:1px solid var(--green);color:var(--green)}
.toast-error{background:var(--bg-card);border:1px solid var(--red);color:var(--red)}
@keyframes toast-in{from{transform:translateX(100%);opacity:0}to{transform:translateX(0);opacity:1}}

/* Paused badge & row */
.health-paused{color:var(--orange);background:rgba(219,109,40,.12)}
.paused-tag{display:inline-flex;align-items:center;gap:4px;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600;color:var(--orange);background:rgba(219,109,40,.12);margin-left:6px}
.paused-row{opacity:.55}
.paused-row:hover{opacity:.75}

/* Row warnings */
.row-warning{border-left:3px solid var(--yellow)}
.row-danger{border-left:3px solid var(--red);background:rgba(248,81,73,.05)}

/* Sort */
th.sortable{cursor:pointer;user-select:none}
th.sortable:hover{color:var(--text)}
.sort-arrow{font-size:10px;margin-left:4px;color:var(--text-dim)}
th.sortable.sort-active .sort-arrow{color:var(--primary)}

/* Bulk bar */
.bulk-bar{position:fixed;bottom:0;left:0;right:0;background:var(--bg-card);border-top:1px solid var(--border);padding:12px 24px;display:flex;align-items:center;gap:12px;z-index:150;transform:translateY(100%);transition:transform .2s;box-shadow:0 -2px 12px rgba(0,0,0,.2)}
.bulk-bar.visible{transform:translateY(0)}
.bulk-bar .bulk-count{font-size:14px;font-weight:600;color:var(--text)}
.bulk-bar .bulk-spacer{flex:1}
td.cb-cell,th.cb-cell{width:40px;text-align:center;padding:10px 8px}
td.cb-cell input,th.cb-cell input{width:16px;height:16px;cursor:pointer;accent-color:var(--primary)}

/* Theme toggle */
.theme-btn{background:none;border:1px solid var(--border);color:var(--text-muted);border-radius:var(--radius-sm);padding:4px 8px;font-size:16px;line-height:1;cursor:pointer}
.theme-btn:hover{color:var(--text);border-color:var(--text-muted)}

/* Alert badge & panel */
.alert-btn{position:relative;background:none;border:1px solid var(--border);color:var(--text-muted);border-radius:var(--radius-sm);padding:4px 10px;font-size:14px;cursor:pointer}
.alert-btn:hover{color:var(--text);border-color:var(--text-muted)}
.alert-count{position:absolute;top:-6px;right:-6px;min-width:18px;height:18px;border-radius:9px;background:var(--red);color:#fff;font-size:11px;font-weight:700;display:flex;align-items:center;justify-content:center;padding:0 4px}
.alert-count.hidden{display:none}
.alert-list{list-style:none;padding:0;margin:0 0 16px}
.alert-list li{padding:8px 12px;border:1px solid var(--border);border-radius:var(--radius-sm);margin-bottom:6px;font-size:13px;display:flex;align-items:center;gap:8px}
.alert-list li.warn{border-left:3px solid var(--yellow);color:var(--yellow)}
.alert-list li.crit{border-left:3px solid var(--red);color:var(--red)}
.alert-empty{text-align:center;padding:20px;color:var(--text-muted);font-size:14px}
.threshold-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}

/* Status bar */
.status-bar{display:flex;flex-wrap:wrap;gap:16px;padding:16px 0;border-bottom:1px solid var(--border);margin-bottom:0;font-size:13px;color:var(--text-muted);align-items:center}
.status-bar .status-item{display:flex;align-items:center;gap:6px}
.status-bar .status-item .status-label{color:var(--text-dim);font-size:11px;text-transform:uppercase;letter-spacing:.3px}
.status-bar .status-item .status-value{color:var(--text);font-weight:500}

/* Config panel */
.config-panel{background:var(--bg-card);border:1px solid var(--border);border-radius:var(--radius);margin-bottom:16px;overflow:hidden;max-height:0;opacity:0;transition:max-height .3s,opacity .3s,padding .3s;padding:0 20px}
.config-panel.open{max-height:400px;opacity:1;padding:20px}
.config-panel h3{margin-bottom:16px;font-size:16px}
.config-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px}
.config-item{display:flex;flex-direction:column;gap:2px}
.config-item .label{font-size:11px;color:var(--text-dim);text-transform:uppercase;letter-spacing:.3px}
.config-item .value{font-size:14px;font-weight:500;color:var(--text)}

/* Responsive */
@media(max-width:900px){.summary{grid-template-columns:repeat(2,1fr)}.form-grid{grid-template-columns:1fr 1fr}.detail-grid{grid-template-columns:1fr}.config-grid{grid-template-columns:1fr 1fr}}
@media(max-width:600px){.summary{grid-template-columns:1fr}.header-badges{margin-left:0}.header-inner{gap:8px}.status-bar{flex-direction:column;gap:8px}}
</style>
</head>
<body>

<header>
  <div class="header-inner">
    <div class="header-title">
      <svg viewBox="0 0 24 24"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z"/></svg>
      DBBouncer
    </div>
    <span id="overallBadge" class="badge badge-healthy"><span class="dot dot-green"></span> Healthy</span>
    <span id="portsBadge" class="badge badge-port"></span>
    <div class="header-badges">
      <button class="alert-btn" id="alertsBtn" title="Alerts">Alerts <span class="alert-count hidden" id="alertCount">0</span></button>
      <button class="theme-btn" id="themeBtn" title="Toggle theme">&#9790;</button>
      <div class="refresh-controls">
        <label class="toggle">
          <input type="checkbox" id="autoRefresh" checked>
          <span class="slider"></span>
        </label>
        <label for="autoRefresh">Auto-refresh</label>
        <select id="refreshInterval">
          <option value="1000">1s</option>
          <option value="3000" selected>3s</option>
          <option value="5000">5s</option>
          <option value="10000">10s</option>
        </select>
      </div>
    </div>
  </div>
</header>

<div class="container">
  <!-- Status bar -->
  <div class="status-bar" id="statusBar">
    <div class="status-item"><span class="status-label">Uptime</span><span class="status-value" id="sUptime">-</span></div>
    <div class="status-item"><span class="status-label">Go</span><span class="status-value" id="sGoVer">-</span></div>
    <div class="status-item"><span class="status-label">Goroutines</span><span class="status-value" id="sGoroutines">-</span></div>
    <div class="status-item"><span class="status-label">Memory</span><span class="status-value" id="sMemory">-</span></div>
    <div class="status-item"><span class="status-label">PG Port</span><span class="status-value" id="sPgPort">-</span></div>
    <div class="status-item"><span class="status-label">MySQL Port</span><span class="status-value" id="sMysqlPort">-</span></div>
    <div class="status-item"><span class="status-label">API Port</span><span class="status-value" id="sApiPort">-</span></div>
  </div>

  <!-- Summary cards -->
  <div class="summary">
    <div class="card">
      <div class="card-label">Total Tenants</div>
      <div class="card-value" id="totalTenants">0</div>
    </div>
    <div class="card">
      <div class="card-label">Active Connections</div>
      <div class="card-value" id="activeConns">0</div>
    </div>
    <div class="card">
      <div class="card-label">Idle Connections</div>
      <div class="card-value" id="idleConns">0</div>
    </div>
    <div class="card" id="unhealthyCard">
      <div class="card-label">Unhealthy Tenants</div>
      <div class="card-value" id="unhealthyCount">0</div>
    </div>
  </div>

  <!-- Toolbar -->
  <div class="toolbar">
    <input type="text" class="search" id="searchInput" placeholder="Search tenants...">
    <button class="btn btn-primary" id="addTenantBtn">+ Add Tenant</button>
    <button class="btn" id="configBtn">Config</button>
    <button class="btn" id="exportBtn">Export</button>
    <button class="btn" id="importBtn">Import</button>
    <input type="file" id="importFile" accept=".json" style="display:none">
  </div>

  <!-- Config panel -->
  <div class="config-panel" id="configPanel">
    <h3>Running Configuration</h3>
    <div class="config-grid" id="configGrid">
      <div class="config-item"><span class="label">Loading...</span></div>
    </div>
  </div>

  <!-- Add tenant panel -->
  <div class="add-panel" id="addPanel">
    <h3>Add New Tenant</h3>
    <form id="addForm">
      <div class="form-grid">
        <div class="form-group">
          <label for="f-id">Tenant ID</label>
          <input type="text" id="f-id" required placeholder="e.g. customer-api">
        </div>
        <div class="form-group">
          <label for="f-dbtype">DB Type</label>
          <select id="f-dbtype" required>
            <option value="postgres">PostgreSQL</option>
            <option value="mysql">MySQL</option>
          </select>
        </div>
        <div class="form-group">
          <label for="f-host">Host</label>
          <input type="text" id="f-host" required placeholder="e.g. db.example.com">
        </div>
        <div class="form-group">
          <label for="f-port">Port</label>
          <input type="number" id="f-port" required placeholder="5432">
        </div>
        <div class="form-group">
          <label for="f-dbname">Database Name</label>
          <input type="text" id="f-dbname" required placeholder="e.g. mydb">
        </div>
        <div class="form-group">
          <label for="f-user">Username</label>
          <input type="text" id="f-user" required placeholder="e.g. dbuser">
        </div>
        <div class="form-group">
          <label for="f-pass">Password</label>
          <input type="password" id="f-pass" placeholder="optional">
        </div>
        <div class="form-group">
          <label for="f-minconn">Min Connections</label>
          <input type="number" id="f-minconn" placeholder="default">
        </div>
        <div class="form-group">
          <label for="f-maxconn">Max Connections</label>
          <input type="number" id="f-maxconn" placeholder="default">
        </div>
      </div>
      <div class="form-actions">
        <button type="submit" class="btn btn-primary">Create Tenant</button>
        <button type="button" class="btn" id="cancelAdd">Cancel</button>
      </div>
    </form>
  </div>

  <!-- Tenant table -->
  <div class="table-wrap">
    <table>
      <thead>
        <tr>
          <th class="cb-cell"><input type="checkbox" id="selectAll"></th>
          <th class="sortable" data-col="id">Tenant ID<span class="sort-arrow"></span></th>
          <th class="sortable" data-col="db_type">DB Type<span class="sort-arrow"></span></th>
          <th class="sortable" data-col="host">Host:Port<span class="sort-arrow"></span></th>
          <th class="sortable" data-col="health">Health<span class="sort-arrow"></span></th>
          <th class="sortable" data-col="active">Active<span class="sort-arrow"></span></th>
          <th class="sortable" data-col="idle">Idle<span class="sort-arrow"></span></th>
          <th class="sortable" data-col="total">Total<span class="sort-arrow"></span></th>
          <th class="sortable" data-col="waiting">Waiting<span class="sort-arrow"></span></th>
          <th>Pool Usage</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody id="tenantTableBody">
        <tr><td colspan="11" class="empty-state"><h3>No tenants found</h3>Loading data...</td></tr>
      </tbody>
    </table>
  </div>
</div>

<!-- Tenant detail modal -->
<div class="modal-overlay" id="detailModal">
  <div class="modal">
    <h2>
      <span id="modalTitle">Tenant Details</span>
      <button class="modal-close" id="modalClose">&times;</button>
    </h2>
    <div id="modalContent"></div>
    <div class="modal-actions" id="modalActions"></div>
  </div>
</div>

<!-- Confirm dialog -->
<div class="confirm-overlay" id="confirmOverlay">
  <div class="confirm-box">
    <h3 id="confirmTitle">Confirm</h3>
    <p id="confirmMsg"></p>
    <div class="confirm-actions">
      <button class="btn" id="confirmCancel">Cancel</button>
      <button class="btn btn-danger" id="confirmOk">Confirm</button>
    </div>
  </div>
</div>

<!-- Bulk action bar -->
<div class="bulk-bar" id="bulkBar">
  <span class="bulk-count" id="bulkCount">0 selected</span>
  <span class="bulk-spacer"></span>
  <button class="btn btn-sm" id="bulkDrain">Drain Selected</button>
  <button class="btn btn-sm btn-danger" id="bulkDelete">Delete Selected</button>
  <button class="btn btn-sm" id="bulkCancel">Cancel</button>
</div>

<!-- Alerts modal -->
<div class="modal-overlay" id="alertsModal">
  <div class="modal">
    <h2>
      <span>Alerts &amp; Thresholds</span>
      <button class="modal-close" id="alertsClose">&times;</button>
    </h2>
    <div class="modal-section">
      <h4>Active Alerts</h4>
      <div id="alertsList"></div>
    </div>
    <div class="modal-section">
      <h4>Threshold Settings</h4>
      <div class="threshold-grid">
        <div class="form-group">
          <label>Pool Usage Warning (%)</label>
          <input type="number" id="threshPoolWarn" min="1" max="100" value="80">
        </div>
        <div class="form-group">
          <label>Pool Usage Critical (%)</label>
          <input type="number" id="threshPoolCrit" min="1" max="100" value="95">
        </div>
      </div>
      <div class="form-actions" style="margin-top:12px">
        <button class="btn btn-primary btn-sm" id="saveThresholds">Save Thresholds</button>
      </div>
    </div>
  </div>
</div>

<!-- Toast stack -->
<div class="toast-stack" id="toastStack"></div>

<script>
(function() {
  'use strict';

  // --- State ---
  var tenants = [];
  var refreshTimer = null;
  var sortCol = '';
  var sortDir = 'asc';
  var selectedIds = {};
  var alerts = [];

  // --- DOM refs ---
  var g = function(id) { return document.getElementById(id); };
  var elTotalTenants = g('totalTenants');
  var elActiveConns = g('activeConns');
  var elIdleConns = g('idleConns');
  var elUnhealthyCount = g('unhealthyCount');
  var elUnhealthyCard = g('unhealthyCard');
  var elOverallBadge = g('overallBadge');
  var elPortsBadge = g('portsBadge');
  var elSearchInput = g('searchInput');
  var elTbody = g('tenantTableBody');
  var elAddPanel = g('addPanel');
  var elAddForm = g('addForm');
  var elAutoRefresh = g('autoRefresh');
  var elInterval = g('refreshInterval');
  var elToastStack = g('toastStack');
  var elConfigPanel = g('configPanel');
  var elConfigGrid = g('configGrid');
  var elBulkBar = g('bulkBar');
  var elBulkCount = g('bulkCount');
  var elAlertCount = g('alertCount');

  // --- API helpers ---
  var apiBase = window.location.origin;

  function apiFetch(path, opts) {
    opts = opts || {};
    var headers = { 'Content-Type': 'application/json' };
    if (opts.headers) {
      for (var k in opts.headers) headers[k] = opts.headers[k];
    }
    opts.headers = headers;
    return fetch(apiBase + path, opts).then(function(resp) {
      return resp.json().then(function(data) {
        if (!resp.ok) throw new Error(data.error || ('HTTP ' + resp.status));
        return data;
      });
    });
  }

  // --- Toast ---
  function toast(message, type) {
    type = type || 'success';
    var el = document.createElement('div');
    el.className = 'toast toast-' + type;
    el.textContent = message;
    elToastStack.appendChild(el);
    setTimeout(function() { el.style.opacity = '0'; el.style.transition = 'opacity .3s'; setTimeout(function() { el.remove(); }, 300); }, 3000);
  }

  // --- Confirm dialog ---
  function confirmDialog(title, message) {
    return new Promise(function(resolve) {
      g('confirmTitle').textContent = title;
      g('confirmMsg').textContent = message;
      g('confirmOverlay').classList.add('open');
      var cleanup = function(val) { g('confirmOverlay').classList.remove('open'); resolve(val); };
      g('confirmCancel').onclick = function() { cleanup(false); };
      g('confirmOk').onclick = function() { cleanup(true); };
    });
  }

  // --- Data fetching ---
  function fetchTenants() {
    return apiFetch('/tenants').then(function(data) {
      tenants = Array.isArray(data) ? data : [];
      render();
    }).catch(function() {
      tenants = [];
      render();
    });
  }

  function fetchHealth() {
    return apiFetch('/health').then(function(data) {
      var isHealthy = data.status === 'healthy';
      elOverallBadge.className = 'badge ' + (isHealthy ? 'badge-healthy' : 'badge-unhealthy');
      elOverallBadge.innerHTML = '<span class="dot ' + (isHealthy ? 'dot-green' : 'dot-red') + '"></span> ' + (isHealthy ? 'Healthy' : 'Unhealthy');
    }).catch(function() {
      elOverallBadge.className = 'badge badge-unhealthy';
      elOverallBadge.innerHTML = '<span class="dot dot-red"></span> Unreachable';
    });
  }

  function formatUptime(secs) {
    var d = Math.floor(secs / 86400);
    var h = Math.floor((secs % 86400) / 3600);
    var m = Math.floor((secs % 3600) / 60);
    var s = secs % 60;
    if (d > 0) return d + 'd ' + h + 'h ' + m + 'm';
    if (h > 0) return h + 'h ' + m + 'm ' + s + 's';
    if (m > 0) return m + 'm ' + s + 's';
    return s + 's';
  }

  function fetchStatus() {
    return apiFetch('/status').then(function(data) {
      g('sUptime').textContent = formatUptime(data.uptime_seconds || 0);
      g('sGoVer').textContent = data.go_version || '-';
      g('sGoroutines').textContent = data.goroutines || '-';
      g('sMemory').textContent = (data.memory_mb || 0).toFixed(1) + ' MB';
      if (data.listen) {
        g('sPgPort').textContent = data.listen.postgres_port || '-';
        g('sMysqlPort').textContent = data.listen.mysql_port || '-';
        g('sApiPort').textContent = data.listen.api_port || '-';
        elPortsBadge.textContent = 'PG:' + data.listen.postgres_port + ' | MySQL:' + data.listen.mysql_port + ' | API:' + data.listen.api_port;
      }
    }).catch(function() {});
  }

  function fetchConfig() {
    return apiFetch('/config').then(function(data) {
      var items = '';
      if (data.listen) {
        items += '<div class="config-item"><span class="label">PostgreSQL Port</span><span class="value">' + data.listen.postgres_port + '</span></div>';
        items += '<div class="config-item"><span class="label">MySQL Port</span><span class="value">' + data.listen.mysql_port + '</span></div>';
        items += '<div class="config-item"><span class="label">API Port</span><span class="value">' + data.listen.api_port + '</span></div>';
      }
      if (data.defaults) {
        items += '<div class="config-item"><span class="label">Min Connections</span><span class="value">' + data.defaults.min_connections + '</span></div>';
        items += '<div class="config-item"><span class="label">Max Connections</span><span class="value">' + data.defaults.max_connections + '</span></div>';
        items += '<div class="config-item"><span class="label">Idle Timeout</span><span class="value">' + data.defaults.idle_timeout + '</span></div>';
        items += '<div class="config-item"><span class="label">Max Lifetime</span><span class="value">' + data.defaults.max_lifetime + '</span></div>';
        items += '<div class="config-item"><span class="label">Acquire Timeout</span><span class="value">' + data.defaults.acquire_timeout + '</span></div>';
      }
      items += '<div class="config-item"><span class="label">Tenant Count</span><span class="value">' + (data.tenant_count || 0) + '</span></div>';
      elConfigGrid.innerHTML = items;
    }).catch(function() {});
  }

  function refreshData() {
    return Promise.all([fetchTenants(), fetchHealth(), fetchStatus()]);
  }

  // --- Thresholds ---
  function loadThresholds() {
    try {
      var saved = JSON.parse(localStorage.getItem('dbbouncer-thresholds'));
      if (saved) return saved;
    } catch(e) {}
    return { poolWarn: 80, poolCrit: 95 };
  }
  function saveThresholds(t) {
    localStorage.setItem('dbbouncer-thresholds', JSON.stringify(t));
  }
  var thresholds = loadThresholds();

  // --- Sort helper ---
  function getSortVal(t, col) {
    var cfg = t.config || {};
    var stats = t.stats || {};
    var hp = t.health || {};
    switch(col) {
      case 'id': return t.id.toLowerCase();
      case 'db_type': return (cfg.db_type || '').toLowerCase();
      case 'host': return (cfg.host || '').toLowerCase();
      case 'health': return t.paused ? 'paused' : (hp.status || 'unknown');
      case 'active': return stats.active || 0;
      case 'idle': return stats.idle || 0;
      case 'total': return stats.total || 0;
      case 'waiting': return stats.waiting || 0;
      default: return '';
    }
  }

  // --- Alerts check ---
  function checkAlerts() {
    alerts = [];
    tenants.forEach(function(t) {
      var stats = t.stats || {};
      var active = stats.active || 0;
      var maxC = stats.max_connections || 20;
      var pct = maxC > 0 ? (active / maxC) * 100 : 0;
      if (pct >= thresholds.poolCrit) {
        alerts.push({ type: 'crit', msg: t.id + ': pool at ' + Math.round(pct) + '% (critical)' });
      } else if (pct >= thresholds.poolWarn) {
        alerts.push({ type: 'warn', msg: t.id + ': pool at ' + Math.round(pct) + '% (warning)' });
      }
      var hp = t.health || {};
      if (hp.status === 'unhealthy') {
        alerts.push({ type: 'crit', msg: t.id + ': unhealthy (' + (hp.consecutive_failures || 0) + ' failures)' });
      }
    });
    elAlertCount.textContent = alerts.length;
    if (alerts.length > 0) {
      elAlertCount.classList.remove('hidden');
    } else {
      elAlertCount.classList.add('hidden');
    }
  }

  // --- Selection ---
  function updateBulkBar() {
    var count = Object.keys(selectedIds).length;
    elBulkCount.textContent = count + ' selected';
    if (count > 0) {
      elBulkBar.classList.add('visible');
    } else {
      elBulkBar.classList.remove('visible');
    }
  }

  // --- Render ---
  function render() {
    var filter = elSearchInput.value.toLowerCase();
    var filtered = tenants.filter(function(t) {
      return t.id.toLowerCase().indexOf(filter) !== -1 ||
        (t.config && t.config.db_type && t.config.db_type.toLowerCase().indexOf(filter) !== -1) ||
        (t.config && t.config.host && t.config.host.toLowerCase().indexOf(filter) !== -1);
    });

    // Sort
    if (sortCol) {
      filtered.sort(function(a, b) {
        var va = getSortVal(a, sortCol);
        var vb = getSortVal(b, sortCol);
        var cmp = 0;
        if (typeof va === 'number') { cmp = va - vb; }
        else { cmp = va < vb ? -1 : va > vb ? 1 : 0; }
        return sortDir === 'desc' ? -cmp : cmp;
      });
    }

    // Update sort arrows
    var ths = document.querySelectorAll('th.sortable');
    for (var i = 0; i < ths.length; i++) {
      var th = ths[i];
      var arrow = th.querySelector('.sort-arrow');
      if (th.getAttribute('data-col') === sortCol) {
        th.classList.add('sort-active');
        arrow.textContent = sortDir === 'asc' ? ' \\u25B2' : ' \\u25BC';
      } else {
        th.classList.remove('sort-active');
        arrow.textContent = '';
      }
    }

    // Summary
    var sumActive = 0, sumIdle = 0, countUnhealthy = 0, countPaused = 0;
    tenants.forEach(function(t) {
      if (t.stats) { sumActive += t.stats.active || 0; sumIdle += t.stats.idle || 0; }
      if (t.health && t.health.status === 'unhealthy') countUnhealthy++;
      if (t.paused) countPaused++;
    });
    elTotalTenants.textContent = tenants.length;
    elActiveConns.textContent = sumActive;
    elIdleConns.textContent = sumIdle;
    elUnhealthyCount.textContent = countUnhealthy;
    if (countUnhealthy > 0) {
      elUnhealthyCount.classList.add('danger');
      elUnhealthyCard.classList.add('danger-card');
    } else {
      elUnhealthyCount.classList.remove('danger');
      elUnhealthyCard.classList.remove('danger-card');
    }

    // Alerts
    checkAlerts();

    // Table
    if (filtered.length === 0) {
      elTbody.innerHTML = '<tr><td colspan="11" class="empty-state"><h3>No tenants found</h3>' +
        (tenants.length === 0 ? 'Add a tenant to get started' : 'Try a different search') + '</td></tr>';
      updateBulkBar();
      return;
    }

    elTbody.innerHTML = filtered.map(function(t) {
      var cfg = t.config || {};
      var stats = t.stats || {};
      var hp = t.health || {};
      var active = stats.active || 0;
      var idle = stats.idle || 0;
      var maxC = stats.max_connections || 20;
      var waiting = stats.waiting || 0;
      var total = stats.total || 0;
      var usagePct = maxC > 0 ? (active / maxC) * 100 : 0;
      var activePct = Math.min(100, usagePct);
      var idlePct = Math.min(100 - activePct, (idle / maxC) * 100);
      var isPaused = t.paused;
      var hStatus = isPaused ? 'paused' : (hp.status || 'unknown');
      var hClass = isPaused ? 'health-paused' : (hStatus === 'healthy' ? 'health-healthy' : hStatus === 'unhealthy' ? 'health-unhealthy' : 'health-unknown');
      var dotClass = isPaused ? 'dot-gray' : (hStatus === 'healthy' ? 'dot-green' : hStatus === 'unhealthy' ? 'dot-red' : 'dot-gray');
      var pauseBtn = isPaused
        ? '<button class="btn btn-sm" onclick="window._resumeTenant(\'' + esc(t.id) + '\')" title="Resume">Resume</button>'
        : '<button class="btn btn-sm" onclick="window._pauseTenant(\'' + esc(t.id) + '\')" title="Pause">Pause</button>';
      var eid = esc(t.id);
      var rowClasses = [];
      if (isPaused) rowClasses.push('paused-row');
      if (usagePct >= thresholds.poolCrit) rowClasses.push('row-danger');
      else if (usagePct >= thresholds.poolWarn) rowClasses.push('row-warning');
      var checked = selectedIds[t.id] ? ' checked' : '';
      return '<tr data-id="' + eid + '"' + (rowClasses.length ? ' class="' + rowClasses.join(' ') + '"' : '') + ' onclick="window._openDetail(\'' + eid + '\')">' +
        '<td class="cb-cell" onclick="event.stopPropagation()"><input type="checkbox" data-tid="' + eid + '"' + checked + ' onchange="window._toggleSelect(\'' + eid + '\',this.checked)"></td>' +
        '<td><strong>' + eid + '</strong>' + (isPaused ? '<span class="paused-tag">PAUSED</span>' : '') + '</td>' +
        '<td>' + esc(cfg.db_type || '-') + '</td>' +
        '<td>' + esc(cfg.host || '-') + ':' + (cfg.port || '-') + '</td>' +
        '<td><span class="health-badge ' + hClass + '"><span class="dot ' + dotClass + '"></span>' + hStatus + '</span></td>' +
        '<td>' + active + '</td>' +
        '<td>' + idle + '</td>' +
        '<td>' + total + '</td>' +
        '<td>' + waiting + '</td>' +
        '<td><div class="pool-bar"><div class="active" style="width:' + activePct + '%"></div><div class="idle" style="width:' + idlePct + '%"></div></div></td>' +
        '<td class="actions-cell" onclick="event.stopPropagation()">' +
          pauseBtn +
          '<button class="btn btn-sm" onclick="window._drainTenant(\'' + eid + '\')" title="Drain">Drain</button>' +
          '<button class="btn btn-sm btn-danger" onclick="window._deleteTenant(\'' + eid + '\')" title="Delete">Delete</button>' +
        '</td>' +
      '</tr>';
    }).join('');
    updateBulkBar();
  }

  function esc(s) {
    if (s == null) return '';
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
  }

  function or(val, fallback) { return val != null ? val : fallback; }

  // --- Actions ---
  window._drainTenant = function(id) {
    confirmDialog('Drain Tenant', 'Drain all connections for "' + id + '"?').then(function(ok) {
      if (!ok) return;
      apiFetch('/tenants/' + encodeURIComponent(id) + '/drain', { method: 'POST' }).then(function() {
        toast('Tenant "' + id + '" drained');
        refreshData();
      }).catch(function(e) { toast(e.message, 'error'); });
    });
  };

  window._deleteTenant = function(id) {
    confirmDialog('Delete Tenant', 'Permanently delete "' + id + '"? This will drain all connections.').then(function(ok) {
      if (!ok) return;
      apiFetch('/tenants/' + encodeURIComponent(id), { method: 'DELETE' }).then(function() {
        toast('Tenant "' + id + '" deleted');
        closeModal();
        refreshData();
      }).catch(function(e) { toast(e.message, 'error'); });
    });
  };

  window._pauseTenant = function(id) {
    confirmDialog('Pause Tenant', 'Pause routing for "' + id + '"? New connections will be rejected.').then(function(ok) {
      if (!ok) return;
      apiFetch('/tenants/' + encodeURIComponent(id) + '/pause', { method: 'POST' }).then(function() {
        toast('Tenant "' + id + '" paused');
        refreshData();
      }).catch(function(e) { toast(e.message, 'error'); });
    });
  };

  window._resumeTenant = function(id) {
    apiFetch('/tenants/' + encodeURIComponent(id) + '/resume', { method: 'POST' }).then(function() {
      toast('Tenant "' + id + '" resumed');
      refreshData();
    }).catch(function(e) { toast(e.message, 'error'); });
  };

  // --- Detail modal ---
  window._openDetail = function(id) {
    var t = tenants.find(function(x) { return x.id === id; });
    if (!t) return;
    var cfg = t.config || {};
    var stats = t.stats || {};
    var hp = t.health || {};
    var isPaused = t.paused;
    var hStatus = isPaused ? 'paused' : (hp.status || 'unknown');
    var hClass = isPaused ? 'health-paused' : (hStatus === 'healthy' ? 'health-healthy' : hStatus === 'unhealthy' ? 'health-unhealthy' : 'health-unknown');

    g('modalTitle').innerHTML = esc(t.id) + (isPaused ? ' <span class="paused-tag">PAUSED</span>' : '');
    g('modalContent').innerHTML =
      '<div class="modal-section">' +
        '<h4>Configuration</h4>' +
        '<div class="detail-grid">' +
          '<div class="detail-item"><span class="label">DB Type</span><span class="value">' + esc(cfg.db_type) + '</span></div>' +
          '<div class="detail-item"><span class="label">Host</span><span class="value">' + esc(cfg.host) + ':' + or(cfg.port, '-') + '</span></div>' +
          '<div class="detail-item"><span class="label">Database</span><span class="value">' + esc(cfg.dbname) + '</span></div>' +
          '<div class="detail-item"><span class="label">Username</span><span class="value">' + esc(cfg.username) + '</span></div>' +
          '<div class="detail-item"><span class="label">Min Connections</span><span class="value">' + (cfg.min_connections != null ? cfg.min_connections : 'default') + '</span></div>' +
          '<div class="detail-item"><span class="label">Max Connections</span><span class="value">' + (cfg.max_connections != null ? cfg.max_connections : 'default') + '</span></div>' +
        '</div>' +
      '</div>' +
      '<div class="modal-section">' +
        '<h4>Health</h4>' +
        '<div class="detail-grid">' +
          '<div class="detail-item"><span class="label">Status</span><span class="value"><span class="health-badge ' + hClass + '">' + hStatus + '</span></span></div>' +
          '<div class="detail-item"><span class="label">Last Check</span><span class="value">' + (hp.last_check ? new Date(hp.last_check).toLocaleString() : '-') + '</span></div>' +
          '<div class="detail-item"><span class="label">Consecutive Failures</span><span class="value">' + (hp.consecutive_failures || 0) + '</span></div>' +
          (hp.last_error ? '<div class="detail-item"><span class="label">Last Error</span><span class="value" style="color:var(--red)">' + esc(hp.last_error) + '</span></div>' : '') +
        '</div>' +
      '</div>' +
      '<div class="modal-section">' +
        '<h4>Pool Statistics</h4>' +
        '<div class="detail-grid">' +
          '<div class="detail-item"><span class="label">Active</span><span class="value">' + (stats.active || 0) + '</span></div>' +
          '<div class="detail-item"><span class="label">Idle</span><span class="value">' + (stats.idle || 0) + '</span></div>' +
          '<div class="detail-item"><span class="label">Total</span><span class="value">' + (stats.total || 0) + '</span></div>' +
          '<div class="detail-item"><span class="label">Waiting</span><span class="value">' + (stats.waiting || 0) + '</span></div>' +
          '<div class="detail-item"><span class="label">Max Connections</span><span class="value">' + (stats.max_connections || '-') + '</span></div>' +
          '<div class="detail-item"><span class="label">Pool Exhausted</span><span class="value">' + (stats.pool_exhausted_total || 0) + '</span></div>' +
        '</div>' +
      '</div>' +
      '<div class="modal-section">' +
        '<h4>Edit Configuration</h4>' +
        '<form id="editForm">' +
          '<div class="form-grid">' +
            '<div class="form-group"><label>Host</label><input type="text" id="e-host" value="' + esc(cfg.host) + '"></div>' +
            '<div class="form-group"><label>Port</label><input type="number" id="e-port" value="' + or(cfg.port, '') + '"></div>' +
            '<div class="form-group"><label>Database</label><input type="text" id="e-dbname" value="' + esc(cfg.dbname) + '"></div>' +
            '<div class="form-group"><label>Username</label><input type="text" id="e-user" value="' + esc(cfg.username) + '"></div>' +
            '<div class="form-group"><label>Password</label><input type="password" id="e-pass" placeholder="unchanged"></div>' +
            '<div class="form-group"><label>Min Conn</label><input type="number" id="e-minconn" value="' + (cfg.min_connections != null ? cfg.min_connections : '') + '" placeholder="default"></div>' +
            '<div class="form-group"><label>Max Conn</label><input type="number" id="e-maxconn" value="' + (cfg.max_connections != null ? cfg.max_connections : '') + '" placeholder="default"></div>' +
          '</div>' +
        '</form>' +
      '</div>';

    var pauseResumeBtn = isPaused
      ? '<button class="btn" onclick="window._resumeTenant(\'' + esc(t.id) + '\');closeModal()">Resume</button>'
      : '<button class="btn" onclick="window._pauseTenant(\'' + esc(t.id) + '\');closeModal()">Pause</button>';

    g('modalActions').innerHTML =
      '<button class="btn btn-primary" id="saveEditBtn">Save Changes</button>' +
      pauseResumeBtn +
      '<button class="btn" onclick="window._drainTenant(\'' + esc(t.id) + '\')">Drain</button>' +
      '<button class="btn btn-danger" onclick="window._deleteTenant(\'' + esc(t.id) + '\')">Delete</button>';

    g('detailModal').classList.add('open');

    // Bind save
    g('saveEditBtn').onclick = function() {
      var body = {};
      var host = g('e-host').value.trim();
      var port = parseInt(g('e-port').value);
      var dbname = g('e-dbname').value.trim();
      var user = g('e-user').value.trim();
      var pass = g('e-pass').value;
      var minC = g('e-minconn').value;
      var maxC = g('e-maxconn').value;
      if (host) body.host = host;
      if (port) body.port = port;
      if (dbname) body.dbname = dbname;
      if (user) body.username = user;
      if (pass) body.password = pass;
      if (minC !== '') body.min_connections = parseInt(minC);
      if (maxC !== '') body.max_connections = parseInt(maxC);

      apiFetch('/tenants/' + encodeURIComponent(t.id), { method: 'PUT', body: JSON.stringify(body) }).then(function() {
        toast('Tenant "' + t.id + '" updated');
        closeModal();
        refreshData();
      }).catch(function(e) { toast(e.message, 'error'); });
    };
  };

  function closeModal() {
    g('detailModal').classList.remove('open');
  }
  window.closeModal = closeModal;
  g('modalClose').onclick = closeModal;
  g('detailModal').onclick = function(e) { if (e.target === this) closeModal(); };

  // --- Add tenant ---
  g('addTenantBtn').onclick = function() { elAddPanel.classList.toggle('open'); };
  g('cancelAdd').onclick = function() { elAddPanel.classList.remove('open'); };

  elAddForm.onsubmit = function(e) {
    e.preventDefault();
    var body = {
      id: g('f-id').value.trim(),
      db_type: g('f-dbtype').value,
      host: g('f-host').value.trim(),
      port: parseInt(g('f-port').value),
      dbname: g('f-dbname').value.trim(),
      username: g('f-user').value.trim(),
      password: g('f-pass').value
    };
    var minC = g('f-minconn').value;
    var maxC = g('f-maxconn').value;
    if (minC !== '') body.min_connections = parseInt(minC);
    if (maxC !== '') body.max_connections = parseInt(maxC);

    apiFetch('/tenants', { method: 'POST', body: JSON.stringify(body) }).then(function() {
      toast('Tenant "' + body.id + '" created');
      elAddForm.reset();
      elAddPanel.classList.remove('open');
      refreshData();
    }).catch(function(e) { toast(e.message, 'error'); });
  };

  // --- Search ---
  elSearchInput.oninput = function() { render(); };

  // --- Auto-refresh ---
  function startRefresh() {
    stopRefresh();
    if (elAutoRefresh.checked) {
      var interval = parseInt(elInterval.value);
      refreshTimer = setInterval(refreshData, interval);
    }
  }
  function stopRefresh() {
    if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null; }
  }
  elAutoRefresh.onchange = startRefresh;
  elInterval.onchange = startRefresh;

  // --- Config panel ---
  g('configBtn').onclick = function() {
    var isOpen = elConfigPanel.classList.toggle('open');
    if (isOpen) fetchConfig();
  };

  // --- Column sorting ---
  document.querySelectorAll('th.sortable').forEach(function(th) {
    th.onclick = function() {
      var col = th.getAttribute('data-col');
      if (sortCol === col) {
        sortDir = sortDir === 'asc' ? 'desc' : 'asc';
      } else {
        sortCol = col;
        sortDir = 'asc';
      }
      render();
    };
  });

  // --- Select all / toggle ---
  window._toggleSelect = function(id, checked) {
    if (checked) { selectedIds[id] = true; } else { delete selectedIds[id]; }
    updateBulkBar();
  };
  g('selectAll').onchange = function() {
    var checked = this.checked;
    var boxes = elTbody.querySelectorAll('input[type="checkbox"]');
    selectedIds = {};
    if (checked) {
      for (var i = 0; i < boxes.length; i++) {
        boxes[i].checked = true;
        var tid = boxes[i].getAttribute('data-tid');
        if (tid) selectedIds[tid] = true;
      }
    } else {
      for (var i = 0; i < boxes.length; i++) { boxes[i].checked = false; }
    }
    updateBulkBar();
  };

  // --- Bulk actions ---
  g('bulkCancel').onclick = function() {
    selectedIds = {};
    g('selectAll').checked = false;
    render();
  };
  g('bulkDrain').onclick = function() {
    var ids = Object.keys(selectedIds);
    if (ids.length === 0) return;
    confirmDialog('Bulk Drain', 'Drain connections for ' + ids.length + ' tenant(s)?').then(function(ok) {
      if (!ok) return;
      var promises = ids.map(function(id) {
        return apiFetch('/tenants/' + encodeURIComponent(id) + '/drain', { method: 'POST' });
      });
      Promise.all(promises).then(function() {
        toast(ids.length + ' tenant(s) drained');
        selectedIds = {};
        g('selectAll').checked = false;
        refreshData();
      }).catch(function(e) { toast(e.message, 'error'); });
    });
  };
  g('bulkDelete').onclick = function() {
    var ids = Object.keys(selectedIds);
    if (ids.length === 0) return;
    confirmDialog('Bulk Delete', 'Permanently delete ' + ids.length + ' tenant(s)? This cannot be undone.').then(function(ok) {
      if (!ok) return;
      var promises = ids.map(function(id) {
        return apiFetch('/tenants/' + encodeURIComponent(id), { method: 'DELETE' });
      });
      Promise.all(promises).then(function() {
        toast(ids.length + ' tenant(s) deleted');
        selectedIds = {};
        g('selectAll').checked = false;
        refreshData();
      }).catch(function(e) { toast(e.message, 'error'); });
    });
  };

  // --- Export ---
  g('exportBtn').onclick = function() {
    var exportData = tenants.map(function(t) {
      var cfg = t.config || {};
      var item = { id: t.id, db_type: cfg.db_type, host: cfg.host, port: cfg.port, dbname: cfg.dbname, username: cfg.username };
      if (cfg.min_connections != null) item.min_connections = cfg.min_connections;
      if (cfg.max_connections != null) item.max_connections = cfg.max_connections;
      return item;
    });
    var blob = new Blob([JSON.stringify(exportData, null, 2)], { type: 'application/json' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = 'dbbouncer-tenants.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    toast('Exported ' + exportData.length + ' tenant(s)');
  };

  // --- Import ---
  g('importBtn').onclick = function() { g('importFile').click(); };
  g('importFile').onchange = function(e) {
    var file = e.target.files[0];
    if (!file) return;
    var reader = new FileReader();
    reader.onload = function(ev) {
      try {
        var data = JSON.parse(ev.target.result);
        if (!Array.isArray(data)) throw new Error('Expected JSON array');
        confirmDialog('Import Tenants', 'Import ' + data.length + ' tenant(s)? Existing tenants with same ID will be skipped.').then(function(ok) {
          if (!ok) return;
          var success = 0, fail = 0;
          var promises = data.map(function(item) {
            return apiFetch('/tenants', { method: 'POST', body: JSON.stringify(item) })
              .then(function() { success++; })
              .catch(function() { fail++; });
          });
          Promise.all(promises).then(function() {
            toast('Imported: ' + success + ' created, ' + fail + ' failed');
            refreshData();
          });
        });
      } catch(err) {
        toast('Invalid JSON file: ' + err.message, 'error');
      }
    };
    reader.readAsText(file);
    e.target.value = '';
  };

  // --- Theme toggle ---
  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    g('themeBtn').innerHTML = theme === 'light' ? '&#9728;' : '&#9790;';
    localStorage.setItem('dbbouncer-theme', theme);
  }
  g('themeBtn').onclick = function() {
    var current = localStorage.getItem('dbbouncer-theme') || 'dark';
    applyTheme(current === 'dark' ? 'light' : 'dark');
  };
  // Load saved theme
  var savedTheme = localStorage.getItem('dbbouncer-theme') || 'dark';
  applyTheme(savedTheme);

  // --- Alerts modal ---
  g('alertsBtn').onclick = function() {
    renderAlerts();
    g('alertsModal').classList.add('open');
  };
  g('alertsClose').onclick = function() { g('alertsModal').classList.remove('open'); };
  g('alertsModal').onclick = function(e) { if (e.target === this) this.classList.remove('open'); };

  function renderAlerts() {
    var el = g('alertsList');
    if (alerts.length === 0) {
      el.innerHTML = '<div class="alert-empty">No active alerts</div>';
    } else {
      el.innerHTML = '<ul class="alert-list">' + alerts.map(function(a) {
        return '<li class="' + a.type + '">' + esc(a.msg) + '</li>';
      }).join('') + '</ul>';
    }
    g('threshPoolWarn').value = thresholds.poolWarn;
    g('threshPoolCrit').value = thresholds.poolCrit;
  }

  g('saveThresholds').onclick = function() {
    var w = parseInt(g('threshPoolWarn').value) || 80;
    var c = parseInt(g('threshPoolCrit').value) || 95;
    if (w < 1) w = 1; if (w > 100) w = 100;
    if (c < 1) c = 1; if (c > 100) c = 100;
    if (c <= w) c = w + 1;
    thresholds = { poolWarn: w, poolCrit: c };
    saveThresholds(thresholds);
    toast('Thresholds saved');
    render();
    renderAlerts();
  };

  // --- Init ---
  refreshData();
  startRefresh();
})();
</script>
</body>
</html>
`
