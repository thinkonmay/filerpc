package client

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	mlspb "github.com/pigeatgarlic/grpc-file-server/pkg/protobuf"
)

const (
	// 512kB * (second/nanosec) / 50MB/s
	unittime = int64((512 * 1024) * (1000 * 1000 * 1000) / (50 * 1024 * 1024))
)

// MLSClient maintains info for talking to MLS service
type MLSClient struct {
	conn  *grpc.ClientConn
	route mlspb.MLSServiceClient
}

// NewClient will create a New MLSClient
func NewClient(conn *grpc.ClientConn) *MLSClient {
	return &MLSClient{
		conn:  conn,
		route: mlspb.NewMLSServiceClient(conn),
	}
}

// Upload a file to the MLS Service
func (mc *MLSClient) Upload(ctx context.Context, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"file_name", info.Name(),
		"file_size", strconv.FormatInt(info.Size(), 10),
	))

	stream, err := mc.route.Upload(ctx)
	if err != nil {
		return err
	}

	done := false
	bytes_sent := 0
	sent, success := []int64{}, []int64{}
	channel := make(chan []byte, 1000)

	go func() {
		var count int64 = 1
		for {
			buf := <-channel
			if buf == nil {
				return
			}

			sent = append(sent, count)
			chunk := &mlspb.Chunk{
				Id:      count,
				Content: buf,
				Sum256:  fmt.Sprintf("%x", md5.Sum(buf)),
			}
			if err := stream.Send(chunk); err != nil {
				if err == io.EOF {
					break
				}

				panic(err)
			}

			count++
			bytes_sent += len(buf)
		}
	}()

	go func() {
		prev := time.Now().UnixNano()
		for {
			now := time.Now().UnixNano()
			if now-prev < unittime {
				time.Sleep(time.Duration(unittime - (now - prev)))
			}
			prev = now

			buf := make([]byte, 1024*512) // 512KB chunk
			n, err := f.Read(buf)
			if err != nil {
				if err == io.EOF {
					done = true
					channel <- nil
					break
				}

				panic(err)
			}

			channel <- buf[:n]
		}
	}()

	go func() {
		for {
			status, err := stream.Recv()
			if err != nil {
				panic(err)
			}

			success = status.Success
		}
	}()

	for {
		time.Sleep(1 * time.Second)
		if !done || bytes_sent != int(info.Size()) {
			continue
		}

		pass := true
		for _, v := range sent {
			included := false
			for _, v2 := range success {
				if v == v2 {
					included = true
				}
			}

			if !included {
				pass = false
			}
		}

		if pass {
			break
		}
	}

	fmt.Printf("Finished sending file...\n")
	mc.CloseConn()
	return nil
}

// CloseConn
func (mc *MLSClient) CloseConn() error {
	return mc.conn.Close()
}
