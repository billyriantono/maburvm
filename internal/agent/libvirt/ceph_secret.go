package libvirt

import (
	"fmt"
	"strings"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// DefineCephSecret stores a cephx key in libvirt as a secret of usage type
// ceph, so QEMU can open RBD disks without the key being in any domain's XML.
// The UUID is the cluster's id: every node holds the cluster's secret under
// the same UUID, and a VM's definition works on each of them. Defining it
// again replaces the value.
func DefineCephSecret(uuid string, key []byte) error {
	doc, err := (&libvirtxml.Secret{Ephemeral: "no", Private: "yes", UUID: uuid,
		Description: "MaburVM: Ceph cluster " + uuid,
		Usage:       &libvirtxml.SecretUsage{Type: "ceph", Name: "maburvm-ceph-" + uuid}}).Marshal()
	if err != nil {
		return err
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		sec, err := conn.SecretDefineXML(doc, 0)
		if err != nil {
			return fmt.Errorf("libvirt could not define the cluster's secret: %w", err)
		}
		defer sec.Free()
		if err := sec.SetValue(key, 0); err != nil {
			return fmt.Errorf("libvirt could not store the cluster's key: %w", err)
		}
		return nil
	})
}

// HasSecret reports whether libvirt holds a secret with this UUID. Only
// libvirt saying there is none is a "no": a libvirt that cannot be asked
// right now is not reported as having lost the secret.
func HasSecret(uuid string) bool {
	found := true
	_ = WithConnection(func(conn *libvirt.Connect) error {
		sec, err := conn.LookupSecretByUUIDString(uuid)
		if err == nil {
			_ = sec.Free()
		} else if lerr, ok := err.(libvirt.Error); ok && lerr.Code == libvirt.ERR_NO_SECRET {
			found = false
		}
		return nil
	})
	return found
}

// UndefineSecret removes a secret; one that is not there is not an error.
func UndefineSecret(uuid string) error {
	return WithConnection(func(conn *libvirt.Connect) error {
		sec, err := conn.LookupSecretByUUIDString(uuid)
		if err != nil {
			if lerr, ok := err.(libvirt.Error); ok && lerr.Code == libvirt.ERR_NO_SECRET {
				return nil
			}
			return fmt.Errorf("libvirt could not look up the cluster's secret: %w", err)
		}
		defer sec.Free()
		return sec.Undefine()
	})
}

// SecretUsers names the domains whose definition, running or stored, refers
// to a secret. A domain that cannot be read fails the call: the caller is
// about to remove the secret.
func SecretUsers(uuid string) ([]string, error) {
	var users []string
	err := WithConnection(func(conn *libvirt.Connect) error {
		doms, err := conn.ListAllDomains(0) // 0 = all, active and inactive
		if err != nil {
			return fmt.Errorf("failed to list domains: %w", err)
		}
		defer func() {
			for i := range doms {
				_ = doms[i].Free()
			}
		}()
		for i := range doms {
			for _, flags := range []libvirt.DomainXMLFlags{0, libvirt.DOMAIN_XML_INACTIVE} {
				doc, err := doms[i].GetXMLDesc(flags)
				if err != nil {
					return fmt.Errorf("failed to read a domain: %w", err)
				}
				// The secret's UUID appears in a definition only as a disk's
				// <secret uuid=...>.
				if strings.Contains(doc, uuid) {
					name, _ := doms[i].GetName()
					users = append(users, name)
					break
				}
			}
		}
		return nil
	})
	return users, err
}
