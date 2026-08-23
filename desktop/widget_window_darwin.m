//go:build darwin

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <stdint.h>

static NSWindow *workGround2WidgetWindow;
static NSColor *workGround2SavedBackground;
static NSArray<NSValue *> *workGround2HitRegions;
static NSTimer *workGround2MouseTimer;
static id workGround2MouseGate;
static BOOL workGround2IconMode;
static BOOL workGround2SavedOpaque;
static BOOL workGround2SavedShadow;
static BOOL workGround2SavedIgnoresMouse;
static BOOL workGround2SavedButtonHidden[3];
static NSWindowTitleVisibility workGround2SavedTitleVisibility;
static BOOL workGround2SavedTitlebarTransparent;
static NSColor *workGround2SavedWebViewUnderPageBackground;

static WKWebView *workGround2FindWebView(NSView *view) {
    Class webViewClass = NSClassFromString(@"WKWebView");
    if (webViewClass != Nil && [view isKindOfClass:webViewClass]) {
        return (WKWebView *)view;
    }
    for (NSView *child in [view subviews]) {
        WKWebView *webView = workGround2FindWebView(child);
        if (webView != nil) {
            return webView;
        }
    }
    return nil;
}

static BOOL workGround2ColorIsTransparent(id color) {
    if (![color isKindOfClass:[NSColor class]]) {
        return NO;
    }
    @try {
        return [(NSColor *)color alphaComponent] <= 0.001;
    } @catch (NSException *exception) {
        (void)exception;
        return NO;
    }
}

static void workGround2EnsureWebViewUnderPageTransparent(NSWindow *window) {
    WKWebView *webView = workGround2FindWebView([window contentView]);
    if (webView == nil) {
        return;
    }
    @try {
        id underPage = [webView valueForKey:@"underPageBackgroundColor"];
        if (!workGround2ColorIsTransparent(underPage)) {
            [webView setValue:[NSColor clearColor] forKey:@"underPageBackgroundColor"];
        }
        id drawsBackground = [webView valueForKey:@"drawsBackground"];
        if ([drawsBackground respondsToSelector:@selector(boolValue)] && [drawsBackground boolValue]) {
            [webView setValue:@NO forKey:@"drawsBackground"];
        }
    } @catch (NSException *exception) {
        (void)exception;
    }
}

static void workGround2SetWebViewUnderPageTransparent(NSWindow *window) {
    WKWebView *webView = workGround2FindWebView([window contentView]);
    if (webView == nil) {
        return;
    }
    @try {
        id saved = [webView valueForKey:@"underPageBackgroundColor"];
        if ([saved isKindOfClass:[NSColor class]]) {
            workGround2SavedWebViewUnderPageBackground = [saved retain];
        }
    } @catch (NSException *exception) {
        (void)exception;
    }
    workGround2EnsureWebViewUnderPageTransparent(window);
}

static void workGround2RestoreWebViewUnderPageBackground(NSWindow *window) {
    WKWebView *webView = workGround2FindWebView([window contentView]);
    if (webView != nil) {
        @try {
            [webView setValue:workGround2SavedWebViewUnderPageBackground
                       forKey:@"underPageBackgroundColor"];
        } @catch (NSException *exception) {
            (void)exception;
        }
    }
    [workGround2SavedWebViewUnderPageBackground release];
    workGround2SavedWebViewUnderPageBackground = nil;
}

static void workGround2OnMainSync(dispatch_block_t block) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_sync(dispatch_get_main_queue(), block);
    }
}

static NSWindow *workGround2FindWindow(void) {
    if (workGround2WidgetWindow != nil) {
        return workGround2WidgetWindow;
    }
    NSWindow *window = [NSApp mainWindow];
    if (window == nil) {
        window = [NSApp keyWindow];
    }
    if (window == nil) {
        for (NSWindow *candidate in [NSApp windows]) {
            if ([NSStringFromClass([candidate class]) isEqualToString:@"WailsWindow"] ||
                [[candidate title] isEqualToString:@"WorkGround2"]) {
                window = candidate;
                break;
            }
        }
    }
    return window;
}

