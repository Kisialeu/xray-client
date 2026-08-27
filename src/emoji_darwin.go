package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>

struct PNGResult {
	void* data;
	int   len;
};

static struct PNGResult emojiToPNG(const char* emoji, int size) {
	struct PNGResult r = {NULL, 0};

	@autoreleasepool {
		NSString *str = [NSString stringWithUTF8String:emoji];
		NSImage *img  = [[NSImage alloc] initWithSize:NSMakeSize(size, size)];

		[img lockFocus];
		NSDictionary *attrs = @{
			NSFontAttributeName: [NSFont systemFontOfSize:size * 0.78],
		};
		NSSize ts = [str sizeWithAttributes:attrs];
		[str drawAtPoint:NSMakePoint((size - ts.width) / 2,
		                             (size - ts.height) / 2)
		  withAttributes:attrs];
		[img unlockFocus];

		NSBitmapImageRep *rep =
			[[NSBitmapImageRep alloc] initWithData:[img TIFFRepresentation]];
		NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG
		                                properties:@{}];
		if (png && png.length > 0) {
			r.data = malloc(png.length);
			memcpy(r.data, png.bytes, png.length);
			r.len = (int)png.length;
		}
	}
	return r;
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

var emojiIconCache sync.Map

func renderEmojiIcon(emoji string, size int) []byte {
	if v, ok := emojiIconCache.Load(emoji); ok {
		return v.([]byte)
	}

	cs := C.CString(emoji)
	defer C.free(unsafe.Pointer(cs))

	r := C.emojiToPNG(cs, C.int(size))
	if r.data == nil {
		return nil
	}
	defer C.free(r.data)

	png := C.GoBytes(r.data, r.len)
	emojiIconCache.Store(emoji, png)
	return png
}
