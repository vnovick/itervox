#!/usr/bin/env node
// pipeline-guard-capture.mjs: records the pipeline-guard demo
// (scripts/pipeline-guard-demo.sh) in the dashboard: stills at each beat and one video per part, with a caption per
// beat and a corner label "Reproduced with scripted agents". The overlay is
// injected into the page by this script only; the product's web code is not
// touched.
//
// usage: node scripts/pipeline-guard-capture.mjs --out DIR [--bin PATH] [--part unguarded|guarded|both]
//                                              [--style default|projector|terminal] [--keep]
//
// --style terminal runs scripts/pipeline-guard-terminal.mjs (the terminal
// cut; it also needs --referee PATH) with the same arguments.
//
// It runs pipeline-guard-demo.sh with --operator ui and plays the operator in the
// dashboard itself (types the replies into the reply boxes). Needs
// Playwright (require('playwright')) and Chromium; ffmpeg, when present,
// turns each .webm into .mp4 and a small .gif. Output:
//   DIR/part1-unguarded.{webm,mp4,gif}, DIR/part2-guarded.{webm,mp4,gif}
//   DIR/p1-*.png, DIR/p2-*.png (1600x1000 at device scale 2, dark theme)
//   DIR/pipeline-guard-demo.log (the driver's output)
//
// --style projector is the projector cut: 1920x1080 at 30 fps, a larger page
// (1536x864 CSS px at device scale 1.25), every beat held for about 6s,
// slow typing, and a smooth zoom onto the element that carries each beat
// (a CSS transform on the app root or the issue panel, re-rasterised by the
// browser, so zoomed text stays sharp). Frames come from the DevTools
// screencast (JPEG, quality 92), not Playwright's low-bitrate recorder.
// Output: DIR/part1-unguarded-projector.mp4, DIR/part2-guarded-projector.mp4,
// DIR/projector-combined.mp4 (part 1, 1s of black, part 2) and DIR/p*-projector.png.
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
const partArg = opt('--part', 'both');
const parts = partArg === 'both' ? ['unguarded', 'guarded'] : [partArg];
const style = opt('--style', 'default');
if (style !== 'default' && style !== 'projector' && style !== 'terminal') {
  console.error('pipeline-guard-capture: --style must be default, projector or terminal');
  process.exit(2);
}
const PROJECTOR = style === 'projector';
if (style === 'terminal') {
  // The terminal cut is its own script; it takes the same --out and --bin.
  const r = spawnSync(process.execPath, [path.join(here, 'pipeline-guard-terminal.mjs'), ...args], { stdio: 'inherit' });
  process.exit(r.status ?? 1);
}
fs.mkdirSync(outDir, { recursive: true });

// The projector style lays the page out at 1536x864 CSS px and renders it at
// device scale 1.25: 1920x1080 pixels with text 25% larger.
const VIEW = PROJECTOR ? { width: 1536, height: 864 } : { width: 1600, height: 1000 };
const DSF = PROJECTOR ? 1.25 : 2;
const CORNER = 'Reproduced with scripted agents';

// --- the driver ------------------------------------------------------------

const release = path.join(outDir, '.release');
const go = path.join(outDir, '.go');
fs.rmSync(release, { force: true });
fs.rmSync(go, { force: true });
const logFile = fs.createWriteStream(path.join(outDir, PROJECTOR ? 'pipeline-guard-demo-projector.log' : 'pipeline-guard-demo.log'));
const driverArgs = ['--part', partArg, '--operator', 'ui'];
if (bin) driverArgs.push('--bin', path.resolve(bin));
if (args.includes('--keep')) driverArgs.push('--keep');
const driver = spawn(path.join(repoRoot, 'scripts', 'pipeline-guard-demo.sh'), driverArgs, {
  env: {
    ...process.env,
    DEMO_RELEASE: release,
    DEMO_GO: go,
    DEMO_AGENT_PACE: process.env.DEMO_AGENT_PACE ?? (PROJECTOR ? '1' : '0.8'),
    DEMO_START_DELAY: process.env.DEMO_START_DELAY ?? '4',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
});
const events = []; // {key, value}
let waiters = [];
let driverExit = null;
const onLine = (line) => {
  logFile.write(`${new Date().toISOString().slice(11, 19)} ${line}\n`);
  const m = /^(DEMO_[A-Z]+)=(.*)$/.exec(line);
  if (m) events.push({ key: m[1], value: m[2] });
  waiters = waiters.filter((w) => !w());
};
let buf = '';
for (const stream of [driver.stdout, driver.stderr]) {
  stream.setEncoding('utf8');
  stream.on('data', (chunk) => {
    buf += chunk;
    const lines = buf.split('\n');
    buf = lines.pop();
    lines.forEach(onLine);
  });
}
driver.on('exit', (code) => {
  driverExit = code;
  waiters = waiters.filter((w) => !w());
});

let cursor = 0; // events consumed
function next(key, value, timeoutMs = 300_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`timed out waiting for ${key}=${value ?? '*'}`)), timeoutMs);
    const check = () => {
      for (let i = cursor; i < events.length; i++) {
        const e = events[i];
        if (e.key === key && (value === undefined || e.value === value)) {
          cursor = i + 1;
          clearTimeout(timer);
          resolve(e.value);
          return true;
        }
      }
      if (driverExit !== null) {
        clearTimeout(timer);
        reject(new Error(`pipeline-guard-demo.sh exited (${driverExit}) before ${key}=${value ?? '*'}; see pipeline-guard-demo.log`));
        return true;
      }
      return false;
    };
    if (!check()) waiters.push(check);
  });
}

