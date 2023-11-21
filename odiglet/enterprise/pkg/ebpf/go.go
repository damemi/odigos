package ebpf

import (
	"fmt"

	"github.com/keyval-dev/odigos/odiglet/pkg/ebpf"
)

type InstrumentationDirectorGoEnterprise struct {
	*ebpf.InstrumentationDirectorGo
}

func NewInstrumentationDirectorGoEnterprise() (*InstrumentationDirectorGoEnterprise, error) {
	goDirector, err := ebpf.NewInstrumentationDirectorGo()
	if err != nil {
		return nil, err
	}

	goDir, ok := goDirector.(*ebpf.InstrumentationDirectorGo)
	if !ok {
		return nil, fmt.Errorf("failed to cast to ebpf.InstrumentationDirectorGo")
	}

	return &InstrumentationDirectorGoEnterprise{
		InstrumentationDirectorGo: goDir,
	}, nil
}

func (d *InstrumentationDirectorGoEnterprise) SetInstrumentationOptionBool(optionName string, library string, value bool) error {
	inst, exists := d.InstrumentationDirectorGo.GetInstrumentation(1234)
	inst.ConfigBool(library, optionName, value)
	fmt.Println(exists)
	return nil
}
