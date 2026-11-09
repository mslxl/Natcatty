#!/usr/bin/env nu
# Release tooling for the packaging pipeline. mygo.json is the only version
# source; the release workflow calls these subcommands.
#
#   nu scripts/release.nu version [--manifest mygo.json] [--debian]
#   nu scripts/release.nu prerelease [--manifest mygo.json]
#   nu scripts/release.nu rename-assets --dir dist --version 0.1.0
#   nu scripts/release.nu make-portable --dir dist/windows-amd64 --out FILE [--exclude PATH]
#   nu scripts/release.nu generate-sums --dir release [--out release/SHA256SUMS]
#   nu scripts/release.nu collect --from downloads --to release

# Print the subcommand list; a bare invocation does nothing else.
def main [] {
    print "usage: nu scripts/release.nu <version|prerelease|rename-assets|make-portable|generate-sums|collect> [flags]"
}

# MyGo encodes the first "-" of a SemVer pre-release as "~" in the Debian
# version and later "-" as "."; this mirrors that rule so the built .deb can be
# renamed to the published SemVer name.
def deb-version [version: string] {
    let stripped = ($version | str replace --regex '^v' '')
    let chunks = ($stripped | split row '-')
    let encoded = if ($chunks | length) == 1 {
        $stripped
    } else {
        let first = ($chunks | first)
        let rest = ($chunks | skip 1 | str join '.')
        $"($first)~($rest)"
    }
    $encoded | str replace --all '+' '~'
}

# Print the manifest version.
def "main version" [
    --manifest: path = "mygo.json"
    --debian
] {
    let version = (open $manifest | get version)
    if $debian { deb-version $version } else { $version }
}

# Report whether the manifest version is a SemVer pre-release.
def "main prerelease" [
    --manifest: path = "mygo.json"
] {
    let version = (open $manifest | get version)
    $version | str contains '-'
}

# Whether name is a release artifact handed to users.
def is-artifact [name: string] {
    let base = ($name | path basename)
    (
        ($base | str ends-with '.dmg')
        or ($base | str ends-with '.deb')
        or ($base | str ends-with '.tar.gz')
        or ($base | str ends-with '-portable.zip')
        or (
            ($base | str ends-with '.exe')
            and (($base | str contains ' Setup ') or ($base | str ends-with '-setup.exe'))
        )
    )
}

# Give a built artifact its published name. A name that already follows the
# scheme is returned unchanged; anything else artifact-looking is an error.
def canonical-asset-name [name: string, version: string] {
    let base = ($name | path basename)
    let deb = (deb-version $version)
    # The release workflow builds darwin/arm64 and darwin/amd64 separately.
    let dmg_arm64 = $"natcatty-($version)-macos-arm64.dmg"
    let dmg_amd64 = $"natcatty-($version)-macos-amd64.dmg"
    let dmg_universal = $"natcatty-($version)-macos-universal.dmg"
    let setup = $"natcatty-($version)-windows-amd64-setup.exe"
    let portable = $"natcatty-($version)-windows-amd64-portable.zip"
    let linux_amd64_tar = $"natcatty-($version)-linux-amd64.tar.gz"
    let linux_arm64_tar = $"natcatty-($version)-linux-arm64.tar.gz"
    let linux_amd64_deb = $"natcatty-($version)-linux-amd64.deb"
    let linux_arm64_deb = $"natcatty-($version)-linux-arm64.deb"
    if ($base == $"Natcatty ($version) arm64.dmg") { return $dmg_arm64 }
    if ($base == $"Natcatty ($version) amd64.dmg") { return $dmg_amd64 }
    if ($base == $"Natcatty ($version).dmg") { return $dmg_universal }
    if ($base == $"Natcatty Setup ($version) amd64.exe") { return $setup }
    if ($base == $"natcatty_($deb)_amd64.deb") { return $linux_amd64_deb }
    if ($base == $"natcatty_($deb)_arm64.deb") { return $linux_arm64_deb }
    let canonical = [$dmg_arm64 $dmg_amd64 $dmg_universal $setup $portable $linux_amd64_tar $linux_arm64_tar $linux_amd64_deb $linux_arm64_deb]
    if ($base in $canonical) { return $base }
    error make { msg: $"($base) is a release artifact but has no canonical name for version ($version)" }
}

# Nushell glob patterns use "/" even on Windows.
def slash-path [path: path] {
    $path | str replace --all '\' '/'
}

# Name a file relative to root with forward slashes.
def relative-name [root: path, file: path] {
    $file | path relative-to $root | path split | str join '/'
}

# Release artifacts under root as forward-slash relative names, sorted.
def release-artifacts [root: path] {
    let pattern = $"((slash-path $root))/**/*"
    glob --no-dir $pattern
    | where {|file| is-artifact ($file | path basename) }
    | each {|file| relative-name $root $file }
    | sort
}

# Locate 7z, which writes the portable zip and is present on the Windows
# runner and in the usual installation directory.
def seven-zip [] {
    if not (which 7z | is-empty) { return "7z" }
    let fallback = "C:/Program Files/7-Zip/7z.exe"
    if ($fallback | path exists) { return $fallback }
    error make { msg: "7z is required to build the portable archive" }
}

