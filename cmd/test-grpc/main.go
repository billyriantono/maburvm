package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

func main() {
	address := os.Getenv("TEST_GRPC_ADDRESS")
	token := os.Getenv("TEST_GRPC_TOKEN")
	if address == "" || token == "" {
		fmt.Println("TEST_GRPC_ADDRESS and TEST_GRPC_TOKEN are required")
		return
	}

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	creds := credentials.NewTLS(tlsCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
	)
	if err != nil {
		fmt.Printf("DIAL ERROR: %v\n", err)
		return
	}
	defer conn.Close()
	fmt.Println("Connected!")

	client := pb.NewNodeAgentClient(conn)

	md := metadata.New(map[string]string{
		"authorization": "Bearer " + token,
	})
	authCtx := metadata.NewOutgoingContext(ctx, md)

	resp, err := client.GetNodeInfo(authCtx, &pb.GetNodeInfoRequest{})
	if err != nil {
		fmt.Printf("GetNodeInfo ERROR: %v\n", err)
		return
	}
	fmt.Printf("Success! OS: %s, CPU cores: %d, Memory: %d bytes, Disk: %d bytes\n",
		resp.OsInfo.GetOsName(), resp.CpuInfo.GetCores(), resp.MemoryTotalBytes, resp.DiskTotalBytes)
	fmt.Printf("Live metrics: CPU=%.1f%%, Mem=%.1f%%, Disk=%.1f%%\n",
		resp.GetCpuPercent(), resp.GetMemoryUsedPercent(), resp.GetDiskUsedPercent())
	fmt.Printf("Network: RX=%d B/s, TX=%d B/s\n",
		resp.GetNetworkRxBytesPerSec(), resp.GetNetworkTxBytesPerSec())
	fmt.Printf("Disk I/O: Read=%d B/s, Write=%d B/s\n",
		resp.GetDiskReadBytesPerSec(), resp.GetDiskWriteBytesPerSec())
	fmt.Printf("Load: %.2f / %.2f / %.2f\n",
		resp.GetLoadAvg_1(), resp.GetLoadAvg_5(), resp.GetLoadAvg_15())
	fmt.Printf("VMs: %d, Available CPUs: %d, Mem: %d MB, Disk: %d GB\n",
		resp.GetRunningVmCount(), resp.GetAvailableCpus(), resp.GetAvailableMemoryMb(), resp.GetAvailableDiskGb())
}
