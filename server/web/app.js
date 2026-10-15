'use strict';

// confdeck UI —— 无框架、无构建步骤。所有节点用 DOM API 构造（配置值是用户输入，
// 不能走 innerHTML）。

const TOKEN_KEY = 'confdeck.token';
const SCOPE_KEY = 'confdeck.scope';

const state = {
  token: sessionStorage.getItem(TOKEN_KEY) || '',
  scope: sessionStorage.getItem(SCOPE_KEY) || '',
  overview: null,
  dirty: new Map(), // key -> 新值（空串表示删除）
  revealed: new Set(),
  expanded: new Set(), // 值展开成多行输入的键
  descExpanded: new Set(), // 说明展开成全文的键
  search: '',
  filter: 'all',
};

const el = (id) => document.getElementById(id);

function h(tag, props = {}, children = []) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (value === null || value === undefined) continue;
    if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
    else node.setAttribute(key, value);
  }
  for (const child of [].concat(children)) if (child) node.appendChild(child);
  return node;
}

// ---------------------------------------------------------------- API

async function api(path, options = {}) {
  const headers = {};
  if (options.body) headers['Content-Type'] = 'application/json';
  if (state.token) headers['Authorization'] = 'Bearer ' + state.token;

  let res;
  try {
    res = await fetch(path, { ...options, headers });
  } catch (err) {
    throw new Error('请求失败：' + err.message);
  }
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = null; }
  }
  if (!res.ok) throw new Error((data && data.error) || res.status + ' ' + res.statusText);
  return data;
}

// ---------------------------------------------------------------- 提示

function setStatus(text, isError) {
  const node = el('status');
  node.textContent = text || '';
  node.className = isError ? 'status error' : 'status';
}

function showBanner(text, isError) {
  const node = el('banner');
  node.textContent = text;
  node.className = isError ? 'banner error' : 'banner';
  node.hidden = false;
}

function hideBanner() { el('banner').hidden = true; }

// ---------------------------------------------------------------- 数据

function currentScope() {
  if (!state.overview) return null;
  return state.overview.scopes.find((s) => s.scope === state.scope) || null;
}

function effectiveValue(key) {
  if (state.dirty.has(key.key)) return state.dirty.get(key.key);
  return key.value || '';
}

function isPlaceholder(value) {
  return typeof value === 'string' && value.trim().startsWith('replace-with-');
}

// 待删除：值被清空并等着保存（保存时下发空串 = 服务端删除该键）。
function isPendingDelete(key) {
  return state.dirty.has(key.key) && (state.dirty.get(key.key) || '') === '';
}

// 删除/撤销：清空输入框（复用 onValueInput 的脏值逻辑），再点一次恢复原值。
function toggleDelete(key) {
  const row = el('rows').querySelector('tr[data-key="' + CSS.escape(key.key) + '"]');
  const input = row ? row.querySelector('input, textarea') : null;
  if (!input) return;
  input.value = isPendingDelete(key) ? (key.value || '') : '';
  onValueInput(key, input);
}

function problemsOf(key) {
  const value = effectiveValue(key);
  const problems = [];
  if (key.undeclared) problems.push('未声明');
  if (key.sharedMismatch) problems.push('归属不一致');
  // 待删除的行不再报「值」层面的问题（覆盖共享值 / 必填未配 / 值不一致 …）：
  // 那些都是在描述当前值，而这一行马上要被删掉，报出来只会自相矛盾。
  if (isPendingDelete(key)) return problems;
  if (key.override) problems.push('覆盖共享值');
  if (!key.external) {
    // 由部署环境提供的项不走这段：中心看不到进程环境，缺了也不算问题
    // （进程侧启动时照样会被必填校验挡住）。
    if (value === '') {
      if (key.required) problems.push('必填未配');
    } else if (isPlaceholder(value)) {
      problems.push('需替换占位符');
    }
  }
  if (key.divergent) problems.push('值不一致');
  return problems;
}

async function load() {
  try {
    const overview = await api('/v1/overview');
    state.overview = overview;
    state.dirty.clear();
    if (!overview.scopes.some((s) => s.scope === state.scope)) {
      state.scope = overview.scopes.length ? overview.scopes[0].scope : '';
      sessionStorage.setItem(SCOPE_KEY, state.scope);
    }
    hideBanner();
    if (overview.insecure) {
      showBanner('服务端没有配置 token：任何能访问这个端口的人都能读写配置。', false);
    }
    setStatus('已同步 ' + new Date().toLocaleTimeString());
    render();
  } catch (err) {
    setStatus(err.message, true);
    showBanner('读取失败：' + err.message + '（若服务端开启了鉴权，请在左下角填入管理 token）', true);
    state.overview = null;
    render();
  }
}

// ---------------------------------------------------------------- 渲染

function render() {
  renderScopes();
  renderTable();
  updateSaveButton();
}