// --- the overlay -----------------------------------------------------------

const OVERLAY_CSS = `
#demo-overlay-corner, #demo-overlay-caption { position: fixed; z-index: 2147483647; pointer-events: none;
  font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
#demo-overlay-corner { right: 18px; bottom: 16px; padding: 6px 12px; border-radius: 999px;
  background: rgba(10, 12, 20, 0.82); color: #e6e8ef; font-size: 13px; font-weight: 600;
  letter-spacing: 0.02em; border: 1px solid rgba(255, 255, 255, 0.22); }
#demo-overlay-caption { left: 50%; bottom: 58px; transform: translateX(-50%); max-width: 1120px;
  padding: 14px 26px; border-radius: 16px; background: rgba(8, 10, 16, 0.97);
  border: 1px solid rgba(255, 255, 255, 0.25); box-shadow: 0 12px 40px rgba(0, 0, 0, 0.55);
  color: #ffffff; text-align: center; transition: opacity 200ms; }
#demo-overlay-caption .t { font-size: 28px; font-weight: 700; line-height: 1.25; }
#demo-overlay-caption .s { margin-top: 4px; font-size: 17px; color: #c9cede; }
#demo-overlay-caption .t.warn { color: #ffb86b; } #demo-overlay-caption .t.ok { color: #7ee2a8; }
#demo-overlay-caption .t.bad { color: #ff7b7b; }
`;

// The projector style's captions: larger, opaque, always bottom-centre.
const OVERLAY_CSS_PROJECTOR = `
#demo-overlay-corner, #demo-overlay-caption, #demo-records { position: fixed; z-index: 2147483647;
  pointer-events: none; font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
#demo-overlay-corner { right: 16px; bottom: 14px; padding: 7px 14px; border-radius: 999px;
  background: #0a0c14; color: #f2f4f8; font-size: 17px; font-weight: 700;
  border: 1px solid rgba(255, 255, 255, 0.35); }
#demo-overlay-caption { left: 50%; bottom: 64px; transform: translateX(-50%); max-width: 1380px;
  padding: 14px 34px 16px; border-radius: 18px; background: #06080d;
  border: 2px solid rgba(255, 255, 255, 0.35); box-shadow: 0 16px 48px rgba(0, 0, 0, 0.7);
  color: #ffffff; text-align: center; transition: opacity 250ms; white-space: nowrap; }
#demo-overlay-caption .t { font-size: 44px; font-weight: 800; line-height: 1.2; }
#demo-overlay-caption .s { margin-top: 4px; font-size: 25px; font-weight: 500; color: #e3e7f0; }
#demo-overlay-caption .s:empty { display: none; }
#demo-overlay-caption .t.warn { color: #ffc27a; } #demo-overlay-caption .t.ok { color: #86f0b4; }
#demo-overlay-caption .t.bad { color: #ff8a8a; }
#demo-records { z-index: 2147483646; left: 50%; top: 60px; transform: translateX(-50%) scale(0.94); opacity: 0;
  transition: opacity 600ms ease, transform 700ms cubic-bezier(0.4, 0, 0.2, 1);
  width: 1420px; max-height: 640px; overflow: hidden; padding: 24px 32px; border-radius: 18px; background: #0b0e16;
  border: 2px solid rgba(255, 255, 255, 0.3); box-shadow: 0 20px 60px rgba(0, 0, 0, 0.75); }
#demo-records.on { opacity: 1; transform: translateX(-50%) scale(1); }
#demo-records .h { font-size: 22px; color: #c9d1e3; margin-bottom: 12px; font-family: ui-monospace, Menlo, monospace; }
#demo-records pre { margin: 0; font-size: 21px; line-height: 1.38; color: #e8ecf4;
  font-family: ui-monospace, 'JetBrains Mono', Menlo, Consolas, monospace; white-space: pre; }
#demo-records .bad { color: #ff8a8a; font-weight: 700; } #demo-records .ok { color: #86f0b4; font-weight: 700; }
#demo-records .k { color: #9cc4ff; }
`;

let caption = { title: '', sub: '', tone: '' };
async function ensureOverlay(page) {
  await page
    .evaluate(
      ({ css, corner, cap, projector }) => {
        if (!document.getElementById('demo-overlay-style')) {
          const st = document.createElement('style');
          st.id = 'demo-overlay-style';
          st.textContent = css;
          document.head.appendChild(st);
        }
        let c = document.getElementById('demo-overlay-corner');
        if (!c) {
          c = document.createElement('div');
          c.id = 'demo-overlay-corner';
          document.body.appendChild(c);
        }
        c.textContent = corner;
        let k = document.getElementById('demo-overlay-caption');
        if (!k) {
          k = document.createElement('div');
          k.id = 'demo-overlay-caption';
          k.innerHTML = '<div class="t"></div><div class="s"></div>';
          document.body.appendChild(k);
        }
        k.style.opacity = cap.title ? '1' : '0';
        // With the issue panel open (right side), sit over the dimmed left
        // (the projector style stays bottom-centre).
        const panelOpen = !projector && [...document.querySelectorAll('[role="dialog"]')].some((e) => e.getClientRects().length > 0);
        k.style.left = panelOpen ? '320px' : '50%';
        k.style.maxWidth = panelOpen ? '560px' : '';
        k.querySelector('.t').style.fontSize = panelOpen ? '23px' : '';
        const t = k.querySelector('.t');
        t.textContent = cap.title;
        t.className = 't ' + (cap.tone || '');
        k.querySelector('.s').textContent = cap.sub;
      },
      { css: PROJECTOR ? OVERLAY_CSS_PROJECTOR : OVERLAY_CSS, corner: CORNER, cap: caption, projector: PROJECTOR },
    )
    .catch(() => {});
}
async function setCaption(page, title, sub = '', tone = '') {
  caption = { title, sub, tone };
  await ensureOverlay(page);
}

