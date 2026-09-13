// Package leds sets the S1's armour LEDs, so two vehicles on one console can be
// told apart by looking at them rather than by trusting a label.
//
// This is reverse engineering, and the package is honest about it. The bridge
// exposes KeyRobomasterSystemLEDColor as writable but carries no decoded type
// for it, and nothing in the upstream library ever writes it — there is no
// reference implementation to copy. So the payload shape is a hypothesis until
// a real robot changes colour.
//
// # Why not SetKeyValue
//
// unitybridge.SetKeyValue type-checks the value against key.ResultValue(), and
// ResultValue **panics** for a key with no decoded type:
//
//	panic(fmt.Sprintf("Unknown result value for key %s.", k.name))
//
// Calling it on this key would take the vehicle process down rather than return
// an error. So we build the same TypeSetValue event it would have built and
// send the JSON ourselves, skipping only the check that cannot succeed here.
//
// # Why the robot is the only oracle
//
// The send path is fire-and-forget: no acknowledgement comes back, and a nil
// error means "the bytes left", not "the robot obeyed" — the same trap that
// once made a motion run look successful against a stationary chassis
// (DECISIONS.md #12). The only way to know a format works is to look at the
// vehicle.
package leds

import (
	"encoding/json"
	"fmt"

	"github.com/brunoga/robomaster/unitybridge"
	"github.com/brunoga/robomaster/unitybridge/unity/event"
	"github.com/brunoga/robomaster/unitybridge/unity/key"
)

// Colour is an 8-bit-per-channel RGB value.
type Colour struct{ R, G, B uint8 }

// Named colours chosen to be unmistakable from across a room, and from each
// other, including for the most common forms of colour blindness — which rules
// out the obvious red/green pairing.
var Named = map[string]Colour{
	"red":    {255, 0, 0},
	"green":  {0, 255, 0},
	"blue":   {0, 80, 255},
	"cyan":   {0, 255, 255},
	"purple": {180, 0, 255},
	"yellow": {255, 200, 0},
	"white":  {255, 255, 255},
	"off":    {0, 0, 0},
}

// Options are the fields of DJI's LED colour message.
//
// # How the schema was found
//
// Not by guessing. The first attempt aborted the vehicle process with:
//
//	json_dto::ex_t: error reading field "deviceID": mandatory field doesn't exist
//
// which proved the payload is JSON and named a required field. Discovering the
// rest one crash at a time was too expensive — each abort costs a restart and
// leaves the robot briefly refusing connections — so the field names were read
// out of DJI's own binary, which carries them as literals:
//
//	deviceID · maxPlayer · numberOfTeams · R · G · B · flashMode · loopCount · time1 · time2
//
// The channels are uppercase R, G, B; every lowercase guess would have failed.
// `strings` could not show them at all, defaulting to a four-character minimum,
// so they had to be read from the raw bytes. The binary names the struct too:
// RMLEDColorMsg, under RMSystemParamLEDColor. The parser then asked for
// controlMode, which is not in that run of names.
//
// # What is still unknown
//
// The *meanings* of deviceID, controlMode and flashMode. A payload that parses
// is not a payload that does anything: the first correctly-shaped message was
// accepted and changed nothing visible, which is the same lesson as #12 — no
// error is not evidence of effect. Only looking at the robot settles it.
type Options struct {
	DeviceID    int
	ControlMode int
	FlashMode   int
	LoopCount   int
	Time1       int
	Time2       int
}

// Default is the option set currently believed most likely to take effect.
// ControlMode 1 on the theory that 0 leaves the LEDs under system control.
var Default = Options{DeviceID: 0, ControlMode: 1, FlashMode: 1, LoopCount: 0, Time1: 0, Time2: 0}

func payload(o Options, c Colour) (string, error) {
	v := map[string]any{
		"deviceID":    o.DeviceID,
		"controlMode": o.ControlMode,
		"R":           c.R,
		"G":           c.G,
		"B":           c.B,
		"flashMode":   o.FlashMode,
		"loopCount":   o.LoopCount,
		"time1":       o.Time1,
		"time2":       o.Time2,
		// Neighbours in the binary's name table. json_dto ignores fields it
		// does not know, so breadth is free while omission aborts the process.
		"taskId":     0,
		"isCancel":   0,
		"effectMode": 0,
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// Set writes a colour to the vehicle's armour LEDs.
//
// A nil return means the event was handed to the bridge, not that the vehicle
// obeyed it. Verify by looking at the robot.
func Set(ub unitybridge.UnityBridge, o Options, c Colour) error {
	if ub == nil {
		return fmt.Errorf("no bridge")
	}
	data, err := payload(o, c)
	if err != nil {
		return err
	}

	// Deliberately not unitybridge.SetKeyValue: see the package comment. This
	// is the same event that function builds, without the check that panics on
	// a key carrying no decoded type.
	ev := event.NewFromTypeAndSubType(event.TypeSetValue,
		key.KeyRobomasterSystemLEDColor.SubType())

	return ub.SendEventWithString(ev, data)
}

// Payload exposes what Set would send, for logging a trial without running it.
func Payload(o Options, c Colour) (string, error) { return payload(o, c) }

// SendRaw sends an arbitrary JSON payload to the LED colour key.
//
// For identifying the wire format, nothing else. The native library parses this
// with json_dto and throws a C++ exception naming the first field it wanted but
// did not find — which makes each failure a usable clue. That exception is
// uncaught and aborts the process, so every wrong guess costs a restart and Go
// cannot turn it into an error value.
func SendRaw(ub unitybridge.UnityBridge, payload string) error {
	if ub == nil {
		return fmt.Errorf("no bridge")
	}
	if !json.Valid([]byte(payload)) {
		// Cheap guard: malformed JSON would abort the process for a reason that
		// has nothing to teach us about the schema.
		return fmt.Errorf("payload is not valid JSON")
	}
	ev := event.NewFromTypeAndSubType(event.TypeSetValue,
		key.KeyRobomasterSystemLEDColor.SubType())
	return ub.SendEventWithString(ev, payload)
}

// PlayEffect performs the LED light-effect action.
//
// The colour key alone parses and does nothing: every combination of deviceID,
// controlMode and flashMode was sent to a real vehicle and observed through a
// second robot's camera, and the LEDs never changed. The binary suggests why —
// alongside RMLEDColorMsg it carries RMSystemPlayLEDLightEffectInfo and
// DJIRMPlayLEDLightEffectPack, and this key is an *action* rather than a value.
// So the colour may only be staged until an effect is played.
func PlayEffect(ub unitybridge.UnityBridge, o Options, c Colour) error {
	if ub == nil {
		return fmt.Errorf("no bridge")
	}
	data, err := payload(o, c)
	if err != nil {
		return err
	}
	ev := event.NewFromTypeAndSubType(event.TypePerformAction,
		key.KeyRobomasterSystemLEDLightEffect.SubType())
	return ub.SendEventWithString(ev, data)
}