static void workGround2UpdateMouseGate(void) {
    NSWindow *window = workGround2WidgetWindow;
    if (!workGround2IconMode || window == nil || ![window isVisible] || [workGround2HitRegions count] == 0) {
        if (window != nil && workGround2IconMode) {
            [window setIgnoresMouseEvents:NO];
        }
        return;
    }

    // Wails applies the App.BackgroundColour after startup/DOM readiness and
    // can also repaint it on a later theme change. Icon mode may begin before
    // either event, so a one-shot clear during entry is not sufficient. The
    // existing mouse-gate timer cheaply repairs only an observed overwrite.
    if ([window isOpaque]) {
        [window setOpaque:NO];
    }
    if (!workGround2ColorIsTransparent([window backgroundColor])) {
        [window setBackgroundColor:[NSColor clearColor]];
    }
    workGround2EnsureWebViewUnderPageTransparent(window);

    // Keep the drag/click gesture captured once it starts in an icon region.
    if ([NSEvent pressedMouseButtons] != 0) {
        [window setIgnoresMouseEvents:NO];
        return;
    }

    NSView *content = [window contentView];
    NSRect bounds = [content bounds];
    NSRect backingBounds = [content convertRectToBacking:bounds];
    NSPoint windowPoint = [window convertPointFromScreen:[NSEvent mouseLocation]];
    NSPoint contentPoint = [content convertPoint:windowPoint fromView:nil];
    CGFloat scaleX = NSWidth(bounds) > 0 ? NSWidth(backingBounds) / NSWidth(bounds) : [window backingScaleFactor];
    CGFloat scaleY = NSHeight(bounds) > 0 ? NSHeight(backingBounds) / NSHeight(bounds) : [window backingScaleFactor];
    NSPoint topLeftBackingPoint = NSMakePoint(
        (contentPoint.x - NSMinX(bounds)) * scaleX,
        (NSMaxY(bounds) - contentPoint.y) * scaleY
    );

    BOOL inside = NO;
    for (NSValue *value in workGround2HitRegions) {
        if (NSPointInRect(topLeftBackingPoint, [value rectValue])) {
            inside = YES;
            break;
        }
    }
    [window setIgnoresMouseEvents:!inside];
}

@interface WorkGround2DesktopIconMouseGate : NSObject
- (void)tick:(NSTimer *)timer;
@end

@implementation WorkGround2DesktopIconMouseGate
- (void)tick:(NSTimer *)timer {
    (void)timer;
    workGround2UpdateMouseGate();
}
@end

static void workGround2StartMouseGate(void) {
    if (workGround2MouseTimer != nil) {
        return;
    }
    workGround2MouseGate = [WorkGround2DesktopIconMouseGate new];
    workGround2MouseTimer = [NSTimer timerWithTimeInterval:0.04
                                                    target:workGround2MouseGate
                                                  selector:@selector(tick:)
                                                  userInfo:nil
                                                   repeats:YES];
    [[NSRunLoop mainRunLoop] addTimer:workGround2MouseTimer forMode:NSRunLoopCommonModes];
}

static void workGround2StopMouseGate(void) {
    [workGround2MouseTimer invalidate];
    workGround2MouseTimer = nil;
    [workGround2MouseGate release];
    workGround2MouseGate = nil;
    [workGround2HitRegions release];
    workGround2HitRegions = nil;
}

