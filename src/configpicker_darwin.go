//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>
#include <string.h>

// pickConfigFile shows a native, modal NSOpenPanel restricted to a single
// regular file (no directories, no multiple selection). Must be called from
// the main thread, and only before any other Cocoa event loop (e.g.
// systray.Run) has started — NSApplication needs to be minimally
// initialized for the panel to display correctly when no app event loop is
// already running.
//
// Returns a heap-allocated, NUL-terminated C string with the selected path,
// or NULL if the user cancelled. Caller must free the returned pointer with
// free() (done from Go via C.free, see pickConfigFileOrEmpty below).
static char *pickConfigFile(void) {
    @autoreleasepool {
        // NSOpenPanel requires a minimally running NSApplication to present
        // correctly even outside a full Cocoa event loop.
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];

        NSOpenPanel *panel = [NSOpenPanel openPanel];
        [panel setCanChooseFiles:YES];
        [panel setCanChooseDirectories:NO];
        [panel setAllowsMultipleSelection:NO];
        [panel setTitle:@"Select xray-cli config"];
        [panel setPrompt:@"Select"];
        // No file type filter — any file is selectable; validation happens
        // on the Go side after a path is returned.

        // Bring the panel to the front of a process with no existing
        // windows; without this it can appear behind other apps.
        [NSApp activateIgnoringOtherApps:YES];

        NSInteger result = [panel runModal];
        if (result != NSModalResponseOK) {
            return NULL;
        }

        NSURL *url = [[panel URLs] firstObject];
        if (url == nil) {
            return NULL;
        }
        const char *path = [[url path] UTF8String];
        if (path == NULL) {
            return NULL;
        }
        return strdup(path);
    }
}
*/
import "C"

import "unsafe"

// PickConfigFile shows a native, modal macOS file picker and returns the
// selected path, or "" if the user cancelled. Must be called from the main
// goroutine, before systray.Run / runTray starts — NSOpenPanel and
// systray's own Cocoa event loop both need the main thread, and must not
// run concurrently.
func PickConfigFile() string {
	cstr := C.pickConfigFile()
	if cstr == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cstr))
	return C.GoString(cstr)
}
