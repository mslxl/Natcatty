// hwkeyboard detaches or attaches the simulator's hardware keyboard.
//
// CoreSimulator keeps an attached hardware keyboard for each device and
// suppresses the software keyboard while it is attached. `simctl` has no
// command for that state, so the keyboard suite uses the same private
// CoreSimulator entry point the Simulator app does. This helper is a local
// test tool; it is not part of a shipped app.
//
// Usage: hwkeyboard <UDID> <0|1>
#import <Foundation/Foundation.h>
#import <objc/message.h>
#import <objc/runtime.h>
#import <dlfcn.h>
#import <stdio.h>
#import <stdlib.h>

static NSString *developerDir(void) {
    FILE *f = popen("/usr/bin/xcode-select -p", "r");
    if (!f) return nil;
    char buf[4096] = {0};
    size_t n = fread(buf, 1, sizeof(buf) - 1, f);
    pclose(f);
    if (n == 0) return nil;
    while (n > 0 && (buf[n - 1] == '\n' || buf[n - 1] == '\r')) buf[--n] = 0;
    return [NSString stringWithUTF8String:buf];
}

int main(int argc, char **argv) {
    @autoreleasepool {
        if (argc != 3) { fprintf(stderr, "usage: hwkeyboard <UDID> <0|1>\n"); return 2; }
        NSString *devDir = developerDir();
        if (!devDir) { fprintf(stderr, "hwkeyboard: xcode-select -p failed\n"); return 1; }
        // A host Xcode installs CoreSimulator under /Library; the GitHub
        // runner's Xcode keeps it inside the developer dir instead.
        NSString *paths[] = {
            @"/Library/Developer/PrivateFrameworks/CoreSimulator.framework/Versions/A/CoreSimulator",
            [devDir stringByAppendingString:@"/Library/PrivateFrameworks/CoreSimulator.framework/Versions/A/CoreSimulator"],
            [devDir stringByAppendingString:@"/../SharedFrameworks/CoreSimulator.framework/Versions/A/CoreSimulator"],
        };
        BOOL loaded = NO;
        for (size_t i = 0; i < sizeof(paths) / sizeof(paths[0]); i++) {
            if (dlopen([paths[i] UTF8String], RTLD_NOW)) { loaded = YES; break; }
        }
        if (!loaded) {
            fprintf(stderr, "hwkeyboard: no CoreSimulator framework: %s\n", dlerror());
            return 1;
        }
        Class ctxCls = objc_getClass("SimServiceContext");
        if (!ctxCls) { fprintf(stderr, "hwkeyboard: no SimServiceContext\n"); return 1; }
        NSError *err = nil;
        id ctx = ((id(*)(id, SEL, id, NSError **))objc_msgSend)((id)ctxCls,
            sel_registerName("sharedServiceContextForDeveloperDir:error:"), devDir, &err);
        if (!ctx) { fprintf(stderr, "hwkeyboard: no context: %s\n", [[err description] UTF8String]); return 1; }
        id set = ((id(*)(id, SEL, NSError **))objc_msgSend)(ctx, sel_registerName("defaultDeviceSetWithError:"), &err);
        if (!set) { fprintf(stderr, "hwkeyboard: no device set: %s\n", [[err description] UTF8String]); return 1; }
        id byUDID = ((id(*)(id, SEL))objc_msgSend)(set, sel_registerName("devicesByUDID"));
        id key = [[NSUUID alloc] initWithUUIDString:[NSString stringWithUTF8String:argv[1]]];
        id dev = byUDID[key];
        if (!dev) { fprintf(stderr, "hwkeyboard: no device %s\n", argv[1]); return 1; }
        BOOL enabled = atoi(argv[2]) != 0;
        err = nil;
        BOOL ok = ((BOOL(*)(id, SEL, BOOL, unsigned char, NSError **))objc_msgSend)(dev,
            sel_registerName("setHardwareKeyboardEnabled:keyboardType:error:"), enabled, 0, &err);
        if (!ok) { fprintf(stderr, "hwkeyboard: %s\n", err ? [[err description] UTF8String] : "failed"); }
        return ok ? 0 : 1;
    }
}
