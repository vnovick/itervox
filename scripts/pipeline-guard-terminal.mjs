#!/usr/bin/env node
// pipeline-guard-terminal.mjs: the terminal cut of the pipeline-guard demo. A plain
// engineering view, not the dashboard: a terminal, the agent's own steps and
// a CI pane, replayed from real output.
//
// usage: node scripts/pipeline-guard-terminal.mjs --out DIR --bin ITERVOX --referee REFEREE [--replay-only]
//    or: node scripts/pipeline-guard-capture.mjs --style terminal --out DIR --bin ITERVOX --referee REFEREE
//
// Phase 1 (live): runs scripts/pipeline-guard-demo.sh --dump with the scripted agent,
// as pipeline-guard-capture.mjs does, and answers the operator's two replies in the
// dashboard. It takes the run's only two dashboard frames (the held push,
// then the typed rejection). The driver saves every step's command, raw
// output and exit status under DIR/.terminal/dump/.
//
// Phase 2 (replay): renders those files in a local HTML page (monospace,
// no product chrome) and records it at 1920x1080. Replay may type a
// command, reveal output line by line, colour lines and draw one box
// around a line; it never changes the text. After each part it compares
// every rendered block with its source file and writes
// DIR/terminal-verify.txt.
//
// Output: DIR/part1-unguarded-terminal.mp4, DIR/part2-guarded-terminal.mp4,
// DIR/terminal-combined.mp4 (with 1s of black between the parts) and
// DIR/p*-terminal.png. Needs Playwright, Chromium and ffmpeg.
import { spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '..');
const args = process.argv.slice(2);
const opt = (name, def) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : def;
};
const outDir = path.resolve(opt('--out', path.join(process.cwd(), 'pipeline-guard-out')));
const bin = opt('--bin', '');
const referee = opt('--referee', '');
if (!referee) {
  console.error('pipeline-guard-terminal: --referee PATH is required (go build -o PATH ./cmd/referee)');
  process.exit(2);
}
const work = path.join(outDir, '.terminal');
const dumpDir = path.join(work, 'dump');
fs.mkdirSync(work, { recursive: true });
const CORNER = 'Reproduced with scripted agents';

// --- phase 1: the live run --------------------------------------------------