// --- capture helpers -------------------------------------------------------

const stills = [];
let partStart = Date.now();
async function still(page, name) {
  await ensureOverlay(page);
  await page.waitForTimeout(400);
  const file = path.join(outDir, PROJECTOR ? `${name}-projector.png` : `${name}.png`);
  await page.screenshot({ path: file });
  stills.push(file);
  console.log(`still: ${file} (at ${((Date.now() - partStart) / 1000).toFixed(1)}s)`);
}

const pause = (page, ms) => page.waitForTimeout(ms);

async function gotoDashboard(page, url) {
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.getByText('Autonomous agentic harness').waitFor({ timeout: 30_000 });
  await ensureOverlay(page);
}

// reloadDashboard reloads the page: an open issue panel keeps the run
// context it fetched while the run was live, so a fresh load shows the
// finished run ("Last run ... succeeded").
async function reloadDashboard(page) {
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.getByText('Autonomous agentic harness').waitFor({ timeout: 30_000 });
  await ensureOverlay(page);
}

async function closePanel(page) {
  const close = page.getByRole('dialog').getByRole('button', { name: /close/i });
  if (await close.count()) await close.first().click().catch(() => {});
  else await page.keyboard.press('Escape');
  await page.getByRole('dialog').waitFor({ state: 'detached', timeout: 5_000 }).catch(() => {});
  await ensureOverlay(page);
}

async function showBoard(page) {
  await page.evaluate(() => window.scrollTo({ top: 0 }));
  const card = page.getByText('CI is red on main — make the build pass').first();
  await card.waitFor({ timeout: 30_000 });
  await card.scrollIntoViewIfNeeded();
  await page.evaluate(() => window.scrollBy({ top: 260 }));
}

async function openIssue(page) {
  const open = page.locator('button:visible', { hasText: /^Open$/ });
  if (await open.count()) {
    await open.first().click();
  } else {
    await page.getByText('CI is red on main — make the build pass').first().click();
  }
  await page.getByRole('dialog').waitFor({ timeout: 10_000 });
  await pause(page, 800);
  await ensureOverlay(page);
}

// typeReply answers DEMO-1 in the dashboard's "Needs attention" reply box.
async function typeReply(page, text) {
  await page.evaluate(() => window.scrollTo({ top: 0 }));
  const box = page.locator('textarea:visible[placeholder^="Type your reply"]').first();
  // The inbox's Reply button toggles the box, which may still be open.
  if (!(await box.count())) await page.locator('button:visible', { hasText: /^Reply$/ }).first().click();
  await box.waitFor({ timeout: 10_000 });
  await box.pressSequentially(text, { delay: 12 });
  await pause(page, 600);
  await page.locator('button:visible', { hasText: 'Reply & Resume Agent' }).first().click();
}

// showDiff opens the Changes tab of the issue panel at file.
async function showDiff(page, file) {
  const dialog = page.getByRole('dialog');
  const changes = dialog.getByRole('tab', { name: 'Changes' });
  if (!(await changes.count())) return false;
  await changes.first().click();
  const row = dialog.getByText(file, { exact: true }).first();
  await row.waitFor({ timeout: 15_000 });
  await row.click();
  await pause(page, 1500);
  return true;
}

async function waitText(page, text, timeout = 60_000) {
  await page.getByText(text).first().waitFor({ timeout });
}

// --- the parts -------------------------------------------------------------

async function newRecordingPage(browser, name) {
  const dir = path.join(outDir, `.video-${name}`);
  if (!PROJECTOR) fs.mkdirSync(dir, { recursive: true });
  const context = await browser.newContext({
    viewport: VIEW,
    deviceScaleFactor: DSF,
    colorScheme: 'dark',
    ...(PROJECTOR ? {} : { recordVideo: { dir, size: VIEW } }),
  });
  await context.addInitScript(() => {
    try {
      localStorage.setItem('theme', 'dark');
    } catch {
      /* the dashboard defaults to dark */
    }
  });
  const page = await context.newPage();
  page.on('framenavigated', () => ensureOverlay(page));
  return { context, page };
}

async function partUnguarded(page, url) {
  await gotoDashboard(page, url);
  await setCaption(page, 'Part 1 · Unguarded', 'lanes off, no signed check');
  await showBoard(page);
  await pause(page, 1200);
  await setCaption(page, '1. Red build', 'DEMO-1: go test ./... fails on main (TestTotal)', 'bad');
  await still(page, 'p1-1-red-build');
  await pause(page, 800);
  fs.writeFileSync(go, '');

  await next('DEMO_BEAT', 'agent-running');
  await pause(page, 1500);
  await openIssue(page);
  await setCaption(page, '2. Agent edits the pipeline', 'continue-on-error: true on the CI test step', 'warn');
  await waitText(page, '.github/workflows/ci.yml', 60_000);
  await still(page, 'p1-2-pipeline-edit');

  await next('DEMO_BEAT', 'green');
  await closePanel(page);
  await reloadDashboard(page);
  await showBoard(page);
  await setCaption(page, 'Unguarded: green, nothing was tested', 'pushed, PR opened, issue In Review', 'ok');
  await pause(page, 1200);
  await openIssue(page);
  await still(page, 'p1-3-green-untested');
  if (await showDiff(page, '.github/workflows/ci.yml')) {
    await setCaption(page, 'Unguarded: green, nothing was tested', 'the PR only makes CI ignore the failing test', 'ok');
    await still(page, 'p1-4-pushed-diff');
  }
  await pause(page, 1500);
}

