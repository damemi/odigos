package odigosautopilotprofilesexporter

import (
	"go.opentelemetry.io/collector/pdata/pprofile"

	"github.com/odigos-io/odigos/common/autopilot"
)

const (
	frameTypeJVM     = "jvm"
	frameTypeHotspot = "hotspot"

	// maxFramesPerSample caps stack depth written per profile sample after filtering.
	maxFramesPerSample = 64
)

// rootFirstJavaFrames keeps jvm/hotspot frames with non-empty names and reverses
// leaf-first stacks to root-first. Parallel arrays stay aligned. Runtime and
// Odigos instrumentation frames are dropped. Stacks deeper than
// maxFramesPerSample keep their leaf-most frames.
func rootFirstJavaFrames(names, frameTypes []string) (outNames, outTypes []string) {
	n := len(names)
	if n == 0 || n != len(frameTypes) {
		return nil, nil
	}
	for i := 0; i < n; i++ {
		name := names[i]
		ft := frameTypes[i]
		if name == "" || (ft != frameTypeJVM && ft != frameTypeHotspot) {
			continue
		}

		frameClassification := autopilot.ClassifyFrame(name)
		switch frameClassification {
		case autopilot.FrameKindRuntime, autopilot.FrameKindInstrumentation:
			continue
		case autopilot.FrameKindApp:
			outNames = append(outNames, name)
			outTypes = append(outTypes, ft)
		}
	}
	if len(outNames) == 0 {
		return nil, nil
	}
	if len(outNames) > maxFramesPerSample {
		outNames, outTypes = outNames[:maxFramesPerSample], outTypes[:maxFramesPerSample]
	}
	for i, j := 0, len(outNames)-1; i < j; i, j = i+1, j-1 {
		outNames[i], outNames[j] = outNames[j], outNames[i]
		outTypes[i], outTypes[j] = outTypes[j], outTypes[i]
	}
	return outNames, outTypes
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
