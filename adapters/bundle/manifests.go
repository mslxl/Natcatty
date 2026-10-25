package bundle

// Release sources and SHA-256 digests. These values are trusted data compiled
// into the app. The fetch tool verifies the archive digest before extraction
// and the app verifies the binary digest before every launch, so a modified
// file or archive is never executed.
//
// Mosh: https://github.com/binaricat/MoshCatty (GPL-3.0-or-later)
// ET:   https://github.com/binaricat/Netcatty-et-bin (Apache-2.0, upstream
//
//	MisterTea/EternalTerminal at et-v6.2.10, commit
//	f9a584ac06b2f1730b5bdf0a27150f28478368fb; telemetry disabled).
const (
	// MoshRelease is the MoshCatty release the mosh-client binaries come from.
	MoshRelease = "moshcatty-0.1.8"
	// MoshRepository is the public release repository.
	MoshRepository = "https://github.com/binaricat/MoshCatty"
	// ETRelease is the Netcatty-et-bin release the et binaries come from.
	ETRelease = "et-bin-6.2.10-1"
	// ETRepository is the public release repository.
	ETRepository = "https://github.com/binaricat/Netcatty-et-bin"
)

// moshAssets is the MoshCatty release manifest.
var moshAssets = map[string]Asset{
	"win32-x64": {
		Asset:         "mosh-client-win32-x64.tar.gz",
		ArchiveSHA256: "ee437592c351361e47c600fafe04ce97a2551aa61fbef1fb3d6dda565835311e",
		Binary:        "mosh-client.exe",
		BinarySHA256:  "e44616a22038f1742f765c7de553796ba4d1698f2c1f7a7cfb0dafd33ecdc78a",
		Size:          545280,
	},
	"linux-x64": {
		Asset:         "mosh-client-linux-x64.tar.gz",
		ArchiveSHA256: "c911d70eac729cb368667766ed817f7aa0a8dd8e2f8a006c38777900d6ef3575",
		Binary:        "mosh-client",
		BinarySHA256:  "215e2527961058a625e13afb5f7e4bc372cec11314260258dcfa2347961f48b8",
		Size:          653960,
	},
	"linux-arm64": {
		Asset:         "mosh-client-linux-arm64.tar.gz",
		ArchiveSHA256: "ebeb217e11f4bec34269fd06373d067d587fa931808cfa7d5d58d870fc3a846a",
		Binary:        "mosh-client",
		BinarySHA256:  "2553988bea5af73d6d15ecad918855e375ddbf46393ce9379a8ad25c69ceb069",
		Size:          582464,
	},
	"darwin-universal": {
		Asset:         "mosh-client-darwin-universal.tar.gz",
		ArchiveSHA256: "daa2da20d2f135190fdbebf952a692d80eed0e21d48cee252a28e901bccc2402",
		Binary:        "mosh-client",
		BinarySHA256:  "460662ccb768e8ffcc7f0c07f58bb5dd1c32202c3b7e4062207a91a722eb83ed",
		Size:          1159056,
	},
}

// etAssets is the Netcatty-et-bin release manifest.
var etAssets = map[string]Asset{
	"win32-x64": {
		Asset:         "et-win32-x64.tar.gz",
		ArchiveSHA256: "d8fb0056a08fee0551d8526d4b2cb5674b4f757540147de20f00ecc944064cab",
		Binary:        "et.exe",
		BinarySHA256:  "6212e2c089cd55aa9455dc2a9de22ac83d0bc6c8af76ce9ad096668931ee5a25",
		Size:          13654528,
	},
	"linux-x64": {
		Asset:         "et-linux-x64.tar.gz",
		ArchiveSHA256: "25149ca428107822dc374fe186e1dd49098ab8863b899a49f03048ef9dc94935",
		Binary:        "et",
		BinarySHA256:  "2f1b3a4fdda433246783b46669d8099707808a27b74f661acf80af24ec4d7d6d",
		Size:          9741112,
	},
	"linux-arm64": {
		Asset:         "et-linux-arm64.tar.gz",
		ArchiveSHA256: "c0f0fbe18bdd95c409bba11180a98f36876aa03f063ef1fa27048d86471b58ea",
		Binary:        "et",
		BinarySHA256:  "408fe4831b80a0ed89a7cbdcabffccb874c7d1d95b3b16ead09717255e035ea4",
		Size:          9014632,
	},
	"darwin-universal": {
		Asset:         "et-darwin-universal.tar.gz",
		ArchiveSHA256: "d26855cc2272fe3f214d84f482850febe94461703111838c405e968f3eaa0128",
		Binary:        "et",
		BinarySHA256:  "64c46d7f4f84724eb7fa0bfae7321e20896568fb771de418698e61bf9749d983",
		Size:          13764928,
	},
}

// MoshAssets returns a copy of the MoshCatty release manifest.
func MoshAssets() map[string]Asset { return cloneAssets(moshAssets) }

// ETAssets returns a copy of the Netcatty-et-bin release manifest.
func ETAssets() map[string]Asset { return cloneAssets(etAssets) }

// MoshSpec builds the resolver for bundled mosh-client binaries.
func MoshSpec(roots []string) Spec {
	return Spec{Kind: Mosh, Roots: roots, Targets: targets(moshAssets)}
}

// ETSpec builds the resolver for bundled et binaries.
func ETSpec(roots []string) Spec { return Spec{Kind: ET, Roots: roots, Targets: targets(etAssets)} }

func targets(assets map[string]Asset) map[string]Entry {
	result := make(map[string]Entry, len(assets))
	for target, asset := range assets {
		result[target] = Entry{Binary: asset.Binary, SHA256: asset.BinarySHA256, Size: asset.Size}
	}
	return result
}

func cloneAssets(source map[string]Asset) map[string]Asset {
	result := make(map[string]Asset, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