async function partGuarded(page, url) {
  await gotoDashboard(page, url);
  await setCaption(page, 'Part 2 · Guarded', 'same repo, same agent; lanes on, verify: [test]');
  await showBoard(page);
  await pause(page, 1200);
  await setCaption(page, '1. Red build', 'DEMO-1: go test ./... fails on main (TestTotal)', 'bad');
  await still(page, 'p2-1-red-build');
  await pause(page, 800);
  fs.writeFileSync(go, '');

  await next('DEMO_BEAT', 'held');
  await page.evaluate(() => window.scrollTo({ top: 0 }));
  await setCaption(page, '2. Agent edits the pipeline: held', 'the push changes .github/workflows/ci.yml: human lane', 'warn');
  await waitText(page, 'Pipeline change', 30_000);
  await still(page, 'p2-2-held');
  await openIssue(page);
  await page.getByText(/Waiting for approval/).first().scrollIntoViewIfNeeded().catch(() => {});
  await still(page, 'p2-3-held-detail');
  await closePanel(page);
  await setCaption(page, '2. Agent edits the pipeline: held', 'the operator rejects it', 'warn');
  await typeReply(page, 'Rejected: fix the code, not the pipeline.');

  await next('DEMO_BEAT', 'verify-failed');
  await setCaption(page, '2. Agent edits the pipeline: held', 'signed check (verify): go test ./... exited 1', 'bad');
  await openIssue(page);
  await waitText(page, 'Signed checks failed', 30_000);
  await page.getByText('Signed checks failed').first().scrollIntoViewIfNeeded();
  await still(page, 'p2-4-signed-check-failed');
  await closePanel(page);
  await typeReply(page, 'Fix the bug in cart.go; leave CI alone.');
  await setCaption(page, 'The agent tries again', 'reverts the CI edit, fixes cart.go, pushes', '');
  await pause(page, 1500);
  await openIssue(page);

  await next('DEMO_BEAT', 'pr-opened');
  await setCaption(page, '3. Fixed the code: auto lane, signed check passed', 'PR opened, issue In Review', 'ok');
  await pause(page, 800);
  await closePanel(page);
  await reloadDashboard(page);
  await showBoard(page);
  await openIssue(page);
  await still(page, 'p2-5-pr-opened');
  if (await showDiff(page, 'cart.go')) {
    await setCaption(page, '3. Fixed the code: auto lane, signed check passed', 'the PR changes cart.go only', 'ok');
    await still(page, 'p2-6-fix-diff');
  }
  await closePanel(page);
  // The signed records, as the daemon serves them.
  await page.goto(`${url}/api/v1/issues/DEMO-1/attestations`);
  // Chromium's own JSON viewer: tick its "Pretty-print" box when it has one.
  const pretty = page.locator('input[type="checkbox"]');
  if (await pretty.count()) {
    await pretty.first().check().catch(() => {});
    await pause(page, 300);
  }
  await setCaption(page, '3. Fixed the code: auto lane, signed check passed', 'GET /api/v1/issues/DEMO-1/attestations: exit 1, then exit 0, both verified', 'ok');
  await still(page, 'p2-7-signed-records');
  await pause(page, 1500);
}

// --- the projector style: zoom, hold, slow typing --------------------------------

const HOLD = 5400; // a beat's key frame stays on screen this long
const ZOOM_MS = 700;
const TYPE_DELAY = 75; // ms per character in the reply box
const BEAT_GAP = 600; // pause before a transition

// measure returns the union of targets, in viewport CSS px, with no zoom
// applied. A target is {text, scope} (a substring of one text node, searched
// in the issue panel when scope is 'dialog', else in the app) or {selector}.
async function measure(page, targets, pad = 18) {
  return page.evaluate(
    ({ targets, pad }) => {
      const visible = (el) => el && el.getClientRects().length > 0;
      const dialog = [...document.querySelectorAll('[role="dialog"]')].find(visible);
      const rects = [];
      for (const t of targets) {
        const scope = t.scope === 'dialog' ? dialog : document.getElementById('root');
        if (!scope) continue;
        if (t.selector) {
          const el = [...scope.querySelectorAll(t.selector)].find(visible);
          if (el) rects.push(el.getBoundingClientRect());
          continue;
        }
        const w = document.createTreeWalker(scope, NodeFilter.SHOW_TEXT);
        let n;
        while ((n = w.nextNode())) {
          const i = n.data.indexOf(t.text);
          if (i < 0 || !visible(n.parentElement)) continue;
          const r = document.createRange();
          r.setStart(n, i);
          r.setEnd(n, i + t.text.length);
          const rs = [...r.getClientRects()].filter((x) => x.width > 0);
          if (rs.length) {
            rects.push(...rs);
            break;
          }
        }
      }
      if (!rects.length) return null;
      const x0 = Math.min(...rects.map((r) => r.left)) - pad;
      const y0 = Math.min(...rects.map((r) => r.top)) - pad;
      const x1 = Math.max(...rects.map((r) => r.right)) + pad;
      const y1 = Math.max(...rects.map((r) => r.bottom)) + pad;
      return { x: x0, y: y0, w: x1 - x0, h: y1 - y0, inDialog: targets.some((t) => t.scope === 'dialog') };
    },
    { targets, pad },
  );
}

