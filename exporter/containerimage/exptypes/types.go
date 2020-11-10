package exptypes

import (
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ExporterConfigDigestKey      = "config.digest"
	ExporterImageDigestKey       = "containerimage.digest"
	ExporterImageConfigKey       = "containerimage.config"
	ExporterImageConfigDigestKey = "containerimage.config.digest"
	ExporterImageDescriptorKey   = "containerimage.descriptor"
	ExporterBuildInfo            = "containerimage.buildinfo"
	ExporterPlatformsKey         = "refs.platforms"
	ExporterEpochKey             = "source.date.epoch"
)

// KnownRefMetadataKeys are the subset of exporter keys that can be suffixed by
// a platform to become platform specific
var KnownRefMetadataKeys = []string{
	ExporterImageConfigKey,
	ExporterBuildInfo,
}

type Platforms struct {
	Platforms []Platform
}

type Platform struct {
	ID       string
	Platform ocispecs.Platform
}

type InlineCache struct {
	Cache []byte
	// Platforms maps platform -> inline cache
	Platforms map[string][]byte
}
