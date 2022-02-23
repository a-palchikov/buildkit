package local

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/pkg/idtools"
	"github.com/hashicorp/go-multierror"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/progress"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
)

const (
	// preferNondistLayersKey is an exporter option which can be used to mark a layer as non-distributable if the layer reference was
	// already found to use a non-distributable media type.
	// When this option is not set, the exporter will change the media type of the layer to a distributable one.
	preferNondistLayersKey = "prefer-nondist-layers"
	keyLayerCompression    = "compression"
	keyCompressionLevel    = "compression-level"
	keyForceCompression    = "force-compression"
)

type Opt struct {
	SessionManager *session.Manager
}

type localExporter struct {
	opt Opt
	// session manager
}

func New(opt Opt) (exporter.Exporter, error) {
	le := &localExporter{opt: opt}
	return le, nil
}

func (e *localExporter) Resolve(ctx context.Context, opt map[string]string) (exporter.ExporterInstance, error) {
	li := &localExporterInstance{localExporter: e}

	for k, v := range opt {
		switch k {
		case keyLayerCompression:
			switch v {
			case "gzip":
				li.layerCompression = compression.Gzip
			case "estargz":
				li.layerCompression = compression.EStargz
			case "zstd":
				li.layerCompression = compression.Zstd
			case "uncompressed":
				li.layerCompression = compression.Uncompressed
			default:
				return nil, errors.Errorf("unsupported layer compression type: %v", v)
			}
		case keyCompressionLevel:
			ii, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, errors.Wrapf(err, "non-int value %s specified for %s", v, k)
			}
			v := int(ii)
			li.compressionLevel = &v
		case keyForceCompression:
			if v == "" {
				li.forceCompression = true
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value %v specified for %s", v, k)
			}
			li.forceCompression = b
		case preferNondistLayersKey:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, errors.Wrapf(err, "non-bool value %v specified for %s", v, k)
			}
			li.preferNonDist = b
		}
	}

	return li, nil
}

type localExporterInstance struct {
	*localExporter
	preferNonDist    bool
	layerCompression compression.Type
	compressionLevel *int
	forceCompression bool
}

func (e *localExporterInstance) Name() string {
	return "exporting to client"
}

func (e *localExporterInstance) Config() exporter.Config {
	return exporter.Config{}
}

func (e *localExporterInstance) Export(ctx context.Context, inp exporter.Source, sessionID string) (map[string]string, error) {
	var defers []func()

	defer func() {
		for i := len(defers) - 1; i >= 0; i-- {
			defers[i]()
		}
	}()

	getDir := func(ctx context.Context, k string, ref cache.ImmutableRef) (*fsutil.Dir, error) {
		var src string
		var err error
		var idmap *idtools.IdentityMapping
		if ref == nil {
			src, err = ioutil.TempDir("", "buildkit")
			if err != nil {
				return nil, err
			}
			defers = append(defers, func() { os.RemoveAll(src) })
		} else {
			mount, err := ref.Mount(ctx, true, session.NewGroup(sessionID))
			if err != nil {
				return nil, err
			}

			lm := snapshot.LocalMounter(mount)

			src, err = lm.Mount()
			if err != nil {
				return nil, err
			}

			idmap = mount.IdentityMapping()

			defers = append(defers, func() { lm.Unmount() })
		}

		walkOpt := &fsutil.WalkOpt{}

		if idmap != nil {
			walkOpt.Map = func(p string, st *fstypes.Stat) bool {
				uid, gid, err := idmap.ToContainer(idtools.Identity{
					UID: int(st.Uid),
					GID: int(st.Gid),
				})
				if err != nil {
					return false
				}
				st.Uid = uint32(uid)
				st.Gid = uint32(gid)
				return true
			}
		}

		return &fsutil.Dir{
			FS: fsutil.NewFS(src, walkOpt),
			Stat: fstypes.Stat{
				Mode: uint32(os.ModeDir | 0755),
				Path: strings.Replace(k, "/", "_", -1),
			},
		}, nil
	}

	var fs fsutil.FS

	if len(inp.Refs) > 0 {
		dirs := make([]fsutil.Dir, 0, len(inp.Refs))
		for k, ref := range inp.Refs {
			d, err := getDir(ctx, k, ref)
			if err != nil {
				return nil, err
			}
			dirs = append(dirs, *d)
		}
		var err error
		fs, err = fsutil.SubDirFS(dirs)
		if err != nil {
			return nil, err
		}
	} else {
		d, err := getDir(ctx, "", inp.Ref)
		if err != nil {
			return nil, err
		}
		fs = d.FS
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	caller, err := e.opt.SessionManager.Get(timeoutCtx, sessionID, false)
	if err != nil {
		return nil, err
	}

	w, err := filesync.CopyFileWriter(ctx, nil, caller)
	if err != nil {
		return nil, err
	}

	comp := e.compression()
	switch comp.Type {
	case compression.Zstd:
		w, err = zstdWriter(comp, w)
		if err != nil {
			return nil, err
		}
	case compression.Gzip:
		w, err = gzipWriter(comp, w)
		if err != nil {
			return nil, err
		}
	}

	report := oneOffProgress(ctx, "sending tarball")
	if err := fsutil.WriteTar(ctx, fs, w); err != nil {
		w.Close()
		return nil, report(err)
	}
	return nil, report(w.Close())
}

func (e *localExporterInstance) compression() compression.Config {
	c := compression.New(e.layerCompression).SetForce(e.forceCompression)
	if e.compressionLevel != nil {
		c = c.SetLevel(*e.compressionLevel)
	}
	return c
}

func zstdWriter(comp compression.Config, wc io.WriteCloser) (io.WriteCloser, error) {
	level := zstd.SpeedDefault
	if comp.Level != nil {
		level = toZstdEncoderLevel(*comp.Level)
	}
	zw, err := zstd.NewWriter(wc, zstd.WithEncoderLevel(level))
	if err != nil {
		return nil, err
	}
	return multiCloser{
		Writer:  zw,
		closers: []io.Closer{zw, wc},
	}, nil

}

func toZstdEncoderLevel(level int) zstd.EncoderLevel {
	// map zstd compression levels to go-zstd levels
	// once we also have c based implementation move this to helper pkg
	if level < 0 {
		return zstd.SpeedDefault
	} else if level < 3 {
		return zstd.SpeedFastest
	} else if level < 7 {
		return zstd.SpeedDefault
	} else if level < 9 {
		return zstd.SpeedBetterCompression
	}
	return zstd.SpeedBestCompression
}

func gzipWriter(comp compression.Config, wc io.WriteCloser) (io.WriteCloser, error) {
	level := gzip.DefaultCompression
	if comp.Level != nil {
		level = *comp.Level
	}
	gz, err := gzip.NewWriterLevel(wc, level)
	if err != nil {
		return nil, err
	}
	return multiCloser{
		Writer:  gz,
		closers: []io.Closer{gz, wc},
	}, nil
}

// Implements io.Closer
func (r multiCloser) Close() (rerr error) {
	for _, c := range r.closers {
		if err := c.Close(); err != nil {
			rerr = multierror.Append(rerr, err).ErrorOrNil()
		}
	}
	return rerr
}

type multiCloser struct {
	io.Writer
	closers []io.Closer
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.NewFromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}