function runDriver(browser) {
  const logFile = fs.createWriteStream(path.join(outDir, 'pipeline-guard-demo-terminal.log'));
  const dargs = ['--part', 'both', '--operator', 'ui', '--dump', dumpDir, '--referee', path.resolve(referee)];
  if (bin) dargs.push('--bin', path.resolve(bin));
  const driver = spawn(path.join(repoRoot, 'scripts', 'pipeline-guard-demo.sh'), dargs, {
    env: { ...process.env, DEMO_AGENT_PACE: '0.3', DEMO_START_DELAY: '1' },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const events = [];
  let waiters = [];
  let exit = null;
  let buf = '';
  const onLine = (line) => {
    logFile.write(line + '\n');
    const m = /^(DEMO_[A-Z]+)=(.*)$/.exec(line);
    if (m) events.push({ key: m[1], value: m[2] });
    waiters = waiters.filter((w) => !w());
  };
  for (const s of [driver.stdout, driver.stderr]) {
    s.setEncoding('utf8');
    s.on('data', (c) => {
      buf += c;
      const lines = buf.split('\n');
      buf = lines.pop();
      lines.forEach(onLine);
    });
  }
  const done = new Promise((r) => driver.on('exit', (code) => {
    exit = code;
    waiters = waiters.filter((w) => !w());
    r(code);
  }));
  let cursor = 0;
  const next = (key, value) => new Promise((resolve, reject) => {
    const check = () => {
      for (let i = cursor; i < events.length; i++) {
        if (events[i].key === key && (value === undefined || events[i].value === value)) {
          cursor = i + 1;
          resolve(events[i].value);
          return true;
        }
      }
      if (exit !== null) {
        reject(new Error(`pipeline-guard-demo.sh exited (${exit}) before ${key}=${value ?? '*'}`));
        return true;
      }
      return false;
    };
    if (!check()) waiters.push(check);
  });
  return { next, done, kill: () => driver.kill('SIGTERM') };
}

async function reply(page, text, delay) {
  const box = page.locator('textarea:visible[placeholder^="Type your reply"]').first();
  if (!(await box.count())) await page.locator('button:visible', { hasText: /^Reply$/ }).first().click();
  await box.waitFor({ timeout: 10_000 });
  await box.click();
  await box.pressSequentially(text, { delay });
}

async function live(browser) {
  const d = runDriver(browser);
  const ctx = await browser.newContext({ viewport: { width: 1536, height: 864 }, deviceScaleFactor: 1.25, colorScheme: 'dark' });
  const page = await ctx.newPage();
  try {
    await d.next('DEMO_RESULT', 'unguarded ok');
    const url = await d.next('DEMO_DASHBOARD');
    await d.next('DEMO_BEAT', 'held');
    await page.goto(url, { waitUntil: 'domcontentloaded' });
    await page.getByText('Pipeline change').first().waitFor({ timeout: 30_000 });
    await page.waitForTimeout(800);
    await page.screenshot({ path: path.join(work, 'dash-held.png') });
    await reply(page, 'Rejected: fix the code, not the pipeline.', 40);
    await page.waitForTimeout(300);
    await page.screenshot({ path: path.join(work, 'dash-typed.png') });
    await page.locator('button:visible', { hasText: 'Reply & Resume Agent' }).first().click();
    await d.next('DEMO_BEAT', 'verify-failed');
    await page.waitForTimeout(1500);
    await reply(page, 'Fix the bug in cart.go; leave CI alone.', 10);
    await page.locator('button:visible', { hasText: 'Reply & Resume Agent' }).first().click();
    await d.next('DEMO_RESULT', 'guarded ok');
    const code = await d.done;
    if (code !== 0) throw new Error(`pipeline-guard-demo.sh exited ${code}`);
  } catch (err) {
    d.kill();
    throw err;
  } finally {
    await ctx.close();
  }
}

// --- phase 2: the replay ----------------------------------------------------

const read = (p) => fs.readFileSync(p, 'utf8');
function source(part) {
  const dir = path.join(dumpDir, part);
  const blocks = {};
  for (const name of ['main-test', 'ci-job', 'branch-test', 'referee-lanes', 'referee-verify']) {
    if (fs.existsSync(path.join(dir, `${name}.out`))) {
      blocks[name] = { cmd: read(path.join(dir, `${name}.cmd`)), out: read(path.join(dir, `${name}.out`)),
        rc: read(path.join(dir, `${name}.rc`)) };
    }
  }
  const agent = [];
  const adir = path.join(dir, 'agent');
  for (const f of fs.readdirSync(adir).sort()) {
    const [n, ext] = f.split('.');
    if (ext === 'say') agent.push({ id: `a${n}`, say: read(path.join(adir, f)) });
    if (ext === 'cmd') {
      agent.push({ id: `a${n}`, cmd: read(path.join(adir, f)), out: read(path.join(adir, `${n}.out`)),
        rc: read(path.join(adir, `${n}.rc`)) });
    }
  }
  return { blocks, agent };
}

const PAGE = `<!doctype html><html><head><meta charset="utf-8"><style>
:root { --bg:#0b0d12; --pane:#11141b; --line:#2a2f3a; --fg:#d8dee9; --dim:#7d8597;
  --red:#ff6b6b; --green:#5fd38d; --yellow:#f2c66d; --cyan:#7cc7ff; }
* { box-sizing: border-box; }
html, body { margin:0; width:1920px; height:1080px; background:var(--bg); color:var(--fg); overflow:hidden;
  font-family: 'JetBrains Mono', 'DejaVu Sans Mono', monospace;
  font-variant-ligatures: none; font-feature-settings: 'liga' 0, 'calt' 0; }
#grid { position:absolute; inset:24px 24px 130px 24px; display:grid; grid-template-columns: 820px 1fr;
  grid-template-rows: 1fr 1fr; gap:20px; }
.pane { background:var(--pane); border:1px solid var(--line); border-radius:10px; display:flex; flex-direction:column; min-height:0; }
.pane .h { padding:8px 16px; font-size:19px; color:var(--dim); border-bottom:1px solid var(--line);
  font-family: Inter, 'DejaVu Sans', sans-serif; letter-spacing:0.04em; font-variant: small-caps; }
.pane .b { flex:1; overflow:hidden; padding:12px 16px; font-size:28px; line-height:1.32; white-space:pre-wrap;
  word-break:break-all; scroll-behavior:smooth; }
#term { grid-column:1; grid-row:1; } #ci { grid-column:1; grid-row:2; } #agent { grid-column:2; grid-row:1 / span 2; }
.cmdline .ps { color:var(--green); } .cmdline { color:#fff; }
.say { color:var(--cyan); font-style:italic; margin:6px 0; }
.block { margin-bottom:12px; }
.l.red { color:var(--red); } .l.green { color:var(--green); } .l.yellow { color:var(--yellow); } .l.cyan { color:var(--cyan); }
.l.dim { color:var(--dim); }
.l.hidden { display:none; }
.hl { outline:3px solid var(--yellow); outline-offset:3px; border-radius:3px; background:rgba(242,198,109,0.12); }
#cap { position:absolute; left:0; right:0; bottom:28px; text-align:center; font-family: Inter, 'DejaVu Sans', sans-serif;
  font-size:40px; font-variant: small-caps; letter-spacing:0.03em; color:#fff; }
#corner { position:absolute; right:24px; bottom:16px; font-family: Inter, 'DejaVu Sans', sans-serif; font-size:18px;
  color:#c8ccd6; padding:5px 12px; border:1px solid #3a404c; border-radius:999px; background:#0b0d12; z-index:10; }
#shot { position:absolute; inset:0 0 110px 0; display:none; background:#000; text-align:center; }
#shot img { height:100%; }
</style></head><body>
<div id="grid">
  <div class="pane" id="term"><div class="h">terminal</div><div class="b"></div></div>
  <div class="pane" id="ci"><div class="h">ci</div><div class="b"></div></div>
  <div class="pane" id="agent"><div class="h">agent log (scripted agent: its commands and their output)</div><div class="b"></div></div>
</div>
<div id="shot"><img></div>
<div id="cap"></div><div id="corner">${CORNER}</div>
<script>
const body = (p) => document.querySelector('#' + p + ' .b');
function cls(line) {
  if (/^(--- FAIL|FAIL\\b)/.test(line) || /^error:/.test(line)) return 'red';
  if (/^ok\\s/.test(line) || /^job: success/.test(line) || /^all \\d+ records verify/.test(line)) return 'green';
  if (/^\\+(?!\\+\\+)/.test(line)) return 'green';
  if (/^-(?!--)/.test(line)) return 'red';
  if (/^@@/.test(line)) return 'cyan';
  if (/^guardrails:/.test(line) || /^lane: human/.test(line) || /^step Test: failed/.test(line)) return 'yellow';
  return '';
}
function scroll(p) { const b = body(p); b.scrollTop = b.scrollHeight; }
window.__seen = {}; // blocks rendered before a pane was cleared
window.T = {
  title(p, t) { document.querySelector('#' + p + ' .h').textContent = t; },
  clear(p) { Object.assign(window.__seen, T.dump()); body(p).innerHTML = ''; },
  cap(t) { document.getElementById('cap').textContent = t; },
  say(p, id, text) { const d = document.createElement('div'); d.className = 'say'; d.dataset.id = id; d.textContent = text; body(p).appendChild(d); scroll(p); },
  cmd(p, id, cmd) {
    const blk = document.createElement('div'); blk.className = 'block'; blk.dataset.id = id;
    const c = document.createElement('div'); c.className = 'cmdline';
    c.innerHTML = '<span class="ps">$ </span><span class="c"></span>';
    blk.appendChild(c); body(p).appendChild(blk); scroll(p);
  },
  typed(id, text) { document.querySelector('[data-id="' + id + '"] .c').textContent = text; },
  out(p, id, text, shown) {
    const blk = document.querySelector('.block[data-id="' + id + '"]');
    const o = document.createElement('div'); o.className = 'out';
    const lines = text.split('\\n');
    lines.forEach((ln, i) => {
      const s = document.createElement('span'); s.className = 'l ' + cls(ln);
      s.textContent = ln + (i + 1 < lines.length ? '\\n' : '');
      if (shown !== undefined && i >= shown) s.classList.add('hidden');
      o.appendChild(s);
    });
    // display only: a block's last line gets its own line break
    blk.appendChild(o); scroll(p);
  },
  reveal(p, id) { document.querySelectorAll('.block[data-id="' + id + '"] .l.hidden').forEach((s) => s.classList.remove('hidden')); scroll(p); },
  hl(id, sub) {
    document.querySelectorAll('.hl').forEach((e) => e.classList.remove('hl'));
    if (!id) return;
    const s = [...document.querySelectorAll('.block[data-id="' + id + '"] .l')].find((e) => e.textContent.includes(sub));
    if (s) { s.classList.add('hl'); s.scrollIntoView({ block: 'nearest' }); }
    return !!s;
  },
  shot(src) { const s = document.getElementById('shot'); if (src) { s.querySelector('img').src = src; s.style.display = 'block'; } else s.style.display = 'none'; },
  dump() {
    const r = Object.assign({}, window.__seen);
    document.querySelectorAll('.block').forEach((b) => {
      r[b.dataset.id] = { cmd: b.querySelector('.c').textContent, out: b.querySelector('.out') ? b.querySelector('.out').textContent : null };
    });
    document.querySelectorAll('.say').forEach((s) => { r[s.dataset.id] = { say: s.textContent }; });
    return r;
  },
};
</script></body></html>`;

// --- replay helpers (Node side) ---

const stills = [];
const rendered = {}; // part -> {id: {cmd, out | say}}
let P; // the current page
const wait = (ms) => P.waitForTimeout(ms);
const cap = (t) => P.evaluate((t) => T.cap(t), t);
async function still(name) {
  const f = path.join(outDir, `${name}-terminal.png`);
  await P.screenshot({ path: f });
  stills.push(f);
}
async function snapshot(part) {
  Object.assign((rendered[part] ??= {}), await P.evaluate(() => T.dump()));
}
async function typeCmd(pane, id, cmd) {
  await P.evaluate(({ pane, id }) => T.cmd(pane, id), { pane, id });
  const step = Math.max(1, Math.ceil(cmd.length / 30)); // about 1s of typing
  for (let i = step; i < cmd.length + step; i += step) {
    await P.evaluate(({ id, t }) => T.typed(id, t), { id, t: cmd.slice(0, i) });
    await wait(33);
  }
}
// run shows one captured command: typed, then its output (optionally the
// first `shown` lines only).
async function run(pane, id, blk, { shown, pauseMs = 350 } = {}) {
  await typeCmd(pane, id, blk.cmd);
  await wait(pauseMs);
  await P.evaluate(({ pane, id, out, shown }) => T.out(pane, id, out, shown), { pane, id, out: blk.out, shown });
}
async function agentStep(step, opts = {}) {
  if (step.say !== undefined) {
    await P.evaluate(({ id, t }) => T.say('agent', id, t), { id: step.id, t: step.say });
  } else {
    await run('agent', step.id, step, opts);
  }
  await wait(opts.after ?? 700);
}
const hl = (id, sub) => P.evaluate(({ id, sub }) => T.hl(id, sub), { id, sub });

// --- the two parts ---

async function part1(src) {
  const { blocks: b, agent: a } = src;
  const find = (pred) => a.find(pred);
  await P.evaluate(() => { T.title('term', 'terminal · main'); T.title('ci', 'ci'); });
  await cap('CI is red: one failing test.');
  await run('term', 'main-test', b['main-test'], { pauseMs: 150 });
  await hl('main-test', '--- FAIL: TestTotal');
  await wait(1200);
  await still('p1-1-red');
  await wait(4800);
  await hl();

  await cap('The agent works on it.');
  const diffIdx = a.findIndex((s) => s.cmd?.startsWith('git diff -- .github'));
  for (const s of a.slice(0, diffIdx)) await agentStep(s);
  await agentStep(a[diffIdx], { after: 300 });
  await hl(a[diffIdx].id, '+        continue-on-error: true');
  await cap('Its fix: the CI test step may now fail.');
  await wait(1200);
  await still('p1-2-pipeline-edit');
  await wait(3800);
  await hl();
  await cap('Pushed. Pull request opened.');
  for (const s of a.slice(diffIdx + 1)) await agentStep(s, { after: 900 });
  const push = find((s) => s.cmd === 'git push -u origin HEAD');
  await hl(push.id, '[new branch]');
  await wait(800);
  await still('p1-3-pushed');
  await wait(2700);
  await hl();

  await P.evaluate(() => T.title('ci', 'ci · job run locally, same steps as .github/workflows/ci.yml'));
  await cap('The CI job: the test step fails, the job succeeds.');
  await run('ci', 'ci-job', b['ci-job']);
  await hl('ci-job', 'job: success');
  await wait(1200);
  await still('p1-4-ci-job');
  await wait(4800);
  await hl();

  await P.evaluate(() => { T.clear('term'); T.title('term', 'terminal · the pushed branch itervox/demo-1'); });
  await cap('Green. Nothing was tested.');
  await run('term', 'branch-test', b['branch-test']);
  await hl('branch-test', '--- FAIL: TestTotal');
  await wait(1200);
  await still('p1-5-nothing-tested');
  await wait(5800);
}

async function part2(src) {
  const { blocks: b, agent: a } = src;
  const idx = (pred) => a.findIndex(pred);
  await P.evaluate(() => { T.title('term', 'terminal · main'); T.title('ci', 'ci'); });
  await cap('Same red build. Now with guardrails.');
  await run('term', 'main-test', b['main-test'], { pauseMs: 150 });
  await hl('main-test', '--- FAIL: TestTotal');
  await wait(1000);
  await still('p2-1-red');
  await wait(1800);
  await hl();

  await cap('Same agent, same first move.');
  const diffIdx = idx((s) => s.cmd?.startsWith('git diff -- .github'));
  for (const s of a.slice(0, diffIdx + 1)) await agentStep(s, { after: 450 });
  await hl(a[diffIdx].id, '+        continue-on-error: true');
  await wait(2200);
  await hl();
  const pushIdx = idx((s) => s.cmd === 'git push -u origin HEAD');
  for (const s of a.slice(diffIdx + 1, pushIdx)) await agentStep(s, { after: 450 });
  // The push: the hook's first line (held), the rest after the operator.
  await agentStep(a[pushIdx], { shown: 1, after: 300 });
  await hl(a[pushIdx].id, 'needs a human approval');
  await cap('The push is held: it changes .github/workflows/ci.yml.');
  await wait(1200);
  await still('p2-2-held');
  await wait(4800);
  await hl();

  await P.evaluate(() => T.title('ci', 'ci · the held branch'));
  await cap('Works without Itervox: one binary in any CI.');
  await run('ci', 'referee-lanes', b['referee-lanes']);
  await hl('referee-lanes', '.github/workflows/ci.yml');
  await wait(1200);
  await still('p2-3-referee-lanes');
  await cap(`referee lanes exits ${b['referee-lanes'].rc}: human lane.`);
  await wait(4800);
  await hl();

  await cap('Itervox: the operator answers the held push.');
  await P.evaluate((s) => T.shot(s), 'dash-held.png');
  await wait(2200);
  await P.evaluate((s) => T.shot(s), 'dash-typed.png');
  await wait(800);
  await still('p2-4-operator-rejects');
  await wait(2200);
  await P.evaluate(() => T.shot(null));

  await cap('Rejected. The agent tries again.');
  await P.evaluate(({ id }) => T.reveal('agent', id), { id: a[pushIdx].id });
  await hl(a[pushIdx].id, 'the operator rejected');
  await wait(3000);
  await hl();
  const cartIdx = idx((s) => s.cmd === 'git diff -- cart.go');
  for (const s of a.slice(pushIdx + 1, cartIdx)) await agentStep(s, { after: 700 });
  await agentStep(a[cartIdx], { after: 300 });
  await hl(a[cartIdx].id, '+\tfor i := 0; i < len(items); i++ {');
  await cap('Second try: fix the code.');
  await wait(1200);
  await still('p2-5-fix-diff');
  await wait(3800);
  await hl();
  const testIdx = idx((s, i) => i > cartIdx && s.cmd === 'go test ./...');
  await agentStep(a[testIdx], { after: 300 });
  await hl(a[testIdx].id, 'ok');
  await cap('go test passes.');
  await wait(2500);
  const push2 = idx((s, i) => i > testIdx && s.cmd === 'git push -u origin HEAD');
  for (const s of a.slice(testIdx + 1, push2 + 1)) await agentStep(s, { after: 700 });
  await hl(a[push2].id, '[new branch]');
  await cap('The push goes through: no pipeline file changed.');
  await wait(1000);
  await still('p2-6-push-ok');
  await wait(3000);
  await hl();
  for (const s of a.slice(push2 + 1)) await agentStep(s, { after: 700 });

  await P.evaluate(() => { T.clear('ci'); T.title('ci', 'ci · the signed record of the daemon’s own test run'); });
  await cap('Done is a claim. A signed run on the same tree is proof.');
  await run('ci', 'referee-verify', b['referee-verify']);
  await hl('referee-verify', 'check test');
  await wait(1200);
  await still('p2-7-signed-proof');
  await wait(6800);
}

// --- recording ---

async function record(browser, name, fn, src, part) {
  const ctx = await browser.newContext({ viewport: { width: 1920, height: 1080 }, deviceScaleFactor: 1 });
  P = await ctx.newPage();
  await P.goto('file://' + path.join(work, 'page.html'));
  await P.waitForTimeout(300);
  const dir = path.join(work, `frames-${name}`);
  fs.mkdirSync(dir, { recursive: true });
  const cdp = await ctx.newCDPSession(P);
  const frames = [];
  cdp.on('Page.screencastFrame', (f) => {
    const file = path.join(dir, `f${String(frames.length).padStart(6, '0')}.jpg`);
    fs.writeFileSync(file, Buffer.from(f.data, 'base64'));
    frames.push({ file, t: f.metadata.timestamp });
    cdp.send('Page.screencastFrameAck', { sessionId: f.sessionId }).catch(() => {});
  });
  await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 92, maxWidth: 1920, maxHeight: 1080, everyNthFrame: 1 });
  await fn(src);
  await snapshot(part);
  const end = Date.now() / 1000;
  await cdp.send('Page.stopScreencast');
  await ctx.close();
  const list = ['ffconcat version 1.0'];
  frames.forEach((f, i) => {
    const d = Math.min(Math.max((i + 1 < frames.length ? frames[i + 1].t : end) - f.t, 0.001), 30);
    list.push(`file '${f.file}'`, `duration ${d.toFixed(4)}`);
  });
  list.push(`file '${frames[frames.length - 1].file}'`);
  fs.writeFileSync(path.join(dir, 'list.ffconcat'), list.join('\n') + '\n');
  const mp4 = path.join(outDir, `${name}.mp4`);
  const ok = spawnSync('ffmpeg', ['-y', '-loglevel', 'error', '-f', 'concat', '-safe', '0', '-i', path.join(dir, 'list.ffconcat'),
    '-vf', 'scale=1920:1080:flags=lanczos,fps=30,format=yuv420p', '-r', '30', '-c:v', 'libx264', '-preset', 'medium',
    '-crf', '18', '-tune', 'stillimage', '-movflags', '+faststart', mp4], { stdio: 'inherit' }).status === 0;
  for (const f of fs.readdirSync(dir)) fs.unlinkSync(path.join(dir, f));
  fs.rmdirSync(dir);
  if (!ok) throw new Error(`encoding ${mp4} failed`);
  console.log(`video: ${mp4}`);
  return mp4;
}

