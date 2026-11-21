package models

import (
	"encoding/hex"
	"path"
	"regexp"
	"strings"
)

// BlankImageRef is the image reference of a VM installed from an ISO: the node
// makes an empty disk instead of copying a template. An agent that predates it
// takes it for a path, finds nothing and fails the create.
const BlankImageRef = "blank:"

// Metadata the panel sends with a create, an attach or a detach of an ISO.
const (
	// ISOSlotKey picks the CD drive: "1" (the install drive, the default) or
	// "2" (a second drive, for a drivers disc).
	ISOSlotKey = "iso_slot"
	// ISOSHA256Key is the checksum an ISO being attached must have.
	ISOSHA256Key = "iso_sha256"
	// InstallISOKey and DriversISOKey are the ISOs a blank VM starts with, in
	// drives 1 and 2; each *SHA256Key is that image's checksum.
	InstallISOKey       = "install_iso"
	InstallISOSHA256Key = "install_iso_sha256"
	DriversISOKey       = "drivers_iso"
	DriversISOSHA256Key = "drivers_iso_sha256"
)

// FirmwareKey asks for a guest booted with UEFI ("uefi"); without it a VM
// boots with BIOS. Sent only to an agent advertising FeatureUEFI.
const FirmwareKey = "firmware"

// FeatureUEFI is the agent feature (GetNodeInfo) of booting UEFI guests.
const FeatureUEFI = "uefi"

// ISO drive slots.
const (
	ISOSlotInstall = 1
	ISOSlotDrivers = 2
)

const uploadImagePrefix = "upload:"

var uploadNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// ValidUploadName reports a file name an uploaded image may have: a plain
// name, no path.
func ValidUploadName(name string) bool {
	return uploadNameRe.MatchString(name) && !strings.Contains(name, "..")
}

// ValidSHA256 reports a lower-case hex sha256.
func ValidSHA256(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// UploadImageRef is the reference of an image uploaded to the panel and copied
// to the nodes: "upload:<sha256>/<name>". The node finds it in its own cache.
func UploadImageRef(sha256, name string) string {
	return uploadImagePrefix + sha256 + "/" + name
}

// SplitUploadImageRef undoes UploadImageRef; ok is false for any other
// reference or a malformed one.
func SplitUploadImageRef(ref string) (sha256, name string, ok bool) {
	rest, found := strings.CutPrefix(ref, uploadImagePrefix)
	if !found {
		return "", "", false
	}
	sha256, name, found = strings.Cut(rest, "/")
	if !found || !ValidSHA256(sha256) || !ValidUploadName(name) || path.Base(name) != name {
		return "", "", false
	}
	return sha256, name, true
}
