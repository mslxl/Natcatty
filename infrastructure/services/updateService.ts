/**
 * Update Service - Checks GitHub releases for new versions
 */

const GITHUB_API_URL = 'https://api.github.com/repos/binaricat/Netcatty/releases/latest';
const RELEASES_PAGE_URL = 'https://github.com/binaricat/Netcatty/releases';

export interface ReleaseInfo {
  version: string;       // e.g. "1.0.0" (without 'v' prefix)
  tagName: string;       // e.g. "v1.0.0"
  name: string;          // Release title
  body: string;          // Release notes (markdown)
  htmlUrl: string;       // URL to the release page
  publishedAt: string;   // ISO date string
  assets: ReleaseAsset[];
}

export interface ReleaseAsset {
  name: string;
  browserDownloadUrl: string;
  size: number;
}

export interface UpdateCheckResult {
  hasUpdate: boolean;
  currentVersion: string;
  latestRelease: ReleaseInfo | null;
  error?: string;
}

/**
 * Parse version string to comparable array
 * e.g. "1.2.3" -> [1, 2, 3]
 */
function parseVersion(version: string): number[] {
  // Remove 'v' prefix if present
  const clean = version.replace(/^v/i, '');
  return clean.split('.').map((part) => {
    const num = parseInt(part, 10);
    return isNaN(num) ? 0 : num;
  });
}

/**
 * Compare two version strings
 * Returns: 1 if a > b, -1 if a < b, 0 if equal
 */
export function compareVersions(a: string, b: string): number {
  const partsA = parseVersion(a);
  const partsB = parseVersion(b);
  const maxLen = Math.max(partsA.length, partsB.length);

  for (let i = 0; i < maxLen; i++) {
    const numA = partsA[i] ?? 0;
    const numB = partsB[i] ?? 0;
    if (numA > numB) return 1;
    if (numA < numB) return -1;
  }
  return 0;
}

/**
 * Fetch the latest release info from GitHub
 */
export async function fetchLatestRelease(): Promise<ReleaseInfo | null> {
  try {
    const response = await fetch(GITHUB_API_URL, {
      headers: {
        'Accept': 'application/vnd.github.v3+json',
        // Using anonymous access - rate limited to 60 requests/hour
      },
    });

    if (!response.ok) {
      if (response.status === 404) {
        // No releases yet
        return null;
      }
      throw new Error(`GitHub API error: ${response.status}`);
    }

    const data = await response.json();

    return {
      version: data.tag_name?.replace(/^v/i, '') || '0.0.0',
      tagName: data.tag_name || '',
      name: data.name || data.tag_name || '',
      body: data.body || '',
      htmlUrl: data.html_url || RELEASES_PAGE_URL,
      publishedAt: data.published_at || '',
      assets: (data.assets || []).map((asset: { name?: string; browser_download_url?: string; size?: number }) => ({
        name: asset.name || '',
        browserDownloadUrl: asset.browser_download_url || '',
        size: asset.size || 0,
      })),
    };
  } catch (error) {
    console.warn('[UpdateService] Failed to fetch latest release:', error);
    return null;
  }
}

/**
 * Check for updates
 */
export async function checkForUpdates(currentVersion: string): Promise<UpdateCheckResult> {
  const result: UpdateCheckResult = {
    hasUpdate: false,
    currentVersion,
    latestRelease: null,
  };

  try {
    const release = await fetchLatestRelease();
    if (!release) {
      return result;
    }

    result.latestRelease = release;
    result.hasUpdate = compareVersions(release.version, currentVersion) > 0;

    return result;
  } catch (error) {
    result.error = error instanceof Error ? error.message : 'Unknown error';
    return result;
  }
}

/**
 * Get release page URL for a specific version
 */
export function getReleaseUrl(version?: string): string {
  if (version) {
    return `${RELEASES_PAGE_URL}/tag/v${version.replace(/^v/i, '')}`;
  }
  return RELEASES_PAGE_URL;
}

/**
 * Get download URL for current platform
 */
export function getDownloadUrlForPlatform(
  release: ReleaseInfo,
  platform: string
): string | null {
  const assets = release.assets;
  
  // Platform-specific file patterns
  const patterns: Record<string, RegExp[]> = {
    win32: [/\.exe$/i, /win.*\.zip$/i, /windows/i],
    darwin: [/\.dmg$/i, /mac.*\.zip$/i, /darwin/i],
    linux: [/\.AppImage$/i, /\.deb$/i, /linux/i],
  };

  const platformPatterns = patterns[platform] || [];
  
  for (const pattern of platformPatterns) {
    const asset = assets.find((a) => pattern.test(a.name));
    if (asset) {
      return asset.browserDownloadUrl;
    }
  }

  // Fallback to release page
  return null;
}
