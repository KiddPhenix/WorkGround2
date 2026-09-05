// CDP helper: evaluate a JS expression in the page, print JSON result.
// Usage: node .tmp_cdp_eval.js <expr-file>
const fs = require('fs');

async function main() {
  const expr = fs.readFileSync(process.argv[2], 'utf8');
  const targets = await fetch('http://127.0.0.1:50475/json').then(r => r.json());
  const page = targets.find(t => t.type === 'page' && t.url.includes('xiebao18.com'));
  if (!page) { console.error('no page target'); process.exit(1); }
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  let id = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
  };
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  function call(method, params) {
    return new Promise((resolve) => {
      const mid = ++id;
      pending.set(mid, resolve);
      ws.send(JSON.stringify({ id: mid, method, params }));
    });
  }
  const r = await call('Runtime.evaluate', {
    expression: expr,
    returnByValue: true,
    awaitPromise: true,
  });
  if (r.result && r.result.exceptionDetails) {
    console.error('EXCEPTION:', JSON.stringify(r.result.exceptionDetails, null, 2));
  }
  console.log(JSON.stringify(r.result && r.result.result ? r.result.result.value : r, null, 2));
  ws.close();
}
main().catch(e => { console.error(e); process.exit(1); });
