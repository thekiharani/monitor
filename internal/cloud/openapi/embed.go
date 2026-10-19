package openapi

import _ "embed"

//go:embed openapi.yaml
var Spec string

//go:embed control.openapi.yaml
var ControlSpec string
