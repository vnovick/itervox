# Guide screenshots

The docs guides (`site/src/content/docs/guides/*.mdx`) show the dashboard
through screenshots captured here: the real app on Vite's dev server, every
`/api/v1/*` call route-mocked with the Lane-2 fixtures (`e2e/fixtures/`), in
the dark and the light theme. Nothing talks to a daemon, a tracker or an
agent CLI.

```bash
cd web && pnpm install --frozen-lockfile
cd ../site && pnpm install --frozen-lockfile   # sharp, for the WebP encoding
cd .. && make guide-shots                       # every guide
make guide-shots GUIDE=lanes                    # one guide (a spec file filter)
```

`PW_CHROMIUM_PATH=/path/to/chrome` launches that Chromium instead of the one
Playwright's pinned version downloads. `GUIDE_SHOTS_DIR` writes elsewhere.
These specs run only through `playwright.guides.config.ts`; no CI lane or
other config picks them up.

## Output

`site/public/screenshots/guides/<guide>/<slug>-dark.webp` and `-light.webp`,
taken at a 1440×900 window (or the shot's own `viewport`) at device scale 2,
cropped to the shot's region. Commit them with the guide.

## Add shots to a guide

1. **Fixture** — `fixtures/<guide>.ts`: the `Scenario` (snapshot + issues)
   and a `mocks(api)` function for any route the default mock leaves empty
   (`fixed(api, /regex/, body)` from `fixtures/common.ts`). Keep data generic:
   `DEMO-n` issues, `example.com`, no token-shaped strings.
2. **Spec** — `<guide>.spec.ts`, one call:
   ```ts
   defineGuideShots('my-guide', [
     {
       slug: '01-what-it-shows',          // file name, numbered in flow order
       scenario, mocks, route: '/?issue=DEMO-1',
       ready: (p) => expect(p.getByText('…')).toBeVisible(), // wait, click, type
       crop: (p) => p.getByTestId('issue-now-panel'),         // optional
       viewport: { width: 390, height: 844 },                  // optional (phone)
     },
   ]);
   ```
   The theme is set before load (the app reads `localStorage.theme`); a page
   error fails the shot.
3. **Capture** — `make guide-shots GUIDE=my-guide`, then look at the images.
4. **Use** in the guide's MDX:
   ```mdx
   import GuideFlow from '../../../components/GuideFlow.astro';
   import GuideShot from '../../../components/GuideShot.astro';

   <GuideFlow title="What you can do">
     <GuideShot name="guides/my-guide/01-what-it-shows" alt="…" caption="…" />
   </GuideFlow>
   ```
   `name` is the path under `/screenshots/` without `-dark`/`-light` and the
   extension, so the landing page's pairs work too (`name="itervox-sessions"`).
   The size is read from the files; a missing file fails `pnpm build`.
5. **Check** — `cd site && pnpm build`.

No registry: each guide has its own spec, fixture and output directory.
`shoot.ts` and `fixtures/common.ts` are shared; change them only for
something every guide needs.
