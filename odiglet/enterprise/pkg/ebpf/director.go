package ebpf

import (
	"github.com/keyval-dev/odigos/odiglet/pkg/ebpf"
)

type DirectorEnterprise interface {
	ebpf.Director

	SetInstrumentationOptionBool(optionName string, library string, value bool) error
}
