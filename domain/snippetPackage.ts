export const SNIPPET_PACKAGE_NAME_PATTERN = /^[\w\p{L}\p{N}-]+$/u;

export type SnippetPackageRenameError = "empty" | "invalidChars" | "duplicate";

type PackagedSnippet = { package?: string };

export function isSnippetPackagePathAtOrBelow(path: string, root: string): boolean {
  return path === root || path.startsWith(`${root}/`);
}

const rewritePackagePath = (value: string, from: string, to: string): string => {
  if (value === from) return to;
  if (value.startsWith(`${from}/`)) return to + value.slice(from.length);
  return value;
};

/** Remove a package and descendants; keep snippets and clear their package path. */
export function deleteSnippetPackage<T extends PackagedSnippet>(
  packages: string[],
  snippets: T[],
  path: string,
): { packages: string[]; snippets: T[] } {
  return {
    packages: packages.filter((item) => !isSnippetPackagePathAtOrBelow(item, path)),
    snippets: snippets.map((snippet) => {
      const packagePath = snippet.package || "";
      if (!packagePath || !isSnippetPackagePathAtOrBelow(packagePath, path)) return snippet;
      return { ...snippet, package: "" };
    }),
  };
}

export function renameSnippetPackage<T extends PackagedSnippet>(
  packages: string[],
  snippets: T[],
  path: string,
  newName: string,
):
  | { ok: true; packages: string[]; snippets: T[]; newPath: string }
  | { ok: false; error: SnippetPackageRenameError } {
  const trimmed = newName.trim();
  if (!trimmed) return { ok: false, error: "empty" };
  if (!SNIPPET_PACKAGE_NAME_PATTERN.test(trimmed)) return { ok: false, error: "invalidChars" };

  const parts = path.split("/");
  parts[parts.length - 1] = trimmed;
  const newPath = parts.join("/");

  if (newPath === path) {
    return { ok: true, packages, snippets, newPath };
  }

  const duplicate = packages.some(
    (item) => item !== path && item.toLowerCase() === newPath.toLowerCase(),
  );
  if (duplicate) return { ok: false, error: "duplicate" };

  return {
    ok: true,
    newPath,
    packages: Array.from(new Set(
      packages.map((item) => rewritePackagePath(item, path, newPath)),
    )),
    snippets: snippets.map((snippet) => {
      const packagePath = snippet.package || "";
      if (!packagePath) return snippet;
      const nextPath = rewritePackagePath(packagePath, path, newPath);
      return nextPath === packagePath ? snippet : { ...snippet, package: nextPath };
    }),
  };
}
