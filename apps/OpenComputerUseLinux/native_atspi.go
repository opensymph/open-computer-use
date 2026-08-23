//go:build linux

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// D-Bus names, paths, and interfaces from at-spi2-core atspi-constants.h.
const (
	atspiDBusNameRegistry = "org.a11y.atspi.Registry"
	atspiDBusPathRoot     = "/org/a11y/atspi/accessible/root"
	atspiDBusPathNull     = "/org/a11y/atspi/null"
	atspiDBusPathDEC      = "/org/a11y/atspi/registry/deviceeventcontroller"
	atspiDBusPathCache    = "/org/a11y/atspi/cache"

	atspiIfaceAccessible     = "org.a11y.atspi.Accessible"
	atspiIfaceAction         = "org.a11y.atspi.Action"
	atspiIfaceApplication    = "org.a11y.atspi.Application"
	atspiIfaceCache          = "org.a11y.atspi.Cache"
	atspiIfaceComponent      = "org.a11y.atspi.Component"
	atspiIfaceDEC            = "org.a11y.atspi.DeviceEventController"
	atspiIfaceEditableText   = "org.a11y.atspi.EditableText"
	atspiIfaceText           = "org.a11y.atspi.Text"
	atspiIfaceValue          = "org.a11y.atspi.Value"
	atspiIfaceDBus           = "org.freedesktop.DBus"
	atspiDBusPathDBus        = "/org/freedesktop/DBus"
	atspiSessionBusName      = "org.a11y.Bus"
	atspiSessionBusPath      = "/org/a11y/bus"
	atspiCoordTypeScreen     = 0
	atspiInterfaceNamePrefix = "org.a11y.atspi."
)

// accessibleRef is the AT-SPI2 object reference: a (bus name, object path)
// pair, matching the wire (so) type.
type accessibleRef struct {
	Bus  string
	Path dbus.ObjectPath
}

// cacheItem mirrors one entry of Cache.GetItems:
// a((so)(so)(so)iiassusau). The states pair is decoded into a 64-bit mask,
// exactly like libatspi's _atspi_dbus_set_state.
type cacheItem struct {
	ref        accessibleRef
	childCount int32
	interfaces []string
	name       string
	role       uint32
	states     uint64
	hasStates  bool
}

type cacheItemWire struct {
	Path        accessibleRef
	App         accessibleRef
	Parent      accessibleRef
	Index       int32
	ChildCount  int32
	Interfaces  []string
	Name        string
	Role        uint32
	Description string
	States      []uint32
}

// appCache is one application's Cache.GetItems snapshot, indexed by object
// path. Children are resolved through parent links so a tree walk never
// touches the bus.
type appCache struct {
	items    map[dbus.ObjectPath]*cacheItem
	children map[dbus.ObjectPath][]accessibleRef
}

// atspiConnection owns the two-hop AT-SPI2 connection: the session bus (used
// only to discover the a11y bus address, unless AT_SPI_BUS_ADDRESS is set) and
// the a11y bus itself.
type atspiConnection struct {
	ctx     context.Context
	session *dbus.Conn
	bus     *dbus.Conn
	pids    map[string]int
	apps    map[string]*appCache
}

// connectATSPI mirrors what Atspi.init() plus the first registry access did in
// the Python bridge: session bus -> org.a11y.Bus.GetAddress -> a11y bus.
func connectATSPI(ctx context.Context, env map[string]string) (*atspiConnection, error) {
	a11yAddress := strings.TrimSpace(env["AT_SPI_BUS_ADDRESS"])
	var session *dbus.Conn
	if a11yAddress == "" {
		address := strings.TrimSpace(env["DBUS_SESSION_BUS_ADDRESS"])
		if address == "" {
			return nil, fmt.Errorf("DBUS_SESSION_BUS_ADDRESS is not set")
		}
		conn, err := dbus.Connect(address, dbus.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("cannot connect to the session bus: %v", err)
		}
		session = conn
		call := session.Object(atspiSessionBusName, atspiSessionBusPath).
			CallWithContext(ctx, atspiSessionBusName+".GetAddress", 0)
		if call.Err != nil {
			session.Close()
			return nil, fmt.Errorf("cannot query the AT-SPI bus address: %v", call.Err)
		}
		if err := call.Store(&a11yAddress); err != nil || a11yAddress == "" {
			session.Close()
			if err == nil {
				err = fmt.Errorf("empty address")
			}
			return nil, fmt.Errorf("cannot query the AT-SPI bus address: %v", err)
		}
	}
	bus, err := dbus.Connect(a11yAddress, dbus.WithContext(ctx))
	if err != nil {
		if session != nil {
			session.Close()
		}
		return nil, fmt.Errorf("cannot connect to the AT-SPI bus: %v", err)
	}
	return &atspiConnection{
		ctx:     ctx,
		session: session,
		bus:     bus,
		pids:    map[string]int{},
		apps:    map[string]*appCache{},
	}, nil
}

