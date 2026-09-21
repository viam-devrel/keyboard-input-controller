package keyboard

import (
  input "go.viam.com/rdk/components/input"
  "context"
"sync"
"time"
pb "go.viam.com/api/component/inputcontroller/v1"
"go.viam.com/utils"
"go.viam.com/utils/protoutils"
"go.viam.com/utils/rpc"
"google.golang.org/protobuf/types/known/structpb"
"google.golang.org/protobuf/types/known/timestamppb"
"go.viam.com/rdk/logging"
rprotoutils "go.viam.com/rdk/protoutils"
"go.viam.com/rdk/resource"
)

var (
	Input = resource.NewModel("devrel", "keyboard", "input")
	errUnimplemented = errors.New("unimplemented")
)

func init() {
	resource.RegisterComponent(input.API, Input,
		resource.Registration[input.Controller, Config]{
			Constructor: newKeyboardInput,
		},
	)
}

type Config struct {
	/*
	Put config attributes here. There should be public/exported fields
	with a `json` parameter at the end of each attribute.

	Example config struct:
		type Config struct {
			Pin   string `json:"pin"`
			Board string `json:"board"`
			MinDeg *float64 `json:"min_angle_deg,omitempty"`
		}

	If your model does not need a config, replace Config in the init
	function with resource.NoNativeConfig
	*/
}

// Validate ensures all parts of the config are valid and important fields exist.
// Returns three values:
//   1. Required dependencies: other resources that must exist for this resource to work.
//   2. Optional dependencies: other resources that may exist but are not required.
//   3. An error if any Config fields are missing or invalid.
//
// The `path` parameter indicates
// where this resource appears in the machine's JSON configuration
// (for example, "components.0"). You can use it in error messages
// to indicate which resource has a problem.
//
// Note: Validate receives a copy of the config; mutations to it will do
// nothing. Fill in any default values in your resource's constructor function
// instead.
func (cfg Config) Validate(path string) ([]string, []string, error) {
	// Add config validation code here
	 return nil, nil, nil
}

type keyboardInput struct {
	resource.AlwaysRebuild
	resource.Named

	name   resource.Name

	logger logging.Logger
	cfg    Config

	cancelCtx  context.Context
	cancelFunc func()
}

func newKeyboardInput(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (input.Controller, error) {
	conf, err := resource.NativeConfig[Config](rawConf)
	if err != nil {
		return nil, err
	}

    return NewInput(ctx, deps, rawConf.ResourceName(), conf, logger)

}

func NewInput(ctx context.Context, deps resource.Dependencies, name resource.Name, conf Config, logger logging.Logger) (input.Controller, error) {

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &keyboardInput{
		name:       name,
		logger:     logger,
		cfg:        conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}
	return s, nil
}

func (s *keyboardInput) Name() resource.Name {
	return s.name
}

// Controls returns a list of Controls provided by the Controller
func (s *keyboardInput) Controls(ctx context.Context, extra map[string]interface{}) ([]input.Control, error) {
	return nil, fmt.Errorf("not implemented")
}

 // Events returns most recent Event for each input (which should be the current state)
func (s *keyboardInput) Events(ctx context.Context, extra map[string]interface{}) (map[input.Control]input.Event, error) {
	return nil, fmt.Errorf("not implemented")
}

 func (s *keyboardInput) TriggerEvent(ctx context.Context, event input.Event, extra map[string]interface{}) error {
	return fmt.Errorf("not implemented")
}

 // RegisterCallback registers a callback that will fire on given EventTypes for a given Control.
// The callback is called on the same goroutine as the firer and if any long operation is to occur,
// the callback should start a goroutine.
func (s *keyboardInput) RegisterControlCallback(ctx context.Context, control input.Control, triggers []input.EventType, ctrlFunc input.ControlFunction, extra map[string]interface{}) error {
	return fmt.Errorf("not implemented")
}

 func (s *keyboardInput) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}

 func (s *keyboardInput) Status(ctx context.Context) (map[string]interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}



func (s *keyboardInput) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}
