//go:build (linux || freebsd || openbsd || netbsd) && !android

package notify

import (
	"github.com/godbus/dbus/v5"
)

const (
	busName = "org.freedesktop.Notifications"
	objPath = "/org/freedesktop/Notifications"
)

// fdo talks to the freedesktop notification service (mako, dunst,
// swaync, Quickshell, GNOME, KDE) on its own session-bus connection.
type fdo struct {
	conn    *dbus.Conn
	obj     dbus.BusObject
	actions chan Action
	signals chan *dbus.Signal
}

func newDesktop() (Desktop, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(busName), dbus.WithMatchObjectPath(objPath)); err != nil {
		conn.Close()
		return nil, err
	}
	d := &fdo{conn: conn, obj: conn.Object(busName, objPath), actions: make(chan Action, 8), signals: make(chan *dbus.Signal, 16)}
	conn.Signal(d.signals)
	go d.loop()
	return d, nil
}

func (d *fdo) loop() {
	for sig := range d.signals {
		if sig.Name != busName+".ActionInvoked" || len(sig.Body) < 2 {
			continue
		}
		id, _ := sig.Body[0].(uint32)
		key, _ := sig.Body[1].(string)
		select {
		case d.actions <- Action{NotificationID: id, Key: key}:
		default:
		}
	}
	close(d.actions)
}

func (d *fdo) Show(summary, body string, timeoutMs int32) (uint32, error) {
	actions := []string{"default", "Open", "allow", "Allow once", "deny", "Deny"}
	hints := map[string]dbus.Variant{
		"urgency":       dbus.MakeVariant(byte(2)), // critical: stays until answered
		"desktop-entry": dbus.MakeVariant("smart-control"),
		"category":      dbus.MakeVariant("presence"),
	}
	var id uint32
	err := d.obj.Call(busName+".Notify", 0, "Smart Control", uint32(0), "dialog-question",
		summary, body, actions, hints, timeoutMs).Store(&id)
	return id, err
}

func (d *fdo) Close(id uint32) { d.obj.Call(busName+".CloseNotification", 0, id) }

func (d *fdo) Actions() <-chan Action { return d.actions }

func (d *fdo) Shutdown() { d.conn.Close() }
