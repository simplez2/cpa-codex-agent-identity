const embeddedInCPAMC = new URLSearchParams(window.location.search).get('embed') === 'cpamc';
if (embeddedInCPAMC) document.documentElement.dataset.embed = 'cpamc';

const keyInput = document.querySelector('#management-key');
const connectionForm = document.querySelector('#connection-form');
const connectButton = document.querySelector('#connect');
const forgetButton = document.querySelector('#forget');
const refreshButton = document.querySelector('#refresh');
const connectionPill = document.querySelector('#connection-pill');
const statusBox = document.querySelector('#status');
const identitiesBox = document.querySelector('#identities');
const batchInput = document.querySelector('#batch-input');
const batchFile = document.querySelector('#batch-file');
const fileMeta = document.querySelector('#file-meta');
const atomicMode = document.querySelector('#atomic-mode');
const previewButton = document.querySelector('#preview-batch');
const importButton = document.querySelector('#import-batch');
const clearButton = document.querySelector('#clear-batch');
const resultCard = document.querySelector('#result-card');
const resultSummary = document.querySelector('#result-summary');
const resultBody = document.querySelector('#result-body');
const exportJSONButton = document.querySelector('#export-json');
const exportCSVButton = document.querySelector('#export-csv');

const summaryFields = {
  total: document.querySelector('#summary-total'),
  active: document.querySelector('#summary-active'),
  disabled: document.querySelector('#summary-disabled'),
  agent_identity: document.querySelector('#summary-agent'),
  personal_access_token: document.querySelector('#summary-pat'),
  unsynced: document.querySelector('#summary-unsynced')
};

let busy = false;
let lastReport = null;
let lastPreviewSource = '';
let lastPreviewAtomic = true;

keyInput.value = sessionStorage.getItem('cpaManagementKey') || '';

function managementKey() {
  return keyInput.value.trim();
}

function setStatus(message, kind) {
  statusBox.textContent = message;
  statusBox.className = 'status' + (kind ? ' ' + kind : '');
}

function setConnection(state, label) {
  connectionPill.className = 'pill ' + state;
  connectionPill.textContent = label;
}

function updateImportAvailability() {
  const summary = lastReport && lastReport.preview ? lastReport.summary || {} : {};
  const blocking = Boolean(atomicMode.checked && ((summary.invalid || 0) + (summary.upstream_unavailable || 0) > 0));
  importButton.disabled = busy || !lastPreviewSource || blocking || (summary.ready || 0) < 1;
}

function setBusy(value) {
  busy = value;
  [connectButton, forgetButton, refreshButton, previewButton, clearButton, exportJSONButton, exportCSVButton].forEach(function (button) {
    button.disabled = value;
  });
  updateImportAvailability();
}

async function api(path, options) {
  const key = managementKey();
  if (!key) throw new Error('请先输入 CPA 管理密码');
  const requestOptions = options || {};
  const headers = Object.assign({}, requestOptions.headers || {}, { Authorization: 'Bearer ' + key });
  if (requestOptions.body !== undefined && !headers['Content-Type']) headers['Content-Type'] = 'application/json';
  const response = await fetch('./api/' + path, Object.assign({}, requestOptions, { headers: headers }));
  let payload = {};
  try { payload = await response.json(); } catch (_) { payload = {}; }
  if (!response.ok) {
    if (response.status === 401) throw new Error('管理密码不正确');
    throw new Error(payload.error || '请求失败 (' + response.status + ')');
  }
  return payload;
}

function renderSummary(summary) {
  const value = summary || {};
  Object.keys(summaryFields).forEach(function (key) {
    summaryFields[key].textContent = String(value[key] || 0);
  });
}

function formatDate(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleString();
}

function credentialKindLabel(kind) {
  if (kind === 'agent_identity') return 'Agent Identity';
  if (kind === 'personal_access_token') return 'PAT';
  return kind || '未知';
}

function badge(text, kind) {
  const element = document.createElement('span');
  element.className = 'badge ' + kind;
  element.textContent = text;
  return element;
}