// zoomTo smoothly scales the app root (or the open issue panel) so box fills
// about 70% of the frame above the caption, clamped to 1.6x-2.5x. Fixed
// elements under the root are pinned first, so the transform cannot stretch
// them.
async function zoomTo(page, box, { min = 1.6, max = 2.5, maxWidth } = {}) {
  if (!box) return false;
  if (maxWidth && box.w > maxWidth) box = { ...box, w: maxWidth };
  await page.evaluate(
    ({ box, min, max, ms }) => {
      const vw = innerWidth;
      const vh = innerHeight - 170; // the caption sits below this
      const visible = (el) => el && el.getClientRects().length > 0;
      const dialog = [...document.querySelectorAll('[role="dialog"]')].find(visible);
      const el = box.inDialog && dialog ? dialog : document.getElementById('root');
      if (el.id === 'root') {
        window.scrollTo(0, 0);
        for (const f of el.querySelectorAll('*')) {
          if (getComputedStyle(f).position !== 'fixed' || f.dataset.demoPinned) continue;
          if (dialog?.parentElement?.contains(f)) continue;
          const r = f.getBoundingClientRect();
          f.dataset.demoPinned = f.getAttribute('style') ?? '';
          Object.assign(f.style, { top: `${r.top}px`, left: `${r.left}px`, width: `${r.width}px`,
            height: `${r.height}px`, bottom: 'auto', right: 'auto' });
        }
      }
      const er = el.getBoundingClientRect();
      const s = Math.min(max, Math.max(min, Math.min((0.7 * vw) / box.w, (0.7 * vh) / box.h)));
      const cx = box.x + box.w / 2 - er.left;
      const cy = box.y + box.h / 2 - er.top;
      let tx = vw / 2 - er.left - s * cx;
      let ty = vh / 2 - er.top - s * cy;
      // Keep the zoomed element covering the frame.
      tx = Math.min(-er.left, Math.max(innerWidth - er.left - s * er.width, tx));
      ty = Math.min(-er.top, Math.max(innerHeight - er.top - s * Math.max(er.height, innerHeight), ty));
      el.dataset.demoZoomed = '1';
      el.style.transformOrigin = '0 0';
      el.style.transition = `transform ${ms}ms cubic-bezier(0.4, 0, 0.2, 1)`;
      el.style.transform = `translate(${tx}px, ${ty}px) scale(${s})`;
    },
    { box, min, max, ms: ZOOM_MS },
  );
  await pause(page, ZOOM_MS + 150);
  return true;
}

async function zoomOut(page) {
  await page.evaluate((ms) => {
    for (const el of document.querySelectorAll('[data-demo-zoomed]')) {
      el.style.transition = `transform ${ms}ms cubic-bezier(0.4, 0, 0.2, 1)`;
      el.style.transform = 'translate(0px, 0px) scale(1)';
    }
  }, ZOOM_MS);
  await pause(page, ZOOM_MS + 100);
  await page.evaluate(() => {
    for (const el of document.querySelectorAll('[data-demo-zoomed]')) {
      el.style.transform = '';
      el.style.transition = '';
      el.style.transformOrigin = '';
      delete el.dataset.demoZoomed;
    }
    for (const f of document.querySelectorAll('[data-demo-pinned]')) {
      const prev = f.dataset.demoPinned;
      if (prev) f.setAttribute('style', prev);
      else f.removeAttribute('style');
      delete f.dataset.demoPinned;
    }
  });
}

// beat zooms onto targets, holds, takes the still, and zooms back out.
async function beat(page, targets, stillName, opts = {}) {
  const box = await measure(page, targets);
  if (!box) console.log(`pipeline-guard-capture: no zoom target for ${stillName}; holding the full frame`);
  await zoomTo(page, box, opts);
  await pause(page, 400);
  await still(page, stillName);
  await pause(page, HOLD - 800);
  await zoomOut(page);
  await pause(page, BEAT_GAP);
}

const CARD = 'CI is red on main — make the build pass';

async function openIssueProjector(page) {
  const open = page.locator('button:visible', { hasText: /^Open$/ });
  if (await open.count()) await open.first().click();
  else await page.getByText(CARD).first().click();
  await page.getByRole('dialog').waitFor({ timeout: 10_000 });
  await pause(page, 900);
  await ensureOverlay(page);
}

async function closePanelProjector(page) {
  await closePanel(page);
  await page.evaluate(() => window.scrollTo(0, 0));
  await pause(page, 400);
}

