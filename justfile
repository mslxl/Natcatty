export CGO_ENABLED := "0"

# Run all tests without using cached results.
test:
    go test ./... -count=1

# Package the app for the current operating system and architecture in build/.
build:
    go tool mygo build

# Compare the interface with the committed visual baselines (ui/*/testdata/visual).
visual:
    go test -tags visual -run Visual -count=1 ./ui/...

# Rewrite the visual baselines after a deliberate change to how the interface looks.
visual-update:
    NATCATTY_VISUAL_UPDATE=1 go test -tags visual -run Visual -count=1 ./ui/...
