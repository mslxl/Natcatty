export CGO_ENABLED := "0"

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
