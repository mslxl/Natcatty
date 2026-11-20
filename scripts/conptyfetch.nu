#!/usr/bin/env nu
# Download and verify the pinned Windows Terminal ConPTY passthrough pair.

const package_url = "https://github.com/microsoft/terminal/releases/download/v1.24.11911.0/Microsoft.Windows.Console.ConPTY.1.24.260710001.nupkg"
const package_sha = "9382AD7BECB7E4D84E300578D8E4F4DF28F43D979D9055D978C42913C47E0E9D"
const dll_entry = "runtimes/win-x64/native/conpty.dll"
const dll_sha = "39FBA2713E2495117B1591AE8C32A3B904BEA7AA66069CF7815E2844C76D75D8"
const exe_entry = "build/native/runtimes/x64/OpenConsole.exe"
const exe_sha = "B7FD936C2668B87B9ECF7B3366DC6568AFC1C6F981874CBA3E955A1C35CF8160"

def verify-sha [path: path, expected: string, label: string] {
    let actual = (open $path | hash sha256 | str lowercase)
    if $actual != ($expected | str lowercase) {
        error make { msg: $"($label) sha256 is ($actual), expected ($expected)" }
    }
}

def extract-package [package: path, extract_dir: path] {
    let result = (^7z x -y $package $dll_entry $exe_entry $"-o($extract_dir)" | complete)
    if $result.exit_code != 0 {
        error make { msg: $"7z failed to extract the ConPTY package: ($result.stderr)" }
    }
}

def install-file [source: path, destination: path, expected: string, label: string] {
    verify-sha $source $expected $label
    let temporary = $"($destination).tmp"
    rm -f $temporary
    cp $source $temporary
    mv -f $temporary $destination
}

def main [
    --out: path = "resources/winconpty"
] {
    let output = ($out | path expand)
    if not ($output | path exists) {
        mkdir $output
    }

    let temp_root = ($env.TEMP? | default ($env.TMPDIR? | default ".") | path expand)
    let work_id = (random uuid)
    let work = ($temp_root | path join $"natcatty-conpty-($work_id)")
    mkdir $work
    let package = ($work | path join "Microsoft.Windows.Console.ConPTY.nupkg")
    let extract_dir = ($work | path join "extract")
    mkdir $extract_dir

    try {
        http get $package_url | save -f $package
        verify-sha $package $package_sha "ConPTY package"
        extract-package $package $extract_dir
        install-file ($extract_dir | path join $dll_entry) ($output | path join "conpty.dll") $dll_sha "conpty.dll"
        install-file ($extract_dir | path join $exe_entry) ($output | path join "OpenConsole.exe") $exe_sha "OpenConsole.exe"
    } catch {|err|
        rm -rf $work
        error make { msg: $err.msg }
    }
    rm -rf $work
    print $"installed ConPTY passthrough assets in ($output)"
}
