package docker

import (
	"io"
	"os/exec"

	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
)

// NewSyncTarget allows writing into a docker daemon
func NewSyncTarget() session.Attachable {
	return &syncTarget{}
}

type syncTarget struct{}

func (r *syncTarget) Register(server *grpc.Server) {
	filesync.RegisterFileSendServer(server, r)
}

func (r *syncTarget) DiffCopy(stream filesync.FileSend_DiffCopyServer) (err error) {
	cmd := exec.CommandContext(stream.Context(), "docker", "load", "--quiet")
	wc, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	errC := make(chan error, 1)
	go func() {
		err := write(stream, wc)
		wc.Close()
		errC <- err
	}()

	err = cmd.Run()
	errWrite := <-errC
	if err == nil {
		err = errWrite
	}
	return err
}

func write(ds grpc.ServerStream, wc io.WriteCloser) error {
	for {
		bm := filesync.BytesMessage{}
		if err := ds.RecvMsg(&bm); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return errors.WithStack(err)
		}
		if _, err := wc.Write(bm.Data); err != nil {
			return errors.WithStack(err)
		}
	}
}
