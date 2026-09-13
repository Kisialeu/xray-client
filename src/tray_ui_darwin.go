package main

import (
	"sync"

	"github.com/getlantern/systray"
)

// systray serializes Cocoa dispatch internally, but MenuItem's Go fields are
// updated before dispatch. Keep those field updates serialized as well.
var trayUIMu sync.Mutex

func traySetTitle(item *systray.MenuItem, title string) {
	trayUIMu.Lock()
	defer trayUIMu.Unlock()
	item.SetTitle(title)
}

func trayShow(item *systray.MenuItem) {
	trayUIMu.Lock()
	defer trayUIMu.Unlock()
	item.Show()
}

func trayHide(item *systray.MenuItem) {
	trayUIMu.Lock()
	defer trayUIMu.Unlock()
	item.Hide()
}

func trayDisable(item *systray.MenuItem) {
	trayUIMu.Lock()
	defer trayUIMu.Unlock()
	item.Disable()
}

func traySetIcon(icon []byte) {
	trayUIMu.Lock()
	defer trayUIMu.Unlock()
	systray.SetIcon(icon)
}

func traySetTemplateIcon(template, fallback []byte) {
	trayUIMu.Lock()
	defer trayUIMu.Unlock()
	systray.SetTemplateIcon(template, fallback)
}
