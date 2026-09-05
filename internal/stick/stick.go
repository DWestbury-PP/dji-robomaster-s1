// Package stick adapts our command convention to the DJI virtual stick.
//
// We use the convention documented on safety.Command throughout: **positive Y
// is forward (chassis) and up (gimbal)**, positive X is right. The library's
// controller.StickPosition does not — its InterpolatedY negates Y before
// mapping to stick range, while InterpolatedX leaves X alone.
//
// Passing our values in raw therefore inverts forward/back and turret
// up/down while leaving strafe and yaw correct, which is precisely the bug
// this package exists to stop recurring. Every place that builds a
// StickPosition must go through ToVirtual.
package stick

// ToVirtual converts (x, y) in our convention to the axis values the DJI
// virtual stick expects.
func ToVirtual(x, y float64) (vx, vy float64) {
	return x, -y
}
