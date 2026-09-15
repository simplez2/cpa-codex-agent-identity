// Execute the actual Go-rendered wrapper, not a reimplementation of its handlers.
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const input = JSON.parse(fs.readFileSync(0, 'utf8'));

function fixture(cryptoAvailable = true) {
  const listeners = {}, frameListeners = {}, controls = {}, messages = [], timers = new Map();
  const attributes = new Map(), values = new Map();
  let serial = 0, timerID = 0;
  const origin = 'https://cpa.example.test';
  const scope = origin + '/control';
  values.set('cli-proxy-auth', JSON.stringify({state: {apiBase: scope, managementKey: 'fixture-management-key'}}));
  const localStorage = {
    get length() { return values.size; },
    key(i) { return [...values.keys()][i] ?? null; },
    getItem(k) { return values.get(k) ?? null; }
  };
  const root = {
    dataset: {}, style: {removeProperty(){}, setProperty(){}},
    getAttribute(k) { return attributes.get(k) ?? null; },
    setAttribute(k, v) { attributes.set(k, v); }, removeAttribute(k) { attributes.delete(k); }
  };
  const frame = {
    dataset: {src: '/agent-identity/?embed=cpamc'}, src: '',
    contentWindow: {postMessage(data, origin) { messages.push({data, origin}); }},
    addEventListener(type, fn) { frameListeners[type] = fn; }
  };
  const document = {
    documentElement: root,
    getElementById(id) {
      if (id === 'identityFrame') return frame;
      return {addEventListener(type, fn) { controls[id + ':' + type] = fn; }};
    }
  };
  const window = {
    location: new URL(scope + '/v0/resource/plugins/codex-agent-identity/open'),
    parent: {document, location: new URL(scope + '/management.html')},
    localStorage, document,
    matchMedia() { return {matches: false, addEventListener(){}}; },
    getComputedStyle() { return {getPropertyValue() { return ''; }}; },
    addEventListener(type, fn) { listeners[type] = fn; },
    crypto: cryptoAvailable ? {randomUUID() { return 'nonce-fixture-' + String(++serial).padStart(16, '0'); }} : null,
    open() { throw Error('unexpected window creation'); }
  };
  const context = vm.createContext({
    window, document, localStorage, URL, TextEncoder, TextDecoder, Uint8Array,
    navigator: {userAgent: 'fixture'},
    setTimeout(fn) { timers.set(++timerID, fn); return timerID; },
    clearTimeout(id) { timers.delete(id); },
    MutationObserver: class { observe(){} }
  });
  vm.runInContext(input.script, context, {timeout: 2000});
  return {
    frame, frames: frameListeners, controls, listeners, messages, values, timers, attributes,
    auth() { return messages.filter(m => m.data.type === input.authType); },
    nonce() { return new URL(frame.src).searchParams.get('cpa_bridge'); },
    ready(extra = {}) {
      listeners.message(Object.assign({source: frame.contentWindow, origin: new URL(frame.src).origin,
        data: {type: input.readyType, nonce: this.nonce()}}, extra));
    },
    storage() { listeners.storage({key: 'cli-proxy-auth'}); }
  };
}

const f = fixture();
assert.match(f.frame.src, /\/control\/v0\/resource\/plugins\/codex-agent-identity\/ui\?/);
assert.equal(f.auth().length, 0);
f.frames.load(); f.storage();
assert.equal(f.auth().length, 0, 'load/storage must not send credentials before ready');
f.ready({source: {}});
f.ready({origin: 'https://other.example.test'});
f.ready({data: {type: input.readyType, nonce: 'wrong-nonce'}});
assert.equal(f.auth().length, 0, 'wrong source/origin/nonce must not authorize');
f.ready();
assert.equal(f.auth().length, 1);
assert.equal(f.auth()[0].origin, 'https://cpa.example.test');
assert.equal(f.auth()[0].data.managementKey, 'fixture-management-key');
f.frames.load();
assert.equal(f.auth().length, 1, 'load after ready must not resend the secret');
f.storage();
assert.equal(f.auth().length, 2, 'valid ready must preserve credential refresh');

const previousNonce = f.nonce();
f.listeners.message({source: f.frame.contentWindow, origin: new URL(f.frame.src).origin,
  data: {type: input.unavailableType, nonce: previousNonce}});
assert.notEqual(f.nonce(), previousNonce, 'failover must rotate the nonce');
f.frames.load(); f.storage();
f.ready({data: {type: input.readyType, nonce: previousNonce}});
assert.equal(f.auth().length, 2, 'stale same-origin document must not authorize the new candidate');
f.ready();
assert.equal(f.auth().length, 3);
assert.equal(f.auth()[2].data.nonce, f.nonce());

const retryNonce = f.nonce();
f.controls['retry:click']();
assert.notEqual(f.nonce(), retryNonce, 'manual retry must rotate the nonce');
f.storage();
assert.equal(f.auth().length, 3);
// Exhaust the finite candidate list with no successful handshakes.
for (let n = 0; f.timers.size && n < 10; n++) {
  const [id, callback] = [...f.timers][0];
  f.timers.delete(id); callback();
}
assert.equal(f.attributes.get('data-failed'), 'true');
f.storage();
assert.equal(f.auth().length, 3);

const noCrypto = fixture(false);
noCrypto.ready(); noCrypto.frames.load(); noCrypto.storage();
assert.equal(noCrypto.auth().length, 0, 'no cryptographic nonce means manual authentication only');
assert.equal(noCrypto.attributes.get('data-ready'), 'true', 'manual login must remain visible without crypto');
console.log('wrapper handshake, failover, retry, prefix and no-crypto checks passed');