# Give every built release artifact under dir its canonical published name.
def "main rename-assets" [
    --dir: path
    --version: string
] {
    if $dir == null or $version == null {
        error make { msg: "rename-assets needs --dir and --version" }
    }
    let root = ($dir | path expand)
    let pattern = $"((slash-path $root))/**/*"
    let artifacts = (glob --no-dir $pattern | where {|file| is-artifact ($file | path basename) })
    if ($artifacts | is-empty) {
        error make { msg: $"($dir) holds no release artifacts" }
    }
    let renames = (
        $artifacts
        | each {|file|
            let base = ($file | path basename)
            let canonical = (canonical-asset-name $base $version)
            if $canonical == $base {
                null
            } else {
                { from: $file, to: (($file | path dirname) | path join $canonical) }
            }
        }
        | compact
    )
    if ($renames | is-empty) {
        print $"renamed 0 release assets in ($dir)"
        return
    }
    let duplicates = ($renames | get to | uniq -d)
    if not ($duplicates | is-empty) {
        error make { msg: $"($dir): more than one artifact would be renamed to ($duplicates | first)" }
    }
    for rename in $renames {
        if ($rename.to | path exists) {
            error make { msg: $"($rename.from): ($rename.to) already exists" }
        }
    }
    for rename in ($renames | sort-by from) {
        mv $rename.from $rename.to
        print $"($rename.from | path basename) -> ($rename.to | path basename)"
    }
    print $"renamed ($renames | length) release assets in ($dir)"
}

# Write the Windows portable zip: the built app and resources, without the
# NSIS Setup executable.
def "main make-portable" [
    --dir: path
    --out: path
    --exclude: string = ""
] {
    if $dir == null or $out == null {
        error make { msg: "make-portable needs --dir and --out" }
    }
    let root = ($dir | path expand)
    let out_path = ($out | path expand)
    let skip = if ($exclude | is-empty) {
        []
    } else {
        [$exclude | str replace --all '\' '/']
    }
    let pattern = $"((slash-path $root))/**/*"
    let files = (
        glob --no-dir $pattern
        | where {|file|
            let relative = (relative-name $root $file)
            if ($file | path expand) == $out_path { return false }
            if $relative in $skip { return false }
            if ($file | path type) != 'file' {
                error make { msg: $"($file) is not a regular file" }
            }
            true
        }
    )
    if ($files | is-empty) {
        error make { msg: $"($dir) holds no files for the portable archive" }
    }
    let out_dir = ($out_path | path dirname)
    if not ($out_dir | path exists) { mkdir $out_dir }
    let sevenzip = (seven-zip)
    let relative_files = ($files | each {|file| relative-name $root $file })
    cd $root
    try {
        ^$sevenzip a -tzip -y $out_path ...$relative_files | ignore
    } catch {|err|
        rm -f $out_path
        error make { msg: $err.msg }
    }
    if not ($out_path | path exists) {
        error make { msg: $"7z did not write ($out)" }
    }
    print $"wrote ($out) with ($relative_files | length) files"
}

# Write a sha256sum-compatible SHA256SUMS for every release artifact under dir.
def "main generate-sums" [
    --dir: path
    --out: path
] {
    if $dir == null {
        error make { msg: "generate-sums needs --dir" }
    }
    let root = ($dir | path expand)
    let out_path = if $out == null {
        $root | path join 'SHA256SUMS'
    } else {
        $out | path expand
    }
    let artifacts = (release-artifacts $root)
    if ($artifacts | is-empty) {
        error make { msg: $"($dir) holds no release artifacts" }
    }
    let entries = (
        $artifacts
        | each {|relative|
            let sum = (open --raw ($root | path join $relative) | hash sha256)
            { sum: $sum, name: $relative }
        }
    )
    let out_dir = ($out_path | path dirname)
    if not ($out_dir | path exists) { mkdir $out_dir }
    let text = ((($entries | each {|entry| $"($entry.sum)  ($entry.name)" }) | str join "\n") + "\n")
    $text | save -f $out_path
    for entry in $entries {
        print $"($entry.sum)  ($entry.name)"
    }
    print $"wrote ($out_path)"
}

# Copy every release artifact under from into the flat to directory. A
# repeated base name collapses only when the files are byte-identical.
def "main collect" [
    --from: path
    --to: path
] {
    if $from == null or $to == null {
        error make { msg: "collect needs --from and --to" }
    }
    let root = ($from | path expand)
    let dest = ($to | path expand)
    let artifacts = (release-artifacts $root)
    if ($artifacts | is-empty) {
        error make { msg: $"($from) holds no release artifacts" }
    }
    if not ($dest | path exists) { mkdir $dest }
    mut seen = []
    mut names = []
    for relative in $artifacts {
        let base = ($relative | path basename)
        let source = ($root | path join $relative)
        let matches = ($seen | where {|row| $row.base == $base })
        if ($matches | is-empty) {
            $seen = ($seen | append { base: $base, source: $source })
            cp -f $source ($dest | path join $base)
            $names = ($names | append $base)
        } else {
            let previous = ($matches | first | get source)
            let same = ((open --raw $previous | hash sha256) == (open --raw $source | hash sha256))
            if not $same {
                error make { msg: $"($from): ($previous) and ($source) are different artifacts with the same name" }
            }
        }
    }
    for name in ($names | sort) { print $name }
    print $"collected ($names | length) artifacts into ($to)"
}
