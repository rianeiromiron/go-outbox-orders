'use strict';

// Página de prueba de orders-api. Sin dependencias ni scripts en línea.
// Todo texto dinámico (incluido lo que devuelve la API, p. ej. el email escrito
// por el usuario) entra en el DOM como texto (textContent), nunca interpretado
// como HTML. Un test de Go (TestJSNeverInjectsHTML) vigila que siga así.

const HISTORY_KEY = 'orders-api-test-ui:history';
const HISTORY_MAX = 10;
const CURRENCIES = ['USD', 'EUR', 'MXN'];

const $ = (id) => document.getElementById(id);

// h construye un elemento: los hijos que son texto se insertan como texto.
function h(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else node.setAttribute(key, value);
  }
  node.append(...children);
  return node;
}

// ---------- formulario de pedido ----------

function toNumber(raw) {
  const s = String(raw).trim();
  return s === '' ? 0 : Number(s);
}

function addItem(values = {}) {
  const sku = h('input', { type: 'text', class: 'i-sku', autocomplete: 'off', value: values.sku ?? '' });
  const qty = h('input', { type: 'number', class: 'i-qty', step: '1', value: String(values.quantity ?? 1) });
  const price = h('input', { type: 'number', class: 'i-price', step: '1', value: String(values.unit_price_cents ?? 0) });
  const line = h('span', { class: 'line muted' });
  const remove = h('button', { type: 'button', class: 'btn-secondary btn-danger', text: 'Quitar' });

  const row = h('div', { class: 'item' },
    h('label', { class: 'sku' }, 'SKU', sku),
    h('label', {}, 'Cantidad', qty),
    h('label', {}, 'Precio unitario (centavos)', price),
    h('div', { class: 'item-tail' }, line, remove));
  remove.addEventListener('click', () => { row.remove(); recalc(); });
  $('items').append(row);
  recalc();
}

function readItems() {
  return [...$('items').querySelectorAll('.item')].map((row) => ({
    sku: row.querySelector('.i-sku').value,
    quantity: toNumber(row.querySelector('.i-qty').value),
    unit_price_cents: toNumber(row.querySelector('.i-price').value),
  }));
}

function payload() {
  return { customer_email: $('email').value, currency: $('currency').value, items: readItems() };
}

function money(cents, currency) {
  const major = cents / 100;
  try {
    return new Intl.NumberFormat('es', { style: 'currency', currency }).format(major);
  } catch {
    return `${major.toFixed(2)} ${currency}`.trim();
  }
}

function recalc() {
  let total = 0;
  for (const row of $('items').querySelectorAll('.item')) {
    const q = toNumber(row.querySelector('.i-qty').value);
    const p = toNumber(row.querySelector('.i-price').value);
    const sub = q * p;
    row.querySelector('.line').textContent = Number.isFinite(sub) ? `= ${sub} c` : '';
    total += Number.isFinite(sub) ? sub : 0;
  }
  $('total').textContent = `${money(total, $('currency').value)} · ${total} centavos`;
}

function fillForm({ email, currency, items }) {
  $('email').value = email;
  $('currency').value = currency;
  $('items').replaceChildren();
  items.forEach(addItem);
  recalc();
}

const PRESET_OK = {
  email: 'ana@example.com', currency: 'USD',
  items: [{ sku: 'SKU-1', quantity: 2, unit_price_cents: 1500 }, { sku: 'SKU-2', quantity: 1, unit_price_cents: 999 }],
};
const PRESET_BAD = {
  email: 'no-es-un-email', currency: 'usd',
  items: [{ sku: '', quantity: 0, unit_price_cents: -1 }],
};

// ---------- llamadas a la API y presentación del resultado ----------

async function call(method, path, body) {
  const started = performance.now();
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  try {
    const res = await fetch(path, init);
    const text = await res.text();
    let json;
    try { json = JSON.parse(text); } catch { json = undefined; }
    return {
      method, path, status: res.status, statusText: res.statusText,
      location: res.headers.get('Location'), text, json,
      ms: Math.round(performance.now() - started),
    };
  } catch (err) {
    return { method, path, networkError: err && err.message ? err.message : String(err) };
  }
}

function pill(text, kind) {
  return h('span', { class: `pill pill-${kind}`, text });
}

function renderResult(target, r) {
  target.replaceChildren();
  const head = h('div', { class: 'res-head' });

  if (r.networkError) {
    head.append(pill('sin respuesta', 'err'), h('span', { class: 'req', text: `${r.method} ${r.path}` }));
    target.append(head, h('p', { class: 'res-note', text: `No se pudo contactar con la API: ${r.networkError}` }));
    return;
  }

  const kind = r.status < 300 ? 'ok' : r.status < 500 ? 'warn' : 'err';
  head.append(
    pill(`${r.status} ${r.statusText}`.trim(), kind),
    h('span', { class: 'req', text: `${r.method} ${r.path}` }),
    h('span', { class: 'muted', text: `${r.ms} ms` }),
  );
  if (r.location) head.append(h('span', { class: 'muted', text: `Location: ${r.location}` }));
  target.append(head);

  const err = r.json && r.json.error;
  if (err) {
    const note = h('div', { class: 'res-note' }, h('strong', { text: `${err.code}: ` }), err.message);
    if (Array.isArray(err.details) && err.details.length) {
      note.append(h('ul', {}, ...err.details.map((d) => h('li', { text: d }))));
    }
    target.append(note);
  }

  const pretty = r.json !== undefined ? JSON.stringify(r.json, null, 2) : r.text;
  target.append(h('pre', {}, h('code', { text: pretty })));
}

