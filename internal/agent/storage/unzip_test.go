package storage

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackIfZip(t *testing.T) {
	dir := t.TempDir()

	// A zip holding a note and an image: the image (the larger file) replaces it.
	image := bytes.Repeat([]byte("disk"), 4096)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	w.Write([]byte("hello"))
	w, _ = zw.Create("../../etc/chr-7.img") // a hostile name must not matter
	w.Write(image)
	zw.Close()
	zipped := filepath.Join(dir, "chr.img.zip")
	if err := os.WriteFile(zipped, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UnpackIfZip(zipped); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(zipped)
	if !bytes.Equal(got, image) {
		t.Errorf("unpacked %d bytes, want the %d byte image", len(got), len(image))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left %d files behind, want 1", len(entries))
	}

	// Anything that is not a zip is left exactly as it was.
	plain := filepath.Join(dir, "debian.qcow2")
	os.WriteFile(plain, []byte("QFI\xfb not a zip"), 0o644)
	if err := UnpackIfZip(plain); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(plain); string(got) != "QFI\xfb not a zip" {
		t.Errorf("a plain image was changed: %q", got)
	}
}
