// Desktop-app download links, resolved from the latest GitHub release of
// vnovick/itervox. Shared by the build (Downloads.astro frontmatter, through
// releaseBuild.ts) and the page script that refreshes the links on load, so
// both match assets the same way. Every file name carries the version, so a
// link is found by pattern; anything not found points at the release page.

export const RELEASE_PAGE = 'https://github.com/vnovick/itervox/releases/latest';
export const RELEASE_API = 'https://api.github.com/repos/vnovick/itervox/releases/latest';

export const ASSET_PATTERNS = {
  // `-unsigned` until the Mac build is notarized.
  mac: /_universal(-unsigned)?\.dmg$/,
  winMsi: /_x64_en-US\.msi$/,
  winExe: /_x64-setup\.exe$/,
  appImageX64: /_amd64\.AppImage$/,
  appImageArm: /_aarch64\.AppImage$/,
  debX64: /_amd64\.deb$/,
  debArm: /_arm64\.deb$/,
} as const;

export type AssetKey = keyof typeof ASSET_PATTERNS;

export interface Release {
  /** The tag, e.g. "v0.3.3"; null when the release could not be read. */
  version: string | null;
  /** The release's own page, else the latest-release page. */
  page: string;
  links: Record<AssetKey, string>;
  /** The Mac disk image is not notarized (its name says `-unsigned`). */
  macUnsigned: boolean;
}

interface ApiRelease {
  tag_name?: unknown;
  html_url?: unknown;
  assets?: unknown;
}

export function fallbackRelease(): Release {
  const links = {} as Record<AssetKey, string>;
  for (const key of Object.keys(ASSET_PATTERNS) as AssetKey[]) links[key] = RELEASE_PAGE;
  return { version: null, page: RELEASE_PAGE, links, macUnsigned: false };
}

const isGitHubUrl = (u: unknown): u is string =>
  typeof u === 'string' && u.startsWith('https://github.com/vnovick/itervox/');

export function resolveRelease(data: unknown): Release {
  const release = fallbackRelease();
  if (!data || typeof data !== 'object') return release;
  const json = data as ApiRelease;
  if (typeof json.tag_name === 'string' && /^v?\d+\.\d+\.\d+[\w.-]*$/.test(json.tag_name)) {
    release.version = json.tag_name.startsWith('v') ? json.tag_name : `v${json.tag_name}`;
  }
  if (isGitHubUrl(json.html_url)) release.page = json.html_url;
  const assets = Array.isArray(json.assets) ? json.assets : [];
  for (const key of Object.keys(ASSET_PATTERNS) as AssetKey[]) {
    const asset = assets.find(
      (a) => a && typeof a.name === 'string' && ASSET_PATTERNS[key].test(a.name),
    );
    if (asset && isGitHubUrl(asset.browser_download_url)) {
      release.links[key] = asset.browser_download_url;
      if (key === 'mac') release.macUnsigned = asset.name.includes('unsigned');
    }
  }
  return release;
}

/**
 * Whether `brew install --cask vnovick/tap/itervox-app` works: the release
 * workflow publishes the cask only for a notarized Mac build, so it exists
 * once a release with a signed (not `-unsigned`) disk image is out.
 */
export function caskPublished(release: Release): boolean {
  return release.version !== null && release.links.mac !== RELEASE_PAGE && !release.macUnsigned;
}

export type Os = 'mac' | 'windows' | 'linux';

/** The visitor's OS; macOS when it cannot be told (or on a phone). */
export function detectOs(): Os {
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } };
  const platform = nav.userAgentData?.platform || nav.userAgent || '';
  if (/android|cros/i.test(platform)) return 'mac';
  if (/win/i.test(platform)) return 'windows';
  if (/linux|x11/i.test(platform) && !/mac/i.test(platform)) return 'linux';
  return 'mac';
}
