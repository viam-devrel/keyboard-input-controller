package main

import (
	"keyboard"

	input "go.viam.com/rdk/components/input"
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
)

func main() {
	module.ModularMain(resource.APIModel{API: input.API, Model: keyboard.Input})
}
