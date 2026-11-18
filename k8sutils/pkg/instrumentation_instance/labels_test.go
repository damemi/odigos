package instrumentation_instance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestInstrumentedAppLabelValue(t *testing.T) {
	longName := "deployment-" + strings.Repeat("a", 242)
	labelValue := InstrumentedAppLabelValue(longName)
	assert.Empty(t, validation.IsValidLabelValue(labelValue))
	assert.Equal(t, labelValue, InstrumentedAppLabelValue(longName))
	assert.NotEqual(t, labelValue, InstrumentedAppLabelValue(longName+"b"))
}