function appendMeta(container, text) {
  if (!text) return;
  const element = document.createElement('span');
  element.textContent = text;
  container.append(element);
}

function actionButton(text, className, action) {
  const button = document.createElement('button');
  button.type = 'button';
  button.textContent = text;
  button.className = className || 'secondary';
  button.addEventListener('click', action);
  return button;
}

function renderIdentities(items) {
  identitiesBox.replaceChildren();
  if (!items || !items.length) {
    const empty = document.createElement('div');
    empty.className = 'empty';
    empty.textContent = '还没有导入 Codex 凭证。';
    identitiesBox.append(empty);
    return;
  }
  items.forEach(function (item) {
    const row = document.createElement('article');
    row.className = 'identity';

    const info = document.createElement('div');
    const title = document.createElement('div');
    title.className = 'identity-title';
    const name = document.createElement('strong');
    name.textContent = item.id;
    title.append(name);
    title.append(badge(credentialKindLabel(item.credential_kind), 'neutral'));
    if (!item.channel_synced) title.append(badge('未同步', 'error'));
    else if (item.channel_disabled) title.append(badge('已停用', 'warn'));
    else title.append(badge('已启用', 'ok'));
    if (item.expired) title.append(badge('已过期', 'error'));

    const meta = document.createElement('div');
    meta.className = 'identity-meta';
    appendMeta(meta, item.email);
    appendMeta(meta, item.plan_type ? '计划：' + item.plan_type : '');
    appendMeta(meta, item.expires_at ? '到期：' + formatDate(item.expires_at) : '长期 / 未提供到期时间');
    appendMeta(meta, '导入：' + formatDate(item.created_at));
    appendMeta(meta, item.channel_auth_file ? 'CPA：' + item.channel_auth_file : '');
    info.append(title, meta);

    const actions = document.createElement('div');
    actions.className = 'identity-actions';
    actions.append(actionButton('刷新同步', 'secondary', function () { runIdentityAction(item.id, 'refresh'); }));
    if (item.channel_disabled) {
      actions.append(actionButton('启用', 'secondary', function () { runIdentityAction(item.id, 'enable'); }));
    } else {
      actions.append(actionButton('停用', 'secondary', function () { runIdentityAction(item.id, 'disable'); }));
    }
    actions.append(actionButton('删除', 'danger', function () { removeIdentity(item.id); }));
    if (!item.channel_managed) {
      Array.from(actions.querySelectorAll('button')).slice(0, 2).forEach(function (button) { button.disabled = true; });
    }
    row.append(info, actions);
    identitiesBox.append(row);
  });
}

async function refresh() {
  setBusy(true);
  try {
    const payload = await api('identities');
    sessionStorage.setItem('cpaManagementKey', managementKey());
    renderSummary(payload.summary);
    renderIdentities(payload.identities || []);
    setConnection('ok', '已连接');
    if (payload.channel_sync_error) setStatus(payload.channel_sync_error, 'error');
    else setStatus('已连接，共 ' + ((payload.summary && payload.summary.total) || 0) + ' 个 Codex 凭证。', 'ok');
  } catch (error) {
    setConnection('error', '连接失败');
    setStatus(error.message, 'error');
    renderSummary({});
    identitiesBox.replaceChildren();
  } finally {
    setBusy(false);
  }
}

async function runIdentityAction(id, action) {
  setBusy(true);
  try {
    await api('identities/' + encodeURIComponent(id) + '/actions', {
      method: 'POST',
      body: JSON.stringify({ action: action })
    });
    setStatus(id + ' 已执行“' + action + '”。', 'ok');
    await refresh();
  } catch (error) {
    setStatus(error.message, 'error');
    setBusy(false);
  }
}

async function removeIdentity(id) {
  if (!window.confirm('删除 ' + id + ' 并撤销对应 CPA Codex 凭证？')) return;
  setBusy(true);
  try {
    await api('identities/' + encodeURIComponent(id), { method: 'DELETE' });
    setStatus(id + ' 已删除。', 'ok');
    await refresh();
  } catch (error) {
    setStatus(error.message, 'error');
    setBusy(false);
  }
}