func (c *atspiConnection) Close() {
	if c.bus != nil {
		_ = c.bus.Close()
	}
	if c.session != nil {
		_ = c.session.Close()
	}
}

func (c *atspiConnection) object(ref accessibleRef) dbus.BusObject {
	return c.bus.Object(ref.Bus, ref.Path)
}

// refreshAppCache mirrors libatspi's per-application cache fill: one
// Cache.GetItems call on the application's own bus name. Apps that do not
// implement the cache (GTK4) leave the snapshot nil and every node falls back
// to live calls.
func (c *atspiConnection) refreshAppCache(busName string) *appCache {
	cache := &appCache{}
	call := c.bus.Object(busName, atspiDBusPathCache).
		CallWithContext(c.ctx, atspiIfaceCache+".GetItems", 0)
	var wire []cacheItemWire
	if call.Err == nil {
		if err := call.Store(&wire); err != nil {
			call.Err = err
		}
	}
	if call.Err == nil {
		cache.items = map[dbus.ObjectPath]*cacheItem{}
		cache.children = map[dbus.ObjectPath][]accessibleRef{}
		for _, entry := range wire {
			item := &cacheItem{
				ref:        entry.Path,
				childCount: entry.ChildCount,
				interfaces: entry.Interfaces,
				name:       entry.Name,
				role:       entry.Role,
			}
			if len(entry.States) == 2 {
				item.states = uint64(entry.States[0]) | uint64(entry.States[1])<<32
				item.hasStates = true
			}
			cache.items[entry.Path.Path] = item
			cache.children[entry.Parent.Path] = append(cache.children[entry.Parent.Path], entry.Path)
		}
	}
	c.apps[busName] = cache
	return cache
}

// appCacheFor returns the current snapshot for the application's bus name
// without fetching; nil when the app has not been refreshed yet.
func (c *atspiConnection) appCacheFor(busName string) *appCache {
	return c.apps[busName]
}

// dbNode is the D-Bus-backed atspiNode. Following runtime.py's safe()
// wrapper, every method collapses transport errors to the zero value.
type dbNode struct {
	conn *atspiConnection
	ref  accessibleRef
}

func (n *dbNode) isDesktopRoot() bool {
	return n.ref.Bus == atspiDBusNameRegistry && n.ref.Path == atspiDBusPathRoot
}

func (n *dbNode) isAppRoot() bool {
	return n.ref.Bus != atspiDBusNameRegistry && n.ref.Path == atspiDBusPathRoot
}

// cachedItem returns the snapshot entry for this node. Application roots
// refresh their cache on structural access so every tree walk sees fresh
// state, like the Python bridge's live reads; nodes below the root then read
// from that one consistent snapshot.
func (n *dbNode) cachedItem() *cacheItem {
	if n.isDesktopRoot() {
		return nil
	}
	cache := n.conn.appCacheFor(n.ref.Bus)
	if cache == nil || cache.items == nil {
		return nil
	}
	return cache.items[n.ref.Path]
}

func (n *dbNode) Name() string {
	if item := n.cachedItem(); item != nil {
		return item.name
	}
	value, err := n.conn.object(n.ref).GetProperty(atspiIfaceAccessible + ".Name")
	if err != nil {
		return ""
	}
	name, _ := value.Value().(string)
	return name
}

func (n *dbNode) RoleName() string {
	if item := n.cachedItem(); item != nil && int(item.role) < len(atspiRoleNames) {
		return atspiRoleNames[item.role]
	}
	var role string
	call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceAccessible+".GetRoleName", 0)
	if call.Err != nil || call.Store(&role) != nil {
		return ""
	}
	return role
}

