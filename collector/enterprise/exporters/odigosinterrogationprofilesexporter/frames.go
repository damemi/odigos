package odigosinterrogationprofilesexporter

import (
	"strings"

	"go.opentelemetry.io/collector/pdata/pprofile"

	"github.com/odigos-io/odigos/common/interrogation"
)

const (
	frameTypeJVM     = "jvm"
	frameTypeHotspot = "hotspot"

	// Substring of jvm FunctionName values from the Java agent utils package
	// (e.g. "void dev.keyval.ebpfJava.utils.ThreadContext.restoreContext(...)").
	instrumentationUtilsPackage = "dev.keyval.ebpfJava.utils."
)

// rootFirstJavaFrames keeps jvm/hotspot frames with non-empty names and reverses
// leaf-first stacks to root-first. Parallel arrays stay aligned. Instrumentation
// scaffolding (agent utils package, funcEnter/funcExit advice) and runtime frames are
// dropped. Library frames are kept, but each consecutive run collapses to its root-most
// frame so app -> library -> app paths survive without framework noise.
func rootFirstJavaFrames(names, frameTypes []string) (outNames, outTypes []string) {
	n := len(names)
	if n == 0 || n != len(frameTypes) {
		return nil, nil
	}
	var kinds []interrogation.FrameKind
	for i := 0; i < n; i++ {
		name := names[i]
		ft := frameTypes[i]
		if name == "" || (ft != frameTypeJVM && ft != frameTypeHotspot) {
			continue
		}
		if isInstrumentationFrame(name) {
			continue
		}
		kind := interrogation.ClassifyFrame(name)
		if kind == interrogation.FrameKindRuntime {
			continue
		}
		// Leaf-first: a library frame followed by another library frame is a deeper
		// frame of the same run, so replace it with the root-more one.
		if last := len(kinds) - 1; kind == interrogation.FrameKindLibrary && last >= 0 && kinds[last] == interrogation.FrameKindLibrary {
			outNames[last], outTypes[last] = name, ft
			continue
		}
		outNames = append(outNames, name)
		outTypes = append(outTypes, ft)
		kinds = append(kinds, kind)
	}
	if len(outNames) == 0 {
		return nil, nil
	}
	outNames, outTypes = capFrames(outNames, outTypes, kinds, maxFramesPerSample)
	for i, j := 0, len(outNames)-1; i < j; i, j = i+1, j-1 {
		outNames[i], outNames[j] = outNames[j], outNames[i]
		outTypes[i], outTypes[j] = outTypes[j], outTypes[i]
	}
	return outNames, outTypes
}

func isInstrumentationFrame(name string) bool {
	return strings.Contains(name, instrumentationUtilsPackage) ||
		strings.Contains(name, ".funcEnter(") ||
		strings.Contains(name, ".funcExit(")
}

func resolveStackFrames(dic pprofile.ProfilesDictionary, stackIndex int32) (functionNames, frameTypes []string) {
	stackTable := dic.StackTable()
	if stackIndex < 0 || int(stackIndex) >= stackTable.Len() {
		return nil, nil
	}

	stringTable := dic.StringTable()
	locationTable := dic.LocationTable()
	functionTable := dic.FunctionTable()
	attrTable := dic.AttributeTable()

	locationIndices := stackTable.At(int(stackIndex)).LocationIndices()
	for li := 0; li < locationIndices.Len(); li++ {
		locIdx := locationIndices.At(li)
		if locIdx < 0 || int(locIdx) >= locationTable.Len() {
			continue
		}

		location := locationTable.At(int(locIdx))
		frameType := locationFrameType(location, attrTable, stringTable)
		lines := location.Lines()
		if lines.Len() == 0 {
			functionNames = append(functionNames, "")
			frameTypes = append(frameTypes, frameType)
			continue
		}

		for ln := 0; ln < lines.Len(); ln++ {
			line := lines.At(ln)
			functionName := ""
			fnIdx := line.FunctionIndex()
			if fnIdx >= 0 && int(fnIdx) < functionTable.Len() {
				fn := functionTable.At(int(fnIdx))
				functionName = stringAt(stringTable, fn.NameStrindex())
			}
			functionNames = append(functionNames, functionName)
			frameTypes = append(frameTypes, frameType)
		}
	}
	return functionNames, frameTypes
}

func resolveLinkIDs(dic pprofile.ProfilesDictionary, linkIndex int32) (traceID, spanID []byte) {
	linkTable := dic.LinkTable()
	if linkIndex <= 0 || int(linkIndex) >= linkTable.Len() {
		return nil, nil
	}
	link := linkTable.At(int(linkIndex))
	tid := link.TraceID()
	sid := link.SpanID()
	if !tid.IsEmpty() {
		traceID = append([]byte(nil), tid[:]...)
	}
	if !sid.IsEmpty() {
		spanID = append([]byte(nil), sid[:]...)
	}
	return traceID, spanID
}