// typeReplyProjector types the operator's answer slowly, zoomed on the reply box.
async function typeReplyProjector(page, text, stillName) {
  await page.evaluate(() => window.scrollTo(0, 0));
  const box = page.locator('textarea:visible[placeholder^="Type your reply"]').first();
  if (!(await box.count())) await page.locator('button:visible', { hasText: /^Reply$/ }).first().click();
  await box.waitFor({ timeout: 10_000 });
  const b = await box.boundingBox();
  const send = await page.locator('button:visible', { hasText: 'Reply & Resume Agent' }).first().boundingBox();
  const y1 = Math.max(b.y + b.height, send ? send.y + send.height : 0);
  await zoomTo(page, { x: b.x - 16, y: b.y - 60, w: Math.min(b.width, 820) + 32, h: y1 - b.y + 76, inDialog: false });
  await box.click();
  await box.pressSequentially(text, { delay: TYPE_DELAY });
  await pause(page, 500);
  await still(page, stillName);
  await pause(page, 1500);
  await page.locator('button:visible', { hasText: 'Reply & Resume Agent' }).first().click();
  await pause(page, 900);
  await zoomOut(page);
  await pause(page, BEAT_GAP);
}

async function showDiffProjector(page, file) {
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('tab', { name: 'Changes' }).first().click();
  const row = dialog.getByText(file, { exact: true }).first();
  await row.waitFor({ timeout: 20_000 });
  await row.click();
  await pause(page, 1500);
}

async function showDetailsProjector(page) {
  await page.getByRole('dialog').getByRole('tab', { name: 'Details' }).first().click();
  await pause(page, 1000);
}

// The finished run in the issue panel: status badge, "Last run … succeeded"
// and the pull request link.
const FINISHED_TARGETS = [
  { text: 'In Review', scope: 'dialog' },
  { text: 'Last run', scope: 'dialog' },
  { text: 'succeeded', scope: 'dialog' },
  { text: 'Pull request: https://example.com', scope: 'dialog' },
];

async function projectorUnguarded(page, url) {
  await gotoDashboard(page, url);
  await setCaption(page, 'Part 1 · Unguarded', 'lanes off, no signed check');
  await pause(page, 2800);
  await setCaption(page, '1. Red build', 'go test ./... fails on main', 'bad');
  await beat(page, [{ text: CARD }], 'p1-1-red-build');
  fs.writeFileSync(go, '');

  await next('DEMO_BEAT', 'agent-running');
  await openIssueProjector(page);
  await setCaption(page, '2. Agent edits the pipeline', 'instead of the code', 'warn');
  await page.getByRole('dialog').getByText('.github/workflows/ci.yml').first().waitFor({ timeout: 60_000 });
  await pause(page, 2500);
  await showDiffProjector(page, '.github/workflows/ci.yml');
  await setCaption(page, '2. Agent edits the pipeline', 'continue-on-error: true on the test step', 'warn');
  await beat(page, [{ text: '- name: Test', scope: 'dialog' }, { text: 'continue-on-error: true', scope: 'dialog' },
    { text: 'run: go test ./...', scope: 'dialog' }], 'p1-2-pipeline-edit');

  await next('DEMO_BEAT', 'green');
  await closePanelProjector(page);
  await reloadDashboard(page);
  await openIssueProjector(page);
  await setCaption(page, 'Pushed, PR opened, run succeeded', 'issue moved to In Review', 'ok');
  await beat(page, FINISHED_TARGETS, 'p1-3-in-review');
  await closePanelProjector(page);
  await setCaption(page, 'Green. Nothing was tested.', 'the pushed branch still fails go test', 'ok');
  await pause(page, 600);
  await still(page, 'p1-4-green-nothing-tested');
  await pause(page, HOLD);
}

async function projectorGuarded(page, url) {
  await gotoDashboard(page, url);
  await setCaption(page, 'Part 2 · Guarded', 'same repo and agent; lanes on, verify: test');
  await pause(page, 2800);
  await setCaption(page, '1. Red build', 'go test ./... fails on main', 'bad');
  await beat(page, [{ text: CARD }], 'p2-1-red-build');
  fs.writeFileSync(go, '');

  await next('DEMO_BEAT', 'held');
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.getByText('Pipeline change').first().waitFor({ timeout: 30_000 });
  await setCaption(page, '2. Agent edits the pipeline: held', 'the push changes .github/workflows/ci.yml', 'warn');
  await beat(page, [{ text: 'DEMO-1' }, { text: 'Pipeline change: the agent wants to push changes to `.github/workflows/ci.yml`' }],
    'p2-2-held', { min: 1.3 });

  await setCaption(page, 'The operator rejects it', 'the reason goes back to the agent', 'warn');
  await typeReplyProjector(page, 'Rejected: fix the code, not the pipeline.', 'p2-3-operator-rejects');

  await next('DEMO_BEAT', 'verify-failed');
  await openIssueProjector(page);
  await page.getByRole('dialog').getByText(/The push was refused/).first().waitFor({ timeout: 30_000 });
  await setCaption(page, 'The push was refused', 'the operator rejected the pipeline change', 'warn');
  await beat(page, [{ text: 'The push was refused: guardrails: the operator rejected', scope: 'dialog' },
    { text: 'Rejected: fix', scope: 'dialog' }], 'p2-4-push-refused', { min: 1.4 });
  await setCaption(page, 'Signed check: go test exited 1', 'run by Itervox itself after the agent finished', 'bad');
  await beat(page, [{ text: 'Waiting for input: Signed checks failed (verify)', scope: 'dialog' },
    { text: 'exited with code 1', scope: 'dialog' }], 'p2-5-signed-check-failed', { min: 1.4 });
  await closePanelProjector(page);

  await setCaption(page, 'The operator: fix the code', '', 'warn');
  await typeReplyProjector(page, 'Fix the bug in cart.go; leave CI alone.', 'p2-6-operator-fix-the-code');
  await setCaption(page, 'The agent tries again', 'reverts the CI edit, fixes cart.go, pushes');
  await openIssueProjector(page);

  await next('DEMO_BEAT', 'pr-opened');
  await closePanelProjector(page);
  await reloadDashboard(page);
  await openIssueProjector(page);
  await showDiffProjector(page, 'cart.go');
  await setCaption(page, '3. Fixed the code: auto lane', 'the PR changes cart.go only; the push was not held', 'ok');
  await beat(page, [{ text: 'for i := 0; i < len(items)-1; i++ {', scope: 'dialog' },
    { text: 'for i := 0; i < len(items); i++ {', scope: 'dialog' }, { text: 'func Total(items []Item) int {', scope: 'dialog' }],
    'p2-7-fix-diff');
  await showDetailsProjector(page);
  await setCaption(page, '3. Signed check passed, PR opened', 'issue moved to In Review', 'ok');
  await beat(page, FINISHED_TARGETS, 'p2-8-in-review');
  await closePanelProjector(page);

  await showRecords(page, url);
  await setCaption(page, 'Signed records: exit 1, then exit 0', 'both signatures verify', 'ok');
  await pause(page, 900);
  await still(page, 'p2-9-signed-records');
  await pause(page, HOLD + 1500);
}

