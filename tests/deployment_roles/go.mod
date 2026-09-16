module github.com/bahadrdsr/aspm/tests/deployment_roles

go 1.27.0

toolchain go1.27.1

require (
	github.com/bahadrdsr/aspm v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/bahadrdsr/aspm => ../..
