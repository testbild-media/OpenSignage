// Command osrelease prepares and checks OpenSignage releases.
//
//	osrelease keygen -out KEYFILE                         create an Ed25519 key pair
//	osrelease assemble -dir DIST -version V [-notes F] -key KEYFILE
//	    collects dist/opensignage-V-linux-{amd64,arm64,armv7}.tar.gz, writes release.json, SHA256SUMS,
//	    SHA256SUMS.sig and the offline bundle opensignage-V-bundle.tar into dist/release-V/
//	osrelease verify -dir DIR -pubkey KEY                 verify a release directory
//
// The private key never belongs into the repository.
package main

import (
	"archive/tar"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opensignage/internal/common/release"
	"opensignage/internal/common/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: osrelease keygen|assemble|verify …")
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	out := fs.String("out", "", "key file to write (keygen)")
	dir := fs.String("dir", "dist", "directory")
	ver := fs.String("version", "", "release version")
	notes := fs.String("notes", "", "file with release notes (markdown)")
	keyFile := fs.String("key", "", "private key file")
	pub := fs.String("pubkey", "", "trusted public key (osk1:…)")
	minFrom := fs.String("min-upgrade-from", "", "oldest version that can upgrade directly")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch cmd {
	case "keygen":
		return keygen(*out)
	case "assemble":
		return assemble(*dir, *ver, *notes, *keyFile, *minFrom)
	case "verify":
		v, err := release.VerifyDir(*dir, []string{*pub}, false)
		if err != nil {
			return err
		}
		fmt.Printf("ok: %s (%d packages)\n", v.Info.Version, len(v.Info.Packages))
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func keygen(out string) error {
	if out == "" {
		return fmt.Errorf("-out is required")
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s exists; refusing to overwrite a key", out)
	}
	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println("private key written to", out)
	fmt.Println("public key:", release.FormatPublicKey(pubKey))
	return nil
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s is not an osrelease private key", path)
	}
	return ed25519.PrivateKey(raw), nil
}

// packageArches are the architectures scripts/build.sh builds Linux packages for.
var packageArches = []string{"amd64", "arm64", "armv7"}

func assemble(dist, ver, notesFile, keyFile, minFrom string) error {
	if ver == "" || keyFile == "" {
		return fmt.Errorf("-version and -key are required")
	}
	if !version.ValidRelease(ver) {
		return fmt.Errorf("invalid version %q (expected MAJOR.MINOR.PATCH[-pre])", ver)
	}
	if minFrom != "" && !version.ValidRelease(minFrom) {
		return fmt.Errorf("invalid -min-upgrade-from %q", minFrom)
	}
	priv, err := loadKey(keyFile)
	if err != nil {
		return err
	}
	// Exactly opensignage-<v>-linux-<arch>.tar.gz: a pattern would also pick up the
	// packages of <v>-rc1 and others.
	var matches []string
	for _, arch := range packageArches {
		p := filepath.Join(dist, "opensignage-"+ver+"-linux-"+arch+".tar.gz")
		st, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		matches = append(matches, p)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no packages for %s in %s (run scripts/build.sh first)", ver, dist)
	}
	outDir := filepath.Join(dist, "release-"+ver)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	info := release.Info{Version: ver, Date: time.Now().UTC().Format(time.RFC3339), MinUpgradeFrom: minFrom}
	if notesFile != "" {
		b, err := os.ReadFile(notesFile)
		if err != nil {
			return err
		}
		info.Notes = string(b)
	}
	sums := map[string]string{}
	for _, m := range matches {
		name := filepath.Base(m)
		rest := strings.TrimSuffix(strings.TrimPrefix(name, "opensignage-"+ver+"-"), ".tar.gz")
		osName, arch, ok := strings.Cut(rest, "-")
		if !ok {
			return fmt.Errorf("unexpected package name %s", name)
		}
		if err := copyFile(m, filepath.Join(outDir, name)); err != nil {
			return err
		}
		sum, size, err := release.FileSHA256(m)
		if err != nil {
			return err
		}
		info.Packages = append(info.Packages, release.Package{OS: osName, Arch: arch, File: name, SHA256: sum, Size: size})
		sums[name] = sum
	}
	infoData, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, release.InfoFile), infoData, 0o644); err != nil {
		return err
	}
	sum, _, err := release.FileSHA256(filepath.Join(outDir, release.InfoFile))
	if err != nil {
		return err
	}
	sums[release.InfoFile] = sum
	sumsData := release.FormatSums(sums)
	if err := os.WriteFile(filepath.Join(outDir, release.SumsFile), sumsData, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, release.SigFile), []byte(release.Sign(priv, sumsData)+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := release.VerifyDir(outDir, []string{release.FormatPublicKey(priv.Public().(ed25519.PublicKey))}, false); err != nil {
		return fmt.Errorf("self-check failed: %w", err)
	}
	bundle := filepath.Join(outDir, "opensignage-"+ver+"-bundle.tar")
	if err := writeBundle(bundle, outDir, append([]string{release.InfoFile, release.SumsFile, release.SigFile}, keys(sums)...)); err != nil {
		return err
	}
	fmt.Println("release assembled in", outDir)
	return nil
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		if k != release.InfoFile {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func writeBundle(path, dir string, names []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	for _, n := range names {
		src, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			f.Close()
			return err
		}
		st, err := src.Stat()
		if err != nil {
			src.Close()
			f.Close()
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}); err != nil {
			src.Close()
			f.Close()
			return err
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			f.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