// showRecords renders GET /api/v1/issues/DEMO-1/attestations in a readable
// panel: the response, pretty-printed and trimmed to the fields that matter.
async function showRecords(page, url) {
  const res = await page.request.get(`${url}/api/v1/issues/DEMO-1/attestations`);
  const data = await res.json();
  const trimmed = {
    identifier: data.identifier,
    runs: (data.runs ?? []).map((r) => ({
      runId: r.runId,
      records: (r.records ?? []).map((x) => ({
        kind: x.kind,
        ...(x.name ? { name: x.name } : {}),
        exitCode: x.exitCode,
        tree: String(x.treeHash ?? '').slice(0, 12),
        verified: x.verified,
      })),
    })),
  };
  await page.evaluate((json) => {
    const esc = (v) => String(v).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    const k = (name) => `<span class="k">"${name}"</span>`;
    const val = (v) => (typeof v === 'string' ? `"${esc(v)}"` : esc(v));
    // Pretty-printed JSON, one record per line so both runs fit the frame.
    const lines = ['{', `  ${k('identifier')}: ${val(json.identifier)},`, `  ${k('runs')}: [`];
    json.runs.forEach((run, ri) => {
      lines.push('    {', `      ${k('runId')}: ${val(run.runId)},`, `      ${k('records')}: [`);
      run.records.forEach((r, i) => {
        const fields = Object.entries(r).map(([name, v]) => {
          let shown = val(v);
          if (name === 'exitCode' && r.kind === 'check') shown = `<span class="${v === 0 ? 'ok' : 'bad'}">${v}</span>`;
          if (name === 'verified' && v === true) shown = '<span class="ok">true</span>';
          return `${k(name)}: ${shown}`;
        });
        lines.push(`        { ${fields.join(', ')} }${i + 1 < run.records.length ? ',' : ''}`);
      });
      lines.push('      ]', `    }${ri + 1 < json.runs.length ? ',' : ''}`);
    });
    lines.push('  ]', '}');
    let p = document.getElementById('demo-records');
    if (!p) {
      p = document.createElement('div');
      p.id = 'demo-records';
      document.body.appendChild(p);
    }
    p.innerHTML = '<div class="h">GET /api/v1/issues/DEMO-1/attestations (key fields)</div><pre></pre>';
    p.querySelector('pre').innerHTML = lines.join('\n');
    requestAnimationFrame(() => p.classList.add('on'));
  }, trimmed);
  await pause(page, 900);
}

// --- the projector recorder: DevTools screencast frames -> H.264 -----------------

async function startRecorder(page, name) {
  const dir = path.join(outDir, `.frames-${name}`);
  fs.mkdirSync(dir, { recursive: true });
  const cdp = await page.context().newCDPSession(page);
  const frames = [];
  cdp.on('Page.screencastFrame', (f) => {
    const file = path.join(dir, `f${String(frames.length).padStart(6, '0')}.jpg`);
    fs.writeFileSync(file, Buffer.from(f.data, 'base64'));
    frames.push({ file, t: f.metadata.timestamp });
    cdp.send('Page.screencastFrameAck', { sessionId: f.sessionId }).catch(() => {});
  });
  await cdp.send('Page.startScreencast', { format: 'jpeg', quality: 92, maxWidth: 1920, maxHeight: 1080, everyNthFrame: 1 });
  return {
    async stop() {
      const end = Date.now() / 1000;
      await cdp.send('Page.stopScreencast').catch(() => {});
      return { dir, frames, end };
    },
  };
}

// encodeFrames writes an ffconcat list with each frame's real duration and
// encodes it at a constant 30 fps.
function encodeFrames({ dir, frames, end }, mp4) {
  if (!frames.length) return false;
  const lines = ['ffconcat version 1.0'];
  frames.forEach((f, i) => {
    const nextT = i + 1 < frames.length ? frames[i + 1].t : end;
    const d = Math.min(Math.max(nextT - f.t, 0.001), 30);
    lines.push(`file '${f.file}'`, `duration ${d.toFixed(4)}`);
  });
  lines.push(`file '${frames[frames.length - 1].file}'`);
  const list = path.join(dir, 'frames.ffconcat');
  fs.writeFileSync(list, lines.join('\n') + '\n');
  const ok = spawnSync('ffmpeg', ['-y', '-loglevel', 'error', '-f', 'concat', '-safe', '0', '-i', list,
    '-vf', 'scale=1920:1080:flags=lanczos,fps=30,format=yuv420p', '-r', '30', '-c:v', 'libx264',
    '-preset', 'medium', '-crf', '18', '-tune', 'stillimage', '-movflags', '+faststart', mp4], { stdio: 'inherit' }).status === 0;
  for (const f of fs.readdirSync(dir)) fs.unlinkSync(path.join(dir, f));
  fs.rmdirSync(dir);
  return ok;
}

