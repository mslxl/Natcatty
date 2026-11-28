# The picture decoders under ui/terminal/lensview call libjpeg and libwebp
# through cgo, so a drawing block can show the rows of an image that is still
# arriving. The nix dev shells supply both libraries (see flake.nix).
export CGO_ENABLED := "1"

# Apply the MyGo patches to the submodule after checking it out.
prepare-mygo:
    scripts/apply-mygo-patches.sh

# Run all tests without using cached results.
test: prepare-mygo
    go test ./... -count=1

# Run a development build. Its Vault and secrets stay in ./.natcatty-test/.
dev: prepare-mygo
    go run .

# Package the app for the current operating system and architecture in build/.
build: prepare-mygo
    go tool mygo build
