// Build-time read of the latest release (one request per build, shared by
// every Downloads block). Never fails the build: on any error the links fall
// back to the release page. GITHUB_TOKEN, when set (CI), only raises the API
// rate limit; it is sent to api.github.com and never printed.
import { RELEASE_API, fallbackRelease, resolveRelease, type Release } from './release';

let cached: Promise<Release> | undefined;

async function fetchRelease(): Promise<Release> {
  const headers: Record<string, string> = {
    Accept: 'application/vnd.github+json',
    'User-Agent': 'itervox-site-build',
  };
  const token = process.env.GITHUB_TOKEN;
  if (token) headers.Authorization = `Bearer ${token}`;
  try {
    const res = await fetch(RELEASE_API, { headers, signal: AbortSignal.timeout(10_000) });
    if (!res.ok) throw new Error(`GitHub API answered ${res.status}`);
    return resolveRelease(await res.json());
  } catch (err) {
    console.warn(`[downloads] latest release not read, linking the release page: ${(err as Error).message}`);
    return fallbackRelease();
  }
}

export function latestRelease(): Promise<Release> {
  cached ??= fetchRelease();
  return cached;
}