int workGround2SetDesktopIconMode(int active) {
    __block int result = 1;
    workGround2OnMainSync(^{
        if (active != 0) {
            NSWindow *window = workGround2FindWindow();
            if (window == nil) {
                result = 0;
                return;
            }
            if (workGround2IconMode && workGround2WidgetWindow == window) {
                return;
            }

            workGround2WidgetWindow = [window retain];
            workGround2SavedBackground = [[window backgroundColor] retain];
            workGround2SavedOpaque = [window isOpaque];
            workGround2SavedShadow = [window hasShadow];
            workGround2SavedIgnoresMouse = [window ignoresMouseEvents];
            workGround2SavedTitleVisibility = [window titleVisibility];
            workGround2SavedTitlebarTransparent = [window titlebarAppearsTransparent];
            NSWindowButton buttons[3] = { NSWindowCloseButton, NSWindowMiniaturizeButton, NSWindowZoomButton };
            for (int i = 0; i < 3; i++) {
                NSButton *button = [window standardWindowButton:buttons[i]];
                workGround2SavedButtonHidden[i] = button == nil ? YES : [button isHidden];
                [button setHidden:YES];
            }

            workGround2IconMode = YES;
            [window setOpaque:NO];
            [window setBackgroundColor:[NSColor clearColor]];
            [window setHasShadow:NO];
            [window setTitleVisibility:NSWindowTitleHidden];
            [window setTitlebarAppearsTransparent:YES];
            [window setIgnoresMouseEvents:NO];
            workGround2SetWebViewUnderPageTransparent(window);
            workGround2StartMouseGate();
            return;
        }

        if (!workGround2IconMode) {
            return;
        }
        NSWindow *window = workGround2WidgetWindow;
        workGround2IconMode = NO;
        workGround2StopMouseGate();
        if (window != nil) {
            workGround2RestoreWebViewUnderPageBackground(window);
            [window setIgnoresMouseEvents:workGround2SavedIgnoresMouse];
            [window setOpaque:workGround2SavedOpaque];
            [window setBackgroundColor:workGround2SavedBackground ?: [NSColor windowBackgroundColor]];
            [window setHasShadow:workGround2SavedShadow];
            [window setTitleVisibility:workGround2SavedTitleVisibility];
            [window setTitlebarAppearsTransparent:workGround2SavedTitlebarTransparent];
            NSWindowButton buttons[3] = { NSWindowCloseButton, NSWindowMiniaturizeButton, NSWindowZoomButton };
            for (int i = 0; i < 3; i++) {
                [[window standardWindowButton:buttons[i]] setHidden:workGround2SavedButtonHidden[i]];
            }
        }
        [workGround2SavedBackground release];
        workGround2SavedBackground = nil;
        [workGround2WidgetWindow release];
        workGround2WidgetWindow = nil;
    });
    return result;
}

int workGround2SetDesktopIconHitRegions(const int32_t *rects, int count) {
    if (rects == NULL || count <= 0) {
        return 0;
    }
    __block int result = 1;
    workGround2OnMainSync(^{
        if (!workGround2IconMode || workGround2WidgetWindow == nil) {
            return;
        }
        NSMutableArray<NSValue *> *next = [[NSMutableArray alloc] initWithCapacity:(NSUInteger)count];
        for (int i = 0; i < count; i++) {
            int offset = i * 4;
            int32_t x = rects[offset];
            int32_t y = rects[offset + 1];
            int32_t width = rects[offset + 2];
            int32_t height = rects[offset + 3];
            if (width <= 0 || height <= 0) {
                continue;
            }
            [next addObject:[NSValue valueWithRect:NSMakeRect(x, y, width, height)]];
        }
        if ([next count] == 0) {
            [next release];
            result = 0;
            return;
        }
        [workGround2HitRegions release];
        workGround2HitRegions = [next copy];
        [next release];
        workGround2UpdateMouseGate();
    });
    return result;
}

int workGround2CurrentWorkArea(int *width, int *height) {
    if (width == NULL || height == NULL) {
        return 0;
    }
    __block int result = 0;
    workGround2OnMainSync(^{
        NSWindow *window = workGround2FindWindow();
        NSScreen *screen = [window screen] ?: [NSScreen mainScreen];
        if (screen == nil) {
            return;
        }
        NSRect visible = [screen visibleFrame];
        *width = (int)NSWidth(visible);
        *height = (int)NSHeight(visible);
        result = *width > 0 && *height > 0;
    });
    return result;
}