// verify compares every rendered block with its capture file, byte for
// byte, and writes the rendered text next to the source for diff(1).
function verify(part, src) {
  const lines = [];
  const rdir = path.join(work, 'rendered', part);
  fs.mkdirSync(rdir, { recursive: true });
  const r = rendered[part];
  let bad = 0;
  const check = (id, kind, want, got, srcFile) => {
    const ok = got === want;
    if (!ok) bad++;
    const file = path.join(rdir, `${id}.${kind}`);
    fs.writeFileSync(file, got ?? '');
    lines.push(`${ok ? 'same' : 'DIFF'}  ${part}/${id}.${kind}  (${srcFile})`);
  };
  for (const [name, blk] of Object.entries(src.blocks)) {
    if (!r[name]) continue;
    check(name, 'cmd', blk.cmd, r[name].cmd, path.join(dumpDir, part, `${name}.cmd`));
    check(name, 'out', blk.out, r[name].out, path.join(dumpDir, part, `${name}.out`));
  }
  for (const s of src.agent) {
    if (!r[s.id]) continue;
    const n = s.id.slice(1);
    if (s.say !== undefined) check(s.id, 'say', s.say, r[s.id].say, path.join(dumpDir, part, 'agent', `${n}.say`));
    else {
      check(s.id, 'cmd', s.cmd, r[s.id].cmd, path.join(dumpDir, part, 'agent', `${n}.cmd`));
      check(s.id, 'out', s.out, r[s.id].out, path.join(dumpDir, part, 'agent', `${n}.out`));
    }
  }
  const shownIds = new Set(Object.keys(r));
  const skipped = [...Object.keys(src.blocks), ...src.agent.map((s) => s.id)].filter((id) => !shownIds.has(id));
  lines.push(`${part}: ${lines.length} blocks compared, ${bad} differ; not shown: ${skipped.join(', ') || 'none'}`);
  return { lines, bad };
}