function renderScopes() {
  const nav = el('scopes');
  nav.textContent = '';

  if (!state.overview) {
    nav.appendChild(h('div', { class: 'hint', text: '（还没有数据）' }));
    return;
  }

  for (const scope of state.overview.scopes) {
    const missing = scope.missing ? scope.missing.length : 0;
    const label = missing > 0 ? missing + ' 缺' : String(scope.keys.length);
    const link = h('a', {
      href: '#',
      class: (scope.scope === state.scope ? 'active ' : '') + (missing > 0 ? 'has-alert' : ''),
      onclick: (event) => { event.preventDefault(); selectScope(scope.scope); },
    }, [
      h('span', { class: 'scope', text: scope.scope }),
      h('span', { class: 'count', text: label }),
    ]);
    nav.appendChild(link);
  }
}

function selectScope(scope) {
  if (scope === state.scope) return;
  if (state.dirty.size > 0 && !confirm('有未保存的修改，切换作用域会丢弃它们。继续？')) return;
  state.scope = scope;
  sessionStorage.setItem(SCOPE_KEY, scope);
  state.dirty.clear();
  state.revealed.clear();
  state.expanded.clear();
  state.descExpanded.clear();
  render();
}

function matchFilter(key) {
  const problems = problemsOf(key);
  switch (state.filter) {
    case 'alert': return problems.length > 0;
    case 'missing': return !key.external && effectiveValue(key) === '';
    case 'set': return effectiveValue(key) !== '';
    case 'undeclared': return !!key.undeclared;
    default: return true;
  }
}

function matchSearch(key) {
  if (!state.search) return true;
  const needle = state.search.toLowerCase();
  return key.key.toLowerCase().includes(needle) ||
    (key.desc || '').toLowerCase().includes(needle);
}

function renderTable() {
  const tbody = el('rows');
  tbody.textContent = '';
  const scope = currentScope();

  if (!scope) {
    el('empty').hidden = false;
    el('empty').textContent = state.overview
      ? '这个中心还没有任何作用域 —— 服务启动时注册 schema 之后就会出现在左侧。'
      : '还没有数据。';
    el('count').textContent = '';
    return;
  }

  const keys = scope.keys.filter((k) => matchFilter(k) && matchSearch(k));
  for (const key of keys) tbody.appendChild(rowFor(scope, key));

  el('empty').hidden = keys.length > 0;
  if (!keys.length) el('empty').textContent = '没有符合条件的键。';

  const missing = scope.missing ? scope.missing.length : 0;
  el('count').textContent = keys.length + ' / ' + scope.keys.length + ' 个键' +
    (missing > 0 ? ' · 缺 ' + missing + ' 个必填' : '');
}

function rowFor(scope, key) {
  const row = h('tr');
  row.dataset.key = key.key;

  row.appendChild(h('td', {}, [h('span', { class: 'key', text: key.key })]));

  // 值：敏感项默认打码；空串表示删除（保存时下发）。
  // 由部署环境提供的项不给就地编辑：它由 compose 之类的部署过程注入，
  // 在中心里改只会造成「界面显示一个值、进程里其实是另一个」的误解。
  const external = !!key.external;
  const revealed = state.revealed.has(key.key);
  const value = effectiveValue(key);
  // 长文本（连接串、JSON、多行密钥）用 textarea 更好输入：可以手动展开，
  // 值里带换行时自动展开。
  const multiline = state.expanded.has(key.key) || value.includes('\n');
  const common = {
    spellcheck: 'false',
    readonly: external ? 'readonly' : null,
    title: external ? '由部署环境提供（compose 等注入），不在这里改' : null,
    placeholder: external ? '由部署环境提供'
      : (key.default ? '默认 ' + key.default : '未配置'),
    oninput: external ? null : (event) => onValueInput(key, event.target),
  };
  const input = multiline
    ? h('textarea', { ...common, rows: '3' })
    : h('input', {
      ...common,
      type: key.secret && !revealed ? 'password' : 'text',
      autocomplete: 'off',
    });
  if (external) input.classList.add('external');
  input.value = value;
  if (state.dirty.has(key.key)) input.classList.add('dirty');

  const cell = h('div', { class: 'value-cell' }, [input]);
  if (!external) {
    cell.appendChild(h('button', {
      class: 'btn-quiet', type: 'button', text: multiline ? '收起' : '展开',
      title: multiline ? '收回单行' : '展开成多行（长文本更好输入）',
      onclick: () => {
        if (state.expanded.has(key.key)) state.expanded.delete(key.key);
        else state.expanded.add(key.key);
        renderTable();
      },
    }));
  }
  if (key.secret) {
    cell.appendChild(h('button', {
      class: 'btn-quiet', type: 'button', text: revealed ? '隐藏' : '显示',
      onclick: () => {
        if (state.revealed.has(key.key)) state.revealed.delete(key.key);
        else state.revealed.add(key.key);
        renderTable();
      },
    }));
  }
  // 删除：把这个键在**本作用域**里的值去掉（保存后生效）。
  // 主要用在两种「残留」上：中心里有值但已经没有服务声明它（应用最新上报的 schema 里没了），
  // 以及共享键在服务作用域里留下的副本（它会盖住 shared 的值）。
  // 由部署环境提供的项通常不给删，但它要是在中心里存了值（历史残留），也允许删掉。
  if (!external || key.present) {
    const pending = isPendingDelete(key);
    cell.appendChild(h('button', {
      class: 'btn-quiet btn-delete', type: 'button', text: pending ? '撤销' : '✕',
      title: pending ? '撤销删除（恢复原来的值）'
        : '删除这一项在本作用域里的值（保存后生效）',
      onclick: () => toggleDelete(key),
    }));
  }
  row.appendChild(h('td', {}, [cell]));

  row.appendChild(h('td', {}, [h('span', { class: 'type', text: key.type || '—' })]));
  row.appendChild(h('td', {}, [marksNode(key)]));

  // 说明：过长时截断，点一下展开全文（没被截断就不响应，免得点了没反应让人困惑）。
  const descText = key.desc || '';
  const desc = h('div', { class: 'desc', text: descText, title: descText });
  if (state.descExpanded.has(key.key)) desc.classList.add('expanded');
  desc.addEventListener('click', () => {
    const expanded = desc.classList.contains('expanded');
    if (!expanded && desc.scrollWidth <= desc.clientWidth + 2) return;
    desc.classList.toggle('expanded');
    if (expanded) state.descExpanded.delete(key.key);
    else state.descExpanded.add(key.key);
  });
  row.appendChild(h('td', {}, [desc]));

  if (problemsOf(key).length > 0) row.classList.add('alert');
  if (state.dirty.has(key.key) && effectiveValue(key) === '') row.classList.add('removed');
  return row;
}

