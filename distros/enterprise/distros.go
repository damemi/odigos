package distros

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/distros"

	"github.com/odigos-io/odigos-enterprise/distros/yamls"
)

type entepriseDefaulter struct{}

var _ distros.Defaulter = &entepriseDefaulter{}

func NewEnterpriseDefaulter() distros.Defaulter {
	return &entepriseDefaulter{}
}

func (o *entepriseDefaulter) GetDefaultDistroNames() map[common.ProgrammingLanguage]string {
	return map[common.ProgrammingLanguage]string{
		common.JavascriptProgrammingLanguage: "nodejs-enterprise",
		common.PythonProgrammingLanguage:     "python-enterprise",
		common.DotNetProgrammingLanguage:     "dotnet-community",
		common.JavaProgrammingLanguage:       "java-enterprise",
		common.GoProgrammingLanguage:         "golang-enterprise",
		common.MySQLProgrammingLanguage:      "mysql-enterprise",
		common.PhpProgrammingLanguage:        "php-enterprise",
		common.RubyProgrammingLanguage:       "ruby-enterprise",
	}
}

// TODO: When C++ is enabled in k8s, delete the GetDefaultVmDistroNames interface update vm-agent to use GetDefaultDistroNames again ;)
func (o *entepriseDefaulter) GetDefaultVmDistroNames() map[common.ProgrammingLanguage]string {
	distros := o.GetDefaultDistroNames()
	distros[common.CPlusPlusProgrammingLanguage] = "cplusplus-enterprise"
	return distros
}

func NewEnterpriseGetter() (*distros.Getter, error) {
	return distros.NewGetterFromFS(yamls.GetFS())
}

// the provider getter returns both enterprise and community distros.
func NewEnterpriseProvider() (*distros.Provider, error) {

	communityGetter, err := distros.NewCommunityGetter()
	if err != nil {
		return nil, err
	}
	enterpriseGetter, err := NewEnterpriseGetter()
	if err != nil {
		return nil, err
	}

	return distros.NewProvider(NewEnterpriseDefaulter(), communityGetter, enterpriseGetter)
}
