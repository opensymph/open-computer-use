//go:build linux

package main

// Input synthesis goes through the registry daemon's DeviceEventController,
// the same object libatspi's atspi_generate_mouse_event /
// atspi_generate_keyboard_event call. Just like libatspi, transport errors are
// swallowed: the Python bridge never saw them, so neither does the caller.

func (c *atspiConnection) mouseEvent(x, y int, event string) {
	_ = c.bus.Object(atspiDBusNameRegistry, atspiDBusPathDEC).CallWithContext(
		c.ctx, atspiIfaceDEC+".GenerateMouseEvent", 0, int32(x), int32(y), event)
}

func (c *atspiConnection) keyEvent(keyval uint32, keystr string, synthType uint32) {
	_ = c.bus.Object(atspiDBusNameRegistry, atspiDBusPathDEC).CallWithContext(
		c.ctx, atspiIfaceDEC+".GenerateKeyboardEvent", 0, int32(keyval), keystr, synthType)
}
