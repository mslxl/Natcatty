export CGO_ENABLED := "0"

# Run all tests without using cached results.
test:
    go test ./... -count=1

# Run a development build. Its Vault and secrets stay in ./.natcatty-test/.
dev:
    go run .

# Package the app for the current operating system and architecture in build/.
build:
    go tool mygo build