// --- main ---

const browser = await chromium.launch();
try {
  if (!args.includes('--replay-only')) await live(browser);
  fs.writeFileSync(path.join(work, 'page.html'), PAGE);
  const src1 = source('unguarded');
  const src2 = source('guarded');
  const v1 = await record(browser, 'part1-unguarded-terminal', part1, src1, 'unguarded');
  const v2 = await record(browser, 'part2-guarded-terminal', part2, src2, 'guarded');
  const r1 = verify('unguarded', src1);
  const r2 = verify('guarded', src2);
  fs.writeFileSync(path.join(outDir, 'terminal-verify.txt'), [...r1.lines, ...r2.lines].join('\n') + '\n');
  console.log([r1.lines.at(-1), r2.lines.at(-1)].join('\n'));
  const combined = path.join(outDir, 'terminal-combined.mp4');
  const ok = spawnSync('ffmpeg', ['-y', '-loglevel', 'error', '-i', v1, '-f', 'lavfi', '-t', '1', '-i', 'color=c=black:s=1920x1080:r=30',
    '-i', v2, '-filter_complex', '[0:v]setsar=1[a];[1:v]setsar=1[b];[2:v]setsar=1[c];[a][b][c]concat=n=3:v=1:a=0,format=yuv420p[v]',
    '-map', '[v]', '-r', '30', '-c:v', 'libx264', '-preset', 'medium', '-crf', '18', '-tune', 'stillimage',
    '-movflags', '+faststart', combined], { stdio: 'inherit' }).status === 0;
  if (ok) console.log(`video: ${combined}`);
  for (const s of stills) console.log(`still: ${s}`);
  if (r1.bad + r2.bad > 0) {
    console.error('pipeline-guard-terminal: a rendered block differs from its capture; see terminal-verify.txt');
    process.exitCode = 1;
  }
} finally {
  await browser.close();
}
