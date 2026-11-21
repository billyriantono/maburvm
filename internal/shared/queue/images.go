package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/secret"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UploadedImagePath is where the panel keeps an image uploaded to it (an
// ISO), named by its checksum.
func UploadedImagePath(sha256, name string) string {
	return filepath.Join(secret.DataDir(), "images", sha256+"-"+name)
}

// imageChunk is how much of an uploaded image goes in one message.
const imageChunk = 1 << 20

// PushUploadedImage copies an image uploaded to the panel to a node, which
// checks it against its sha256. A node that has it already says so at once.
func PushUploadedImage(ctx context.Context, client pb.NodeAgentClient, sha256, name string) error {
	if !models.ValidSHA256(sha256) || !models.ValidUploadName(name) {
		return fmt.Errorf("%q is not an uploaded image", name)
	}
	f, err := os.Open(UploadedImagePath(sha256, name))
	if err != nil {
		return fmt.Errorf("the uploaded image %s is not on the panel any more: %w", name, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	stream, err := client.PutImage(ctx)
	if err != nil {
		return err
	}
	first := &pb.ImageChunk{Name: name, Sha256: sha256, SizeBytes: fi.Size()}
	buf := make([]byte, imageChunk)
	for msg := first; ; msg = new(pb.ImageChunk) {
		n, rerr := io.ReadFull(f, buf)
		msg.Data = buf[:n]
		if serr := stream.Send(msg); serr != nil {
			// The node may have answered already (it has the image, or refused);
			// its answer is what CloseAndRecv returns.
			break
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
		if rerr != nil {
			_ = stream.CloseSend()
			return rerr
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return errors.New("this node's agent cannot take images uploaded to the panel; update it under Nodes")
		}
		return err
	}
	if !resp.Success {
		return fmt.Errorf("the node refused the image: %s", resp.GetError().GetMessage())
	}
	return nil
}

// agentHasFeature asks a node's agent whether it can do something an older
// one cannot.
func agentHasFeature(ctx context.Context, client pb.NodeAgentClient, feature string) bool {
	info, err := client.GetNodeInfo(ctx, &pb.GetNodeInfoRequest{})
	if err != nil {
		return false
	}
	for _, f := range info.GetFeatures() {
		if f == feature {
			return true
		}
	}
	return false
}
