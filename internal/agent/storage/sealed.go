package storage

import (
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	sharedstorage "github.com/maburvm/panel/internal/shared/storage"
)

// SealFor wraps a backup store with the keys a request sent: uploads are
// encrypted with store.encryption, downloads decrypted with whichever key a
// file names. A request without keys gets the store as it is.
func SealFor(s sharedstorage.Store, store *pb.ObjectStore) sharedstorage.Store {
	enc := store.GetEncryption()
	if len(enc.GetKey()) == 0 && len(store.GetDecryptionKeys()) == 0 {
		return s
	}
	sealed := &sharedstorage.SealedStore{Store: s}
	if len(enc.GetKey()) > 0 {
		sealed.Key = &sharedstorage.BackupKey{ID: enc.GetId(), Key: enc.GetKey()}
		sealed.Keys = append(sealed.Keys, *sealed.Key)
	}
	for _, k := range store.GetDecryptionKeys() {
		sealed.Keys = append(sealed.Keys, sharedstorage.BackupKey{ID: k.GetId(), Key: k.GetKey()})
	}
	return sealed
}

// SealingKeyID is the key a store from SealFor encrypts uploads with; "" when
// it stores them in the clear.
func SealingKeyID(s sharedstorage.Store) string {
	if k, ok := s.(interface{ SealingKeyID() string }); ok {
		return k.SealingKeyID()
	}
	return ""
}
