package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	outDir := fs.String("out", "build/ghostty-vt", "directory holding the .a artifacts")
	manifestPath := fs.String("manifest", "ui/terminal/artifacts.json", "manifest to verify")
	ndkOpt := fs.String("ndk", "", "Android NDK root (default: $ANDROID_NDK_HOME)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	if err := m.validate(); err != nil {
		return fmt.Errorf("%s: %w", *manifestPath, err)
	}

	work, err := os.MkdirTemp("", "ghosttyvt-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	needAndroid := false
	for _, t := range targets {
		if t.Platform == "android" {
			needAndroid = true
		}
	}
	ndk := ""
	if needAndroid {
		ndk, _, err = requireNDK(*ndkOpt)
		if err != nil {
			return err
		}
	}

	for _, t := range targets {
		record := m.Artifacts[t.Key]
		path := filepath.Join(*outDir, record.File)
		sum, size, err := sha256File(path)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Key, err)
		}
		if sum != record.SHA256 {
			return fmt.Errorf("%s: %s has sha256 %s, manifest records %s", t.Key, path, sum, record.SHA256)
		}
		if size != record.Size {
			return fmt.Errorf("%s: %s is %d bytes, manifest records %d", t.Key, path, size, record.Size)
		}
		if t.Platform == "ios" && runtime.GOOS != "darwin" {
			log.Printf("%s: checksum ok; symbol inspection needs macOS", t.Key)
			continue
		}
		a := builtArtifact{Target: t, Path: path}
		if err := inspectArtifact(&a, ndk, work); err != nil {
			return err
		}
	}
	log.Printf("%s: all artifacts match the manifest and their ABI", *manifestPath)
	return nil
}

func cmdManifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	path := fs.String("manifest", "ui/terminal/artifacts.json", "manifest to inspect")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := readManifest(*path)
	if err != nil {
		return err
	}
	if err := m.validate(); err != nil {
		return fmt.Errorf("%s: %w", *path, err)
	}
	data, err := m.marshal()
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}