async function readBatchSource() {
  if (batchFile.files && batchFile.files[0]) {
    const file = batchFile.files[0];
    if (file.size > 4 * 1024 * 1024) throw new Error('文件超过 4 MiB 限制');
    return await file.text();
  }
  return batchInput.value;
}

function markBatchChanged() {
  lastPreviewSource = '';
  lastPreviewAtomic = atomicMode.checked;
  if (lastReport && lastReport.preview) {
    setStatus('批量输入已变化，请重新预检。');
  }
  updateImportAvailability();
}

function clearSensitiveInput() {
  batchInput.value = '';
  batchFile.value = '';
  fileMeta.textContent = '支持 JSON 数组、JSONL 与逐行 TXT；文件内容不会上传到其他服务。';
  lastPreviewSource = '';
  updateImportAvailability();
}

function statusLabel(status) {
  const labels = {
    ready: '可导入',
    imported: '已导入',
    duplicate: '重复',
    invalid: '无效',
    upstream_unavailable: '上游不可用',
    failed: '失败',
    rolled_back: '已回滚',
    rollback_failed: '回滚失败',
    aborted: '已中止'
  };
  return labels[status] || status;
}

function renderReport(report) {
  lastReport = report;
  resultCard.classList.remove('hidden');
  resultSummary.replaceChildren();
  resultBody.replaceChildren();
  const summary = report.summary || {};
  const fields = [
    ['总数', summary.total], ['可导入', summary.ready], ['已导入', summary.imported],
    ['重复', summary.duplicate], ['无效', summary.invalid], ['上游不可用', summary.upstream_unavailable],
    ['失败', summary.failed], ['已回滚', summary.rolled_back], ['回滚失败', summary.rollback_failed], ['中止', summary.aborted]
  ];
  fields.forEach(function (entry) {
    if (!entry[1] && entry[0] !== '总数') return;
    const chip = document.createElement('span');
    chip.textContent = entry[0] + '：' + (entry[1] || 0);
    resultSummary.append(chip);
  });
  (report.items || []).forEach(function (item) {
    const row = document.createElement('tr');
    const index = document.createElement('td');
    index.textContent = String(item.index);
    const identityCell = document.createElement('td');
    identityCell.className = 'result-id';
    const label = document.createElement('strong');
    label.textContent = item.label || '未命名';
    const id = document.createElement('code');
    id.textContent = item.identity_id || '';
    identityCell.append(label, id);
    const kind = document.createElement('td');
    kind.textContent = credentialKindLabel(item.credential_kind);
    const account = document.createElement('td');
    account.textContent = item.email || item.plan_type || '—';
    const status = document.createElement('td');
    const statusPill = document.createElement('span');
    statusPill.className = 'result-status ' + item.status;
    statusPill.textContent = statusLabel(item.status);
    status.append(statusPill);
    const message = document.createElement('td');
    message.textContent = item.message || item.code || '—';
    row.append(index, identityCell, kind, account, status, message);
    resultBody.append(row);
  });
  updateImportAvailability();
}

async function previewBatch() {
  const source = (await readBatchSource()).trim();
  if (!source) throw new Error('请粘贴凭证或选择文件');
  const report = await api('identities/import/batch?preview=true&atomic=' + String(atomicMode.checked), {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    body: source
  });
  lastPreviewSource = source;
  lastPreviewAtomic = atomicMode.checked;
  renderReport(report);
  const summary = report.summary || {};
  if ((summary.invalid || 0) + (summary.upstream_unavailable || 0) > 0) {
    setStatus('预检完成，存在需要处理的条目。原子模式下不会执行导入。', 'error');
  } else {
    setStatus('预检完成，可导入 ' + (summary.ready || 0) + ' 条，重复 ' + (summary.duplicate || 0) + ' 条。', 'ok');
  }
}