// --- main ------------------------------------------------------------------

const browser = await chromium.launch();
const videos = [];
const projectorRecordings = [];
let failed = null;
try {
  for (const [i, part] of parts.entries()) {
    const url = await next('DEMO_DASHBOARD');
    const name = `part${partArg === 'both' ? i + 1 : part === 'unguarded' ? 1 : 2}-${part}`;
    const { context, page } = await newRecordingPage(browser, name);
    partStart = Date.now();
    const recorder = PROJECTOR ? await startRecorder(page, name) : null;
    try {
      if (PROJECTOR) await (part === 'unguarded' ? projectorUnguarded(page, url) : projectorGuarded(page, url));
      else if (part === 'unguarded') await partUnguarded(page, url);
      else await partGuarded(page, url);
    } catch (err) {
      await page.screenshot({ path: path.join(outDir, `failure-${name}.png`) }).catch(() => {});
      throw err;
    } finally {
      if (recorder) projectorRecordings.push({ name, rec: await recorder.stop() });
      const video = page.video();
      await context.close();
      if (video) {
        const target = path.join(outDir, `${name}.webm`);
        await video.saveAs(target);
        await video.delete();
        videos.push(target);
      }
      fs.writeFileSync(release, '');
    }
    await next('DEMO_RESULT', `${part} ok`);
  }
} catch (err) {
  failed = err;
} finally {
  await browser.close();
}
if (failed) {
  driver.kill('SIGTERM');
  console.error(`pipeline-guard-capture: ${failed.message}`);
  process.exit(1);
}
await new Promise((r) => (driverExit !== null ? r() : driver.on('exit', r)));
if (driverExit !== 0) {
  console.error(`pipeline-guard-capture: pipeline-guard-demo.sh exited ${driverExit}; see ${path.join(outDir, 'pipeline-guard-demo.log')}`);
  process.exit(1);
}

// --- video conversion ------------------------------------------------------

const hasFfmpeg = spawnSync('ffmpeg', ['-version'], { stdio: 'ignore' }).status === 0;
if (PROJECTOR) {
  if (!hasFfmpeg) {
    console.error('pipeline-guard-capture: --style projector needs ffmpeg to encode the frames');
    process.exit(1);
  }
  const mp4s = [];
  for (const { name, rec } of projectorRecordings) {
    const mp4 = path.join(outDir, `${name}-projector.mp4`);
    if (!encodeFrames(rec, mp4)) {
      console.error(`pipeline-guard-capture: encoding ${mp4} failed`);
      process.exit(1);
    }
    mp4s.push(mp4);
    console.log(`video: ${mp4}`);
  }
  if (mp4s.length === 2) {
    const combined = path.join(outDir, 'projector-combined.mp4');
    const ok = spawnSync('ffmpeg', ['-y', '-loglevel', 'error', '-i', mp4s[0],
      '-f', 'lavfi', '-t', '1', '-i', 'color=c=black:s=1920x1080:r=30', '-i', mp4s[1],
      '-filter_complex', '[0:v]setsar=1[a];[1:v]setsar=1[b];[2:v]setsar=1[c];[a][b][c]concat=n=3:v=1:a=0,format=yuv420p[v]',
      '-map', '[v]', '-r', '30', '-c:v', 'libx264', '-preset', 'medium', '-crf', '18', '-tune', 'stillimage',
      '-movflags', '+faststart', combined], { stdio: 'inherit' }).status === 0;
    if (ok) console.log(`video: ${combined}`);
  }
  console.log(`pipeline-guard-capture: ${stills.length} stills, ${mp4s.length} videos in ${outDir}`);
  process.exit(0);
}
for (const webm of videos) {
  console.log(`video: ${webm}`);
  if (!hasFfmpeg) continue;
  const mp4 = webm.replace(/\.webm$/, '.mp4');
  const gif = webm.replace(/\.webm$/, '.gif');
  const run = (a) => spawnSync('ffmpeg', ['-y', '-loglevel', 'error', ...a], { stdio: 'inherit' }).status === 0;
  if (run(['-i', webm, '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-crf', '20', '-movflags', '+faststart', mp4])) {
    console.log(`video: ${mp4}`);
  }
  if (
    run([
      '-i', webm, '-vf',
      'fps=8,scale=960:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128[p];[b][p]paletteuse=dither=bayer',
      gif,
    ])
  ) {
    console.log(`video: ${gif}`);
  }
}
if (!hasFfmpeg) console.log('pipeline-guard-capture: ffmpeg not found; videos left as .webm');
for (const d of fs.readdirSync(outDir)) {
  if (d.startsWith('.video-')) {
    try {
      fs.rmdirSync(path.join(outDir, d)); // empty once each video is saved
    } catch {
      /* leave it */
    }
  }
}
console.log(`pipeline-guard-capture: ${stills.length} stills, ${videos.length} videos in ${outDir}`);
