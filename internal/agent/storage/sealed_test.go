package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	sharedstorage "github.com/maburvm/panel/internal/shared/storage"
)

func TestSealFor(t *testing.T) {
	var plain sharedstorage.Store = &sharedstorage.SealedStore{} // any store
	assert.Same(t, plain, SealFor(plain, nil), "no keys: the store as it is")
	assert.Same(t, plain, SealFor(plain, &pb.ObjectStore{Bucket: "b"}))

	key := make([]byte, 32)
	got := SealFor(plain, &pb.ObjectStore{
		Encryption:     &pb.BackupKey{Id: "00000000-0000-4000-8000-000000000002", Key: key},
		DecryptionKeys: []*pb.BackupKey{{Id: "00000000-0000-4000-8000-000000000001", Key: key}},
	})
	assert.Equal(t, "00000000-0000-4000-8000-000000000002", SealingKeyID(got))
	assert.Len(t, got.(*sharedstorage.SealedStore).Keys, 2)

	restore := SealFor(plain, &pb.ObjectStore{DecryptionKeys: []*pb.BackupKey{{Id: "00000000-0000-4000-8000-000000000001", Key: key}}})
	assert.Equal(t, "", SealingKeyID(restore), "a restore decrypts but writes nothing")
	assert.Equal(t, "", SealingKeyID(plain))
}
