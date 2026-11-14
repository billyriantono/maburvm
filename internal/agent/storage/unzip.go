package storage

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
)

// maxUnpackedImage bounds what one archive may expand to, so a bad URL cannot
// fill a node's disk. Far above any image a template ships as a zip.
const maxUnpackedImage = 64 << 30

// UnpackIfZip replaces a downloaded zip archive with the disk image inside it,
// and leaves any other file alone. Some vendors publish their image only as a
// zip (MikroTik's Cloud Hosted Router is one), and nothing downstream can read
// that.
//
// The file is recognised by its content, not its name, and the largest entry
// is taken as the image. Entry names are never used as paths.
func UnpackIfZip(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	magic := make([]byte, 4)
	_, err = io.ReadFull(f, magic)
	f.Close()
	if err != nil || !bytes.Equal(magic, []byte("PK\x03\x04")) {
		return nil
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer zr.Close()
	var image *zip.File
	for _, entry := range zr.File {
		if entry.FileInfo().Mode().IsRegular() && (image == nil || entry.UncompressedSize64 > image.UncompressedSize64) {
			image = entry
		}
	}
	if image == nil {
		return fmt.Errorf("the archive holds no file")
	}
	if image.UncompressedSize64 > maxUnpackedImage {
		return fmt.Errorf("the image in the archive is %d bytes, over the %d limit", image.UncompressedSize64, int64(maxUnpackedImage))
	}

	src, err := image.Open()
	if err != nil {
		return fmt.Errorf("failed to read the archive: %w", err)
	}
	defer src.Close()
	tmp := path + ".unpacked"
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// The limit again, on the bytes actually written: the size in the header
	// is whatever the archive's author typed.
	n, err := io.Copy(dst, io.LimitReader(src, maxUnpackedImage+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxUnpackedImage {
		err = fmt.Errorf("the image in the archive is over the %d byte limit", int64(maxUnpackedImage))
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("failed to unpack the image: %w", err)
	}
	return os.Rename(tmp, path)
}