func (n *dbNode) ChildCount() int {
	if n.isAppRoot() {
		n.conn.refreshAppCache(n.ref.Bus)
	}
	if item := n.cachedItem(); item != nil {
		if item.childCount < 0 {
			return 0
		}
		return int(item.childCount)
	}
	value, err := n.conn.object(n.ref).GetProperty(atspiIfaceAccessible + ".ChildCount")
	if err != nil {
		return 0
	}
	count, ok := value.Value().(int32)
	if !ok || count < 0 {
		return 0
	}
	return int(count)
}

func (n *dbNode) ChildAt(index int) atspiNode {
	if index < 0 {
		return nil
	}
	if n.isAppRoot() {
		n.conn.refreshAppCache(n.ref.Bus)
	}
	if !n.isDesktopRoot() {
		if cache := n.conn.appCacheFor(n.ref.Bus); cache != nil && cache.items != nil {
			if _, ok := cache.items[n.ref.Path]; ok {
				children := cache.children[n.ref.Path]
				if index >= len(children) {
					return nil
				}
				return &dbNode{conn: n.conn, ref: children[index]}
			}
		}
	}
	var child accessibleRef
	call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceAccessible+".GetChildAtIndex", 0, int32(index))
	if call.Err != nil || call.Store(&child) != nil {
		return nil
	}
	if child.Bus == "" || child.Path == "" || child.Path == atspiDBusPathNull {
		return nil
	}
	return &dbNode{conn: n.conn, ref: child}
}

// shortInterfaceNames mirrors libatspi's atspi_accessible_get_interfaces: the
// wire carries full names ("org.a11y.atspi.Text") which the client reduces to
// the fixed-order short names, with "Accessible" always present.
var atspiShortInterfaceOrder = []string{
	"Accessible",
	"Action",
	"Application",
	"Collection",
	"Component",
	"Document",
	"EditableText",
	"Hypertext",
	"Hyperlink",
	"Image",
	"Selection",
	"Table",
	"TableCell",
	"Text",
	"Value",
}

func shortInterfaceNames(wire []string) []string {
	present := map[string]bool{"Accessible": true}
	for _, name := range wire {
		short := strings.TrimPrefix(name, atspiInterfaceNamePrefix)
		for _, known := range atspiShortInterfaceOrder {
			if short == known {
				present[known] = true
				break
			}
		}
	}
	out := make([]string, 0, len(present))
	for _, known := range atspiShortInterfaceOrder {
		if present[known] {
			out = append(out, known)
		}
	}
	return out
}

func (n *dbNode) Interfaces() []string {
	if item := n.cachedItem(); item != nil {
		return shortInterfaceNames(item.interfaces)
	}
	var wire []string
	call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceAccessible+".GetInterfaces", 0)
	if call.Err != nil || call.Store(&wire) != nil {
		return nil
	}
	return shortInterfaceNames(wire)
}

func (n *dbNode) AccessibleID() string {
	value, err := n.conn.object(n.ref).GetProperty(atspiIfaceAccessible + ".AccessibleId")
	if err != nil {
		return ""
	}
	id, _ := value.Value().(string)
	return id
}

// PID mirrors node_pid: GetConnectionUnixProcessID on the a11y bus, cached per
// bus name (unique names never recycle within a bus lifetime), 0 on failure.
func (n *dbNode) PID() int {
	if pid, ok := n.conn.pids[n.ref.Bus]; ok {
		return pid
	}
	var pid uint32
	call := n.conn.bus.Object(atspiIfaceDBus, atspiDBusPathDBus).
		CallWithContext(n.conn.ctx, atspiIfaceDBus+".GetConnectionUnixProcessID", 0, n.ref.Bus)
	if call.Err != nil || call.Store(&pid) != nil {
		return 0
	}
	n.conn.pids[n.ref.Bus] = int(pid)
	return int(pid)
}

func (n *dbNode) stateMask() (uint64, bool) {
	if item := n.cachedItem(); item != nil {
		return item.states, item.hasStates
	}
	var states []uint32
	call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceAccessible+".GetState", 0)
	if call.Err != nil || call.Store(&states) != nil || len(states) != 2 {
		return 0, false
	}
	return uint64(states[0]) | uint64(states[1])<<32, true
}

func (n *dbNode) StateContains(state uint32) bool {
	if state >= 64 {
		return false
	}
	mask, ok := n.stateMask()
	return ok && mask&(uint64(1)<<state) != 0
}

