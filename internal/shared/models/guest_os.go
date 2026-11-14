package models

import "strings"

// The kinds of system a template can hold. The kind decides how a VM built
// from it is set up: Linux through cloud-init, RouterOS through a script on
// its disk, Windows through cloudbase-init reading the cloud-init disc.
const (
	OSTypeLinux    = "linux"
	OSTypeRouterOS = "routeros"
	OSTypeWindows  = "windows"
)

// Windows Server does not install or run usefully below these.
const (
	WindowsMinRAMMB  = 2048
	WindowsMinDiskGB = 32
)

// ValidOSType reports whether t is a kind this build can install.
func ValidOSType(t string) bool {
	return t == OSTypeLinux || t == OSTypeRouterOS || t == OSTypeWindows
}

// OSLoginUser is the account a new VM of this kind is reached with, and the
// one a password reset changes.
func OSLoginUser(osType string) string {
	switch osType {
	case OSTypeRouterOS:
		return "admin"
	case OSTypeWindows:
		return "Administrator"
	}
	return "root"
}

const guestImagePrefix = "guest:"

// GuestImageRef is the image reference sent to a node for a template of the
// given kind. A Linux image is sent as it always was. Any other kind is
// prefixed with it ("guest:routeros:https://..."), which does two things: the
// node learns how to set the VM up, and a node whose agent predates that kind
// fails to find the image and reports an error, instead of quietly treating a
// router's disk as Linux and handing over a VM that cannot boot.
func GuestImageRef(osType, imagePath string) string {
	if osType == "" || osType == OSTypeLinux {
		return imagePath
	}
	return guestImagePrefix + osType + ":" + imagePath
}

// SplitGuestImageRef undoes GuestImageRef on the node.
func SplitGuestImageRef(ref string) (osType, imagePath string) {
	if rest, ok := strings.CutPrefix(ref, guestImagePrefix); ok {
		if kind, path, ok := strings.Cut(rest, ":"); ok && kind != "" {
			return kind, path
		}
	}
	return OSTypeLinux, ref
}