// ---------- historial (solo en este navegador) ----------

function loadHistory() {
  try {
    const parsed = JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]');
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function saveHistory(list) {
  try { localStorage.setItem(HISTORY_KEY, JSON.stringify(list.slice(0, HISTORY_MAX))); } catch { /* sin almacenamiento: no pasa nada */ }
}

function remember(order) {
  const list = loadHistory().filter((o) => o.id !== order.id);
  list.unshift({
    id: order.id, email: order.customer_email, total: order.total_cents,
    currency: order.currency, at: Date.now(),
  });
  saveHistory(list);
  renderHistory();
}

function renderHistory() {
  const list = loadHistory();
  const ul = $('history');
  ul.replaceChildren();
  $('history-empty').hidden = list.length > 0;
  $('clear-history').hidden = list.length === 0;
  for (const o of list) {
    const btn = h('button', { type: 'button', class: 'btn-secondary', title: 'Consultar este pedido', text: o.id });
    btn.addEventListener('click', () => { $('order-id').value = o.id; getOrder(); });
    ul.append(h('li', {}, btn,
      h('span', { class: 'muted', text: `${o.email} · ${money(o.total, o.currency)} · ${new Date(o.at).toLocaleTimeString()}` })));
  }
}

// ---------- acciones ----------

async function getOrder() {
  const id = $('order-id').value.trim();
  if (!id) {
    renderResult($('get-result'), { method: 'GET', path: '/orders/{id}', networkError: 'escribe el id de un pedido' });
    return;
  }
  renderResult($('get-result'), await call('GET', `/orders/${encodeURIComponent(id)}`));
}

async function createOrder(body) {
  const r = await call('POST', '/orders', body);
  if (r.status === 201 && r.json && r.json.id) remember(r.json);
  return r;
}

function randomOrder(n) {
  const pick = (a) => a[Math.floor(Math.random() * a.length)];
  const items = Array.from({ length: 1 + Math.floor(Math.random() * 3) }, (_, i) => ({
    sku: `SKU-${n}-${i + 1}`,
    quantity: 1 + Math.floor(Math.random() * 5),
    unit_price_cents: 100 + Math.floor(Math.random() * 9900),
  }));
  return { customer_email: `cliente${n}@example.com`, currency: pick(CURRENCIES), items };
}

async function bulk() {
  const n = Math.min(50, Math.max(1, Math.trunc(toNumber($('bulk-n').value)) || 1));
  const btn = $('bulk-go');
  btn.disabled = true;
  let ok = 0;
  let failed = 0;
  for (let i = 1; i <= n; i++) {
    $('bulk-out').textContent = `enviando ${i}/${n}…`;
    const r = await createOrder(randomOrder(i));
    if (r.status === 201) ok++; else failed++;
  }
  $('bulk-out').textContent = `Listo: ${ok} creados (201), ${failed} con error.`;
  btn.disabled = false;
}

function curlCommand() {
  const body = JSON.stringify(payload()).replaceAll("'", "'\\''");
  return `curl -s -X POST ${location.origin}/orders -H 'Content-Type: application/json' -d '${body}'`;
}

async function copyText(text, button) {
  const original = button.textContent;
  try {
    await navigator.clipboard.writeText(text);
    button.textContent = 'Copiado ✓';
  } catch {
    button.textContent = 'No se pudo copiar';
  }
  setTimeout(() => { button.textContent = original; }, 1500);
}

// ---------- estado de la API ----------

async function refreshStatus() {
  const el = $('status');
  const set = (text, kind) => { el.className = `pill pill-${kind}`; el.textContent = text; };
  try {
    const res = await fetch('/readyz', { cache: 'no-store' });
    if (res.ok) set('API lista · base de datos OK', 'ok');
    else set(`API sin base de datos (${res.status})`, 'err');
  } catch {
    set('API no responde', 'err');
  }
}

// ---------- arranque ----------

document.addEventListener('DOMContentLoaded', () => {
  fillForm(PRESET_OK);
  renderHistory();
  refreshStatus();
  setInterval(refreshStatus, 5000);

  $('add-item').addEventListener('click', () => addItem({ sku: '', quantity: 1, unit_price_cents: 0 }));
  $('items').addEventListener('input', recalc);
  $('currency').addEventListener('input', recalc);
  $('preset-ok').addEventListener('click', () => fillForm(PRESET_OK));
  $('preset-bad').addEventListener('click', () => fillForm(PRESET_BAD));
  $('copy-curl').addEventListener('click', (e) => copyText(curlCommand(), e.currentTarget));
  $('bulk-go').addEventListener('click', bulk);

  $('order-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = e.submitter;
    if (btn) btn.disabled = true;
    const r = await createOrder(payload());
    renderResult($('create-result'), r);
    if (r.status === 201 && r.json && r.json.id) $('order-id').value = r.json.id;
    if (btn) btn.disabled = false;
  });

  $('get-form').addEventListener('submit', (e) => { e.preventDefault(); getOrder(); });

  $('clear-history').addEventListener('click', () => { saveHistory([]); renderHistory(); });

  for (const btn of document.querySelectorAll('button[data-copy]')) {
    btn.addEventListener('click', () => copyText($(btn.dataset.copy).textContent, btn));
  }
});
