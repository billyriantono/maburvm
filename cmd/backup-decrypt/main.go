// Command backup-decrypt turns an encrypted backup file back into the qcow2
// image it was, without the panel: for when the panel itself is lost. It reads
// the keys exported from the panel (Settings, Backup destinations, Export keys)
// and picks the one the file names. A file stored in the clear is copied as it
// is.
//
//	backup-decrypt -keys keys.json < backup.qcow2.enc > backup.qcow2
//	backup-decrypt -keys keys.json -in backup.qcow2 -out restored.qcow2
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/maburvm/panel/internal/shared/storage"
)

func main() {
	keysPath := flag.String("keys", "", "JSON file of exported keys: [{\"id\": ..., \"key\": \"64 hex digits\"}]")
	in := flag.String("in", "-", "encrypted file, - for standard input")
	out := flag.String("out", "-", "where to write the image, - for standard output")
	flag.Parse()
	if err := run(*keysPath, *in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "backup-decrypt:", err)
		os.Exit(1)
	}
}

func run(keysPath, in, out string) error {
	if keysPath == "" {
		return errors.New("-keys is required")
	}
	keys, err := readKeys(keysPath)
	if err != nil {
		return err
	}
	src := io.Reader(os.Stdin)
	if in != "-" {
		f, err := os.Open(in)
		if err != nil {
			return err
		}
		defer f.Close()
		src = f
	}
	plain, encrypted, err := storage.Unseal(src, keys)
	if err != nil {
		return err
	}
	dst := io.Writer(os.Stdout)
	var file *os.File
	if out != "-" {
		if file, err = os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
			return err
		}
		dst = file
	}
	if _, err := io.Copy(dst, plain); err != nil {
		if file != nil {
			file.Close()
			os.Remove(out) // half an image is worse than none
		}
		return err
	}
	if file != nil {
		if err := file.Close(); err != nil {
			return err
		}
	}
	if !encrypted {
		fmt.Fprintln(os.Stderr, "backup-decrypt: the file was not encrypted; copied as it is")
	}
	return nil
}

func readKeys(path string) ([]storage.BackupKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// The export endpoint's whole answer works as well as its data list.
	var envelope struct {
		Data []struct{ ID, Key string } `json:"data"`
	}
	var list []struct{ ID, Key string }
	if json.Unmarshal(raw, &list) != nil {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("%s is not a list of exported keys: %w", path, err)
		}
		list = envelope.Data
	}
	out := make([]storage.BackupKey, 0, len(list))
	for _, k := range list {
		b, err := hex.DecodeString(k.Key)
		if err != nil || len(b) != 32 {
			return nil, fmt.Errorf("key %s is not 64 hex digits", k.ID)
		}
		out = append(out, storage.BackupKey{ID: k.ID, Key: b})
	}
	return out, nil
}
