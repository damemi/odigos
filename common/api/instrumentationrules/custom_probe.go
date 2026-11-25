package instrumentationrules

import (
	"errors"
	"fmt"
)

// The languages a CustomProbe can be written for.
const (
	CustomProbeLanguageJava   = "java"
	CustomProbeLanguageGo     = "go"
	CustomProbeLanguageCpp    = "cpp"
	CustomProbeLanguagePhp    = "php"
	CustomProbeLanguagePython = "python"
)

// ErrUnknownCustomProbeLanguage is returned for a CustomProbe whose language has no custom probes.
var ErrUnknownCustomProbeLanguage = errors.New("unknown language")

// CustomProbe is one custom probe in flat form: a language and that language's
// fields, as a caller that is not writing a rule spec holds it (a tool call's
// arguments, a model's reply). Fields of other languages are ignored.
type CustomProbe struct {
	Language string

	// java (with MethodName) and php (optional, with FunctionName)
	ClassName string
	// java
	MethodName string
	// go
	PackageName string
	// go and php
	FunctionName string
	// go
	ReceiverName       string
	ReceiverMethodName string
	// cpp
	Signature string
	// python
	Module   string
	Qualname string
	Captures []PythonCapture
}

func (p CustomProbe) java() JavaCustomProbe {
	return JavaCustomProbe{ClassName: p.ClassName, MethodName: p.MethodName}
}

func (p CustomProbe) golang() GolangCustomProbe {
	return GolangCustomProbe{
		PackageName:        p.PackageName,
		FunctionName:       p.FunctionName,
		ReceiverName:       p.ReceiverName,
		ReceiverMethodName: p.ReceiverMethodName,
	}
}

func (p CustomProbe) php() PhpCustomProbe {
	return PhpCustomProbe{ClassName: p.ClassName, FunctionName: p.FunctionName}
}

func (p CustomProbe) python() PythonCustomProbe {
	return PythonCustomProbe{Module: p.Module, Qualname: p.Qualname, Captures: p.Captures}
}

// Target names what the probe instruments, in the form the probe types print
// themselves. Two probes of one language with the same target are the same
// probe. It is empty for an unknown language.
func (p CustomProbe) Target() string {
	switch p.Language {
	case CustomProbeLanguageJava:
		probe := p.java()
		return probe.String()
	case CustomProbeLanguageGo:
		probe := p.golang()
		return probe.String()
	case CustomProbeLanguageCpp:
		return p.Signature
	case CustomProbeLanguagePhp:
		probe := p.php()
		return probe.String()
	case CustomProbeLanguagePython:
		probe := p.python()
		return probe.String()
	default:
		return ""
	}
}

// CustomInstrumentations returns the probe as a rule's custom instrumentations
// with meta attached, after verifying it.
func (p CustomProbe) CustomInstrumentations(meta CustomProbeMetadata) (*CustomInstrumentationsWithMetadata, error) {
	ci := &CustomInstrumentationsWithMetadata{}
	var err error
	switch p.Language {
	case CustomProbeLanguageJava:
		probe := JavaCustomProbeWithMetadata{JavaCustomProbe: p.java(), CustomProbeMetadata: meta}
		err = probe.Verify()
		ci.Java = []JavaCustomProbeWithMetadata{probe}
	case CustomProbeLanguageGo:
		probe := GolangCustomProbeWithMetadata{GolangCustomProbe: p.golang(), CustomProbeMetadata: meta}
		err = probe.Verify()
		ci.Golang = []GolangCustomProbeWithMetadata{probe}
	case CustomProbeLanguageCpp:
		probe := CppCustomProbeWithMetadata{CppCustomProbe: CppCustomProbe{Signature: p.Signature}, CustomProbeMetadata: meta}
		err = probe.Verify()
		ci.Cpp = []CppCustomProbeWithMetadata{probe}
	case CustomProbeLanguagePhp:
		probe := PhpCustomProbeWithMetadata{PhpCustomProbe: p.php(), CustomProbeMetadata: meta}
		err = probe.Verify()
		ci.Php = []PhpCustomProbeWithMetadata{probe}
	case CustomProbeLanguagePython:
		probe := PythonCustomProbeWithMetadata{PythonCustomProbe: p.python(), CustomProbeMetadata: meta}
		err = probe.Verify()
		ci.Python = []PythonCustomProbeWithMetadata{probe}
	default:
		return nil, fmt.Errorf("%w %q", ErrUnknownCustomProbeLanguage, p.Language)
	}
	if err != nil {
		return nil, err
	}
	return ci, nil
}