async function commitBatch() {
  const source = (await readBatchSource()).trim();
  if (!source) throw new Error('敏感输入已清空，请重新选择待导入内容');
  if (source !== lastPreviewSource || atomicMode.checked !== lastPreviewAtomic) {
    await previewBatch();
    throw new Error('输入或原子模式已变化，已重新预检；请确认结果后再次点击导入');
  }
  if (!window.confirm('确认导入预检通过的凭证？原始 token 将写入 AES-256-GCM 加密存储。')) return;
  const report = await api('identities/import/batch?preview=false&atomic=' + String(atomicMode.checked), {
    method: 'POST',
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    body: source
  });
  renderReport(report);
  clearSensitiveInput();
  if (report.transaction === 'rolled_back' || report.transaction === 'rollback_failed') {
    setStatus('批量导入未完整提交，事务状态：' + report.transaction + '。', 'error');
  } else {
    setStatus('批量导入完成：新增 ' + ((report.summary && report.summary.imported) || 0) + ' 条。', 'ok');
  }
  await refresh();
}

async function runBatch(action) {
  setBusy(true);
  try {
    if (action === 'preview') await previewBatch();
    else await commitBatch();
  } catch (error) {
    setStatus(error.message, 'error');
  } finally {
    setBusy(false);
  }
}

function downloadBlob(name, type, content) {
  const blob = new Blob([content], { type: type });
  const link = document.createElement('a');
  const url = URL.createObjectURL(blob);
  link.href = url;
  link.download = name;
  document.body.append(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

function exportJSON() {
  if (!lastReport) return;
  downloadBlob('codex-agent-identity-import-report.json', 'application/json', JSON.stringify(lastReport, null, 2));
}

function csvCell(value) {
  return '"' + String(value === undefined || value === null ? '' : value).split('"').join('""') + '"';
}

function exportCSV() {
  if (!lastReport) return;
  const rows = [['index', 'label', 'identity_id', 'credential_kind', 'email', 'plan_type', 'status', 'code', 'message']];
  (lastReport.items || []).forEach(function (item) {
    rows.push([item.index, item.label, item.identity_id, item.credential_kind, item.email, item.plan_type, item.status, item.code, item.message]);
  });
  const csv = rows.map(function (row) { return row.map(csvCell).join(','); }).join(String.fromCharCode(13, 10));
  downloadBlob('codex-agent-identity-import-report.csv', 'text/csv;charset=utf-8', csv);
}

connectionForm.addEventListener('submit', function (event) {
  event.preventDefault();
  refresh();
});
refreshButton.addEventListener('click', refresh);
previewButton.addEventListener('click', function () { runBatch('preview'); });
importButton.addEventListener('click', function () { runBatch('import'); });
clearButton.addEventListener('click', function () { clearSensitiveInput(); setStatus('敏感输入已清空。'); });
exportJSONButton.addEventListener('click', exportJSON);
exportCSVButton.addEventListener('click', exportCSV);
batchInput.addEventListener('input', markBatchChanged);
atomicMode.addEventListener('change', markBatchChanged);
batchFile.addEventListener('change', function () {
  const file = batchFile.files && batchFile.files[0];
  fileMeta.textContent = file ? file.name + ' · ' + file.size + ' bytes' : '支持 JSON 数组、JSONL 与逐行 TXT；文件内容不会上传到其他服务。';
  markBatchChanged();
});
forgetButton.addEventListener('click', function () {
  sessionStorage.removeItem('cpaManagementKey');
  keyInput.value = '';
  identitiesBox.replaceChildren();
  renderSummary({});
  setConnection('neutral', '未连接');
  setStatus('管理密码已从当前标签页清除。');
});

if (new URLSearchParams(window.location.search).get('embed') === 'cpamc' && window.parent !== window) {
  let targetOrigin = '*';
  try { if (document.referrer) targetOrigin = new URL(document.referrer).origin; } catch (_) { targetOrigin = '*'; }
  window.parent.postMessage({ type: 'cpa-codex-agent-identity:ready' }, targetOrigin);
}

if (managementKey()) refresh();
