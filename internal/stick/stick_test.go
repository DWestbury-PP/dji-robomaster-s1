package stick

import "testing"

// The Y inversion is the whole point of this package: it was found by driving
// the robot and watching it go the wrong way, which no unit test would have
// caught. This one keeps it from coming back.
func TestYIsInvertedAndXIsNot(t *testing.T) {
	cases := []struct {
		name           string
		x, y           float64
		wantVX, wantVY float64
	}{
		{"forward is negative on the wire", 0, 1, 0, -1},
		{"back is positive on the wire", 0, -1, 0, 1},
		{"turret up is negative on the wire", 0, 0.5, 0, -0.5},
		{"right is unchanged", 1, 0, 1, 0},
		{"left is unchanged", -1, 0, -1, 0},
		{"centre stays centre", 0, 0, 0, 0},
		{"combined", 0.4, -0.25, 0.4, 0.25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vx, vy := ToVirtual(c.x, c.y)
			if vx != c.wantVX || vy != c.wantVY {
				t.Fatalf("ToVirtual(%v,%v) = (%v,%v), want (%v,%v)",
					c.x, c.y, vx, vy, c.wantVX, c.wantVY)
			}
		})
	}
}
