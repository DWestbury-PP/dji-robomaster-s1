package main

// SceneSchema is what we ask a model to fill in. Design notes, because the
// shape matters as much as the model:
//
//   - Kept shallow. Constrained-decoding backends handle deep or recursive
//     schemas badly, and every extra level is somewhere for a model to get
//     lost.
//   - Bearings and proximity are coarse enums, not numbers. VLMs are unreliable
//     at metric estimates and confident about them anyway, which is the worst
//     combination. "ahead-left / near" is something we can actually act on.
//   - `confidence` is the model's own hedge. It is not trustworthy on its own,
//     but a model that says "low" while the fast tier disagrees is a useful
//     signal for fusion.
var SceneSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary": map[string]any{"type": "string"},
		"obstacles": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"object":    map[string]any{"type": "string"},
					"bearing":   enum("left", "ahead-left", "ahead", "ahead-right", "right"),
					"proximity": enum("near", "mid", "far"),
					"blocking":  map[string]any{"type": "boolean"},
				},
				"required": []string{"object", "bearing", "proximity", "blocking"},
			},
		},
		"clear_path": enum("left", "ahead", "right", "none"),
		"hazards":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"people":     map[string]any{"type": "integer"},
		"lighting":   enum("good", "dim", "backlit"),
		"confidence": enum("high", "medium", "low"),
	},
	"required": []string{"summary", "obstacles", "clear_path", "hazards", "people", "lighting", "confidence"},
}

func enum(vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals}
}

// Scene is the parsed form, used for scoring.
type Scene struct {
	Summary   string `json:"summary"`
	Obstacles []struct {
		Object    string `json:"object"`
		Bearing   string `json:"bearing"`
		Proximity string `json:"proximity"`
		Blocking  bool   `json:"blocking"`
	} `json:"obstacles"`
	ClearPath  string   `json:"clear_path"`
	Hazards    []string `json:"hazards"`
	People     int      `json:"people"`
	Lighting   string   `json:"lighting"`
	Confidence string   `json:"confidence"`
}

// Prompt is deliberately explicit about the robot's own hardware. The gel
// blaster barrel is mounted beside the camera and appears in the bottom centre
// of every frame; a model that reports it as an obstacle would have a reflex
// layer backing away from the robot itself.
const Prompt = `You are the vision system of a small ground robot about 20cm tall.
The camera sits just above floor level, so the floor fills the lower half of the view.

IGNORE the robot's own hardware: a barrel and housing are permanently visible at the
bottom centre of every frame. They are part of this robot and are never an obstacle.

Report only what would matter to something driving across this floor. An obstacle is
"near" if the robot would reach it in about a second, "blocking" if it cannot be driven
around. Judge lighting honestly — say "backlit" when bright windows wash out the scene.`