function marksNode(key) {
  const problems = problemsOf(key);
  const marks = [];
  if (isPendingDelete(key)) {
    marks.push(h('span', { class: 'mark warn', text: '将删除（保存后生效）' }));
  }
  for (const problem of problems) {
    marks.push(h('span', { class: 'mark alarm', text: problem }));
  }
  if (key.fromShared) marks.push(h('span', { class: 'mark', text: '来自 shared' }));
  if (key.shared) marks.push(h('span', { class: 'mark', text: '共享键' }));
  if (key.external) marks.push(h('span', { class: 'mark', text: '部署提供' }));
  if (key.required && !key.external && !problems.includes('必填未配')) {
    marks.push(h('span', { class: 'mark', text: '必填' }));
  }
  if (key.secret) marks.push(h('span', { class: 'mark', text: '敏感' }));
  // 多个服务都声明了它 —— 把服务名写出来，比只说「多服务共用」有用
  if (key.declaredIn && key.declaredIn.length > 1) {
    marks.push(h('span', { class: 'mark', text: key.declaredIn.join('、') + ' 都声明' }));
  }
  return h('div', { class: 'marks' }, marks);
}

function onValueInput(key, input) {
  const original = key.value || '';
  if (input.value === original) state.dirty.delete(key.key);
  else state.dirty.set(key.key, input.value);

  input.classList.toggle('dirty', state.dirty.has(key.key));

  const row = el('rows').querySelector('tr[data-key="' + CSS.escape(key.key) + '"]');
  if (row) {
    row.classList.toggle('removed', state.dirty.has(key.key) && input.value === '');
    const marksCell = row.children[3];
    marksCell.textContent = '';
    marksCell.appendChild(marksNode(key));
    const problems = problemsOf(key);
    row.classList.toggle('alert', problems.length > 0);
  }
  updateSaveButton();
}

function updateSaveButton() {
  const button = el('save');
  button.disabled = state.dirty.size === 0;
  button.textContent = state.dirty.size > 0 ? '保存修改（' + state.dirty.size + '）' : '保存修改';
}

// ---------------------------------------------------------------- 动作

async function save() {
  const scope = currentScope();
  if (!scope || state.dirty.size === 0) return;

  const values = {};
  for (const [key, value] of state.dirty) values[key] = value;

  setStatus('保存中…');
  try {
    await api('/v1/values/' + encodeURIComponent(scope.scope), {
      method: 'PUT',
      body: JSON.stringify({ values }),
    });
    await load();
    setStatus('已保存 ' + new Date().toLocaleTimeString());
  } catch (err) {
    setStatus(err.message, true);
    showBanner('保存失败：' + err.message, true);
  }
}

function applyToken() {
  state.token = el('token').value.trim();
  if (state.token) sessionStorage.setItem(TOKEN_KEY, state.token);
  else sessionStorage.removeItem(TOKEN_KEY);
  load();
}

// ---------------------------------------------------------------- 启动

el('token').value = state.token;
el('token-apply').addEventListener('click', applyToken);
el('token').addEventListener('keydown', (event) => { if (event.key === 'Enter') applyToken(); });
el('refresh').addEventListener('click', load);
el('save').addEventListener('click', save);
el('search').addEventListener('input', (event) => { state.search = event.target.value.trim(); renderTable(); });
el('filter').addEventListener('change', (event) => { state.filter = event.target.value; renderTable(); });
window.addEventListener('beforeunload', (event) => {
  if (state.dirty.size > 0) { event.preventDefault(); event.returnValue = ''; }
});

load();
if (!state.token) el('token').focus();