func (n *dbNode) ComponentExtents() (x, y, width, height int32, ok bool) {
	// GetExtents returns ONE out arg of type (iiii), not four values.
	var rect struct {
		X      int32
		Y      int32
		Width  int32
		Height int32
	}
	call := n.conn.object(n.ref).CallWithContext(
		n.conn.ctx, atspiIfaceComponent+".GetExtents", 0, uint32(atspiCoordTypeScreen))
	if call.Err != nil || call.Store(&rect) != nil {
		return 0, 0, 0, 0, false
	}
	return rect.X, rect.Y, rect.Width, rect.Height, true
}

func (n *dbNode) ToolkitName() string {
	value, err := n.conn.object(n.ref).GetProperty(atspiIfaceApplication + ".ToolkitName")
	if err != nil {
		return ""
	}
	name, _ := value.Value().(string)
	return name
}

func (n *dbNode) CharacterCount() int {
	value, err := n.conn.object(n.ref).GetProperty(atspiIfaceText + ".CharacterCount")
	if err != nil {
		return 0
	}
	count, ok := value.Value().(int32)
	if !ok {
		return 0
	}
	return int(count)
}

func (n *dbNode) TextRange(start, end int) string {
	var text string
	call := n.conn.object(n.ref).CallWithContext(
		n.conn.ctx, atspiIfaceText+".GetText", 0, int32(start), int32(end))
	if call.Err != nil || call.Store(&text) != nil {
		return ""
	}
	return text
}

func (n *dbNode) SelectionCount() int {
	var count int32
	call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceText+".GetNSelections", 0)
	if call.Err != nil || call.Store(&count) != nil {
		return 0
	}
	return int(count)
}

func (n *dbNode) Selection(index int) (start, end int, ok bool) {
	var s, e int32
	call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceText+".GetSelection", 0, int32(index))
	if call.Err != nil || call.Store(&s, &e) != nil {
		return 0, 0, false
	}
	return int(s), int(e), true
}

func (n *dbNode) InsertText(position int, text string, length int) bool {
	var ok bool
	call := n.conn.object(n.ref).CallWithContext(
		n.conn.ctx, atspiIfaceEditableText+".InsertText", 0, int32(position), text, int32(length))
	if call.Err != nil || call.Store(&ok) != nil {
		return false
	}
	return ok
}

func (n *dbNode) SetTextContents(text string) bool {
	var ok bool
	call := n.conn.object(n.ref).CallWithContext(
		n.conn.ctx, atspiIfaceEditableText+".SetTextContents", 0, text)
	if call.Err != nil || call.Store(&ok) != nil {
		return false
	}
	return ok
}

func (n *dbNode) CurrentValue() (float64, bool) {
	value, err := n.conn.object(n.ref).GetProperty(atspiIfaceValue + ".CurrentValue")
	if err != nil {
		return 0, false
	}
	current, ok := value.Value().(float64)
	return current, ok
}

// SetCurrentValue mirrors libatspi's atspi_value_set_current_value: the
// CurrentValue property setter, not a dedicated method.
func (n *dbNode) SetCurrentValue(value float64) bool {
	return n.conn.object(n.ref).SetProperty(
		atspiIfaceValue+".CurrentValue", dbus.MakeVariant(value)) == nil
}

// Actions mirrors Python's per-index get_action_name/get_action_description
// loop over get_n_actions; individual failures degrade to empty strings.
func (n *dbNode) Actions() []atspiAction {
	value, err := n.conn.object(n.ref).GetProperty(atspiIfaceAction + ".NActions")
	if err != nil {
		return nil
	}
	count, ok := value.Value().(int32)
	if !ok || count <= 0 {
		return nil
	}
	actions := make([]atspiAction, 0, count)
	for i := int32(0); i < count; i++ {
		var name, description string
		call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceAction+".GetName", 0, i)
		if call.Err == nil {
			_ = call.Store(&name)
		}
		call = n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceAction+".GetDescription", 0, i)
		if call.Err == nil {
			_ = call.Store(&description)
		}
		actions = append(actions, atspiAction{Name: name, Description: description})
	}
	return actions
}

func (n *dbNode) DoAction(index int) bool {
	var ok bool
	call := n.conn.object(n.ref).CallWithContext(n.conn.ctx, atspiIfaceAction+".DoAction", 0, int32(index))
	if call.Err != nil || call.Store(&ok) != nil {
		return false
	}
	return ok
}
